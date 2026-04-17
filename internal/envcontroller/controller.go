// Package envcontroller reconciles EphemeralEnvironment CRs:
//   - ensures a matching Argo Application exists for the vcluster
//   - deletes the Argo Application when the CR goes away
//   - deletes expired CRs (TTL / hard max-age)
//
// Everything flows through the cluster API — no git commits, no push
// credentials, no worktree.
package envcontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/dusttostars/vcluster-ephemeral-envs/internal/render"
)

var (
	eeGVR  = schema.GroupVersionResource{Group: "ephemeral.io", Version: "v1alpha1", Resource: "ephemeralenvironments"}
	appGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
)

const (
	ownerUIDLabel   = "ephemeral.io/owner-uid"
	finalizerName   = "ephemeral.io/vcluster-cleanup"
	argoNamespace   = "argocd"
	phasePending    = "Pending"
	phaseRunning    = "Running"
	phaseExpired    = "Expired"
	phaseDeleting   = "Deleting"
)

type Controller struct {
	client   dynamic.Interface
	interval time.Duration
	maxAge   time.Duration
}

func NewController(client dynamic.Interface, interval, maxAge time.Duration) *Controller {
	return &Controller{client: client, interval: interval, maxAge: maxAge}
}

func (c *Controller) Run(ctx context.Context) error {
	log.Printf("envcontroller started — interval=%s maxAge=%s", c.interval, c.maxAge)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	if err := c.reconcile(ctx); err != nil {
		log.Printf("reconcile: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.reconcile(ctx); err != nil {
				log.Printf("reconcile: %v", err)
			}
		}
	}
}

func (c *Controller) reconcile(ctx context.Context) error {
	envs, err := c.client.Resource(eeGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing EphemeralEnvironments: %w", err)
	}
	log.Printf("reconciling %d EphemeralEnvironments", len(envs.Items))

	for i := range envs.Items {
		env := &envs.Items[i]
		if err := c.reconcileOne(ctx, env); err != nil {
			log.Printf("reconcile %s/%s: %v", env.GetNamespace(), env.GetName(), err)
		}
	}
	return nil
}

func (c *Controller) reconcileOne(ctx context.Context, env *unstructured.Unstructured) error {
	// Deletion path: finalizer cleans up the Argo Application before the CR disappears.
	if env.GetDeletionTimestamp() != nil {
		return c.finalize(ctx, env)
	}

	if !hasFinalizer(env, finalizerName) {
		addFinalizer(env, finalizerName)
		if _, err := c.client.Resource(eeGVR).Namespace(env.GetNamespace()).Update(ctx, env, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("adding finalizer: %w", err)
		}
		return nil // re-queue on next tick with the updated object
	}

	// Expiration path: delete the CR; the finalizer will clean up the Argo App.
	if c.isExpired(env) {
		log.Printf("%s/%s expired — deleting CR", env.GetNamespace(), env.GetName())
		return c.client.Resource(eeGVR).Namespace(env.GetNamespace()).Delete(ctx, env.GetName(), metav1.DeleteOptions{})
	}

	if err := c.ensureArgoApp(ctx, env); err != nil {
		return fmt.Errorf("ensuring argo app: %w", err)
	}
	return c.updateStatus(ctx, env, phaseRunning, "")
}

func (c *Controller) ensureArgoApp(ctx context.Context, env *unstructured.Unstructured) error {
	spec, _, _ := unstructured.NestedMap(env.Object, "spec")
	tenant, _ := spec["tenant"].(string)
	branch, _ := spec["branch"].(string)
	ttl, _ := spec["ttl"].(string)
	if ttl == "" {
		ttl = "2h"
	}

	p := render.Params{
		Name:     env.GetName(),
		Tenant:   tenant,
		Branch:   branch,
		TTL:      ttl,
		OwnerUID: string(env.GetUID()),
	}

	raw, err := render.Template("vcluster.yaml.tmpl", p)
	if err != nil {
		return err
	}

	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(raw, obj); err != nil {
		return fmt.Errorf("parsing rendered vcluster yaml: %w", err)
	}

	// Server-side apply so we own the fields cleanly and can re-reconcile idempotently.
	data, err := json.Marshal(obj.Object)
	if err != nil {
		return err
	}
	_, err = c.client.Resource(appGVR).Namespace(argoNamespace).Patch(
		ctx, obj.GetName(), types.ApplyPatchType, data,
		metav1.PatchOptions{FieldManager: "ephemeral-controller", Force: ptr(true)},
	)
	return err
}

// finalize runs before the CR is deleted from the API: remove the Argo App
// we created, then clear our finalizer so the CR can go away.
func (c *Controller) finalize(ctx context.Context, env *unstructured.Unstructured) error {
	selector := fmt.Sprintf("%s=%s", ownerUIDLabel, string(env.GetUID()))
	apps, err := c.client.Resource(appGVR).Namespace(argoNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("listing argo apps: %w", err)
	}
	for _, app := range apps.Items {
		log.Printf("finalize %s/%s: deleting argo app %s", env.GetNamespace(), env.GetName(), app.GetName())
		if err := c.client.Resource(appGVR).Namespace(argoNamespace).Delete(ctx, app.GetName(), metav1.DeleteOptions{}); err != nil {
			return err
		}
	}

	removeFinalizer(env, finalizerName)
	_, err = c.client.Resource(eeGVR).Namespace(env.GetNamespace()).Update(ctx, env, metav1.UpdateOptions{})
	return err
}

func (c *Controller) isExpired(env *unstructured.Unstructured) bool {
	created := env.GetCreationTimestamp()
	if created.IsZero() {
		return false
	}
	age := time.Since(created.Time)
	if age > c.maxAge {
		return true
	}
	spec, _, _ := unstructured.NestedMap(env.Object, "spec")
	ttlStr, _ := spec["ttl"].(string)
	if ttlStr == "" {
		return false
	}
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		log.Printf("invalid ttl %q on %s/%s: %v", ttlStr, env.GetNamespace(), env.GetName(), err)
		return false
	}
	return age > ttl
}

func (c *Controller) updateStatus(ctx context.Context, env *unstructured.Unstructured, phase, message string) error {
	created := env.GetCreationTimestamp()
	spec, _, _ := unstructured.NestedMap(env.Object, "spec")
	ttlStr, _ := spec["ttl"].(string)
	ttl, _ := time.ParseDuration(ttlStr)
	expiresAt := created.Add(ttl).UTC().Format(time.RFC3339)

	status := map[string]interface{}{
		"phase":       phase,
		"expiresAt":   expiresAt,
		"vclusterApp": "vcluster-" + env.GetName(),
	}
	if message != "" {
		status["message"] = message
	}
	unstructured.SetNestedMap(env.Object, status, "status")

	_, err := c.client.Resource(eeGVR).Namespace(env.GetNamespace()).UpdateStatus(ctx, env, metav1.UpdateOptions{})
	return err
}

func hasFinalizer(obj *unstructured.Unstructured, name string) bool {
	for _, f := range obj.GetFinalizers() {
		if f == name {
			return true
		}
	}
	return false
}

func addFinalizer(obj *unstructured.Unstructured, name string) {
	obj.SetFinalizers(append(obj.GetFinalizers(), name))
}

func removeFinalizer(obj *unstructured.Unstructured, name string) {
	cur := obj.GetFinalizers()
	out := cur[:0]
	for _, f := range cur {
		if f != name {
			out = append(out, f)
		}
	}
	obj.SetFinalizers(out)
}

func ptr[T any](v T) *T { return &v }
