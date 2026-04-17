package cleanup

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var argoAppGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// Controller watches for expired ephemeral vclusters and removes them.
type Controller struct {
	client   dynamic.Interface
	repoPath string
	interval time.Duration
	maxAge   time.Duration
}

// NewController creates a cleanup controller.
// repoPath is the local clone of the GitOps repo where environment manifests live.
// interval is how often the controller checks for expired environments.
// maxAge is the hard ceiling — no environment lives longer than this regardless of TTL.
func NewController(client dynamic.Interface, repoPath string, interval, maxAge time.Duration) *Controller {
	return &Controller{
		client:   client,
		repoPath: repoPath,
		interval: interval,
		maxAge:   maxAge,
	}
}

// Run starts the reconcile loop. It blocks until the context is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	log.Printf("cleanup controller started — interval=%s maxAge=%s", c.interval, c.maxAge)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Run immediately on start, then on interval.
	if err := c.reconcile(ctx); err != nil {
		log.Printf("reconcile error: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("cleanup controller stopping")
			return ctx.Err()
		case <-ticker.C:
			if err := c.reconcile(ctx); err != nil {
				log.Printf("reconcile error: %v", err)
			}
		}
	}
}

// reconcile finds all ArgoCD Applications managed by us and deletes expired ones.
func (c *Controller) reconcile(ctx context.Context) error {
	apps, err := c.client.Resource(argoAppGVR).Namespace("argocd").List(ctx, metav1.ListOptions{
		LabelSelector: "ephemeral.io/managed-by=ephemeral-controller",
	})
	if err != nil {
		return fmt.Errorf("listing argo applications: %w", err)
	}

	log.Printf("found %d managed ephemeral environments", len(apps.Items))

	var expired []unstructured.Unstructured
	for _, app := range apps.Items {
		if c.isExpired(app) {
			expired = append(expired, app)
		}
	}

	if len(expired) == 0 {
		log.Println("no expired environments found")
		return nil
	}

	log.Printf("found %d expired environments — cleaning up", len(expired))

	for _, app := range expired {
		name := app.GetName()
		labels := app.GetLabels()
		tenant := labels["ephemeral.io/tenant"]

		log.Printf("deleting expired environment: %s (tenant=%s)", name, tenant)

		// Remove the manifest from the GitOps repo so ArgoCD prunes it.
		envName := strings.TrimPrefix(name, "vcluster-")
		if err := c.removeManifest(tenant, envName); err != nil {
			log.Printf("failed to remove manifest for %s: %v", name, err)
			continue
		}

		// Also delete the ArgoCD Application directly for immediate cleanup.
		if err := c.client.Resource(argoAppGVR).Namespace("argocd").Delete(
			ctx, name, metav1.DeleteOptions{},
		); err != nil {
			log.Printf("failed to delete argo app %s: %v", name, err)
		}
	}

	// Commit and push the manifest removals.
	if len(expired) > 0 {
		if err := c.gitPush(ctx, expired); err != nil {
			log.Printf("failed to push cleanup changes: %v", err)
		}
	}

	return nil
}

// isExpired checks whether an ArgoCD Application has exceeded its TTL or the hard maxAge.
func (c *Controller) isExpired(app unstructured.Unstructured) bool {
	labels := app.GetLabels()

	createdAtStr, ok := labels["ephemeral.io/created-at"]
	if !ok {
		return false
	}

	createdAt, err := parseCreatedAt(createdAtStr)
	if err != nil {
		log.Printf("invalid created-at label on %s: %v", app.GetName(), err)
		return false
	}

	// Hard max age check.
	if time.Since(createdAt) > c.maxAge {
		log.Printf("%s exceeded max age (%s)", app.GetName(), c.maxAge)
		return true
	}

	// TTL check.
	ttlStr, ok := labels["ephemeral.io/ttl"]
	if !ok {
		return false
	}

	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		log.Printf("invalid ttl label on %s: %v", app.GetName(), err)
		return false
	}

	return time.Now().UTC().After(createdAt.Add(ttl))
}

// parseCreatedAt accepts the compact ISO 8601 format used in labels
// (20060102T150405Z — chosen because Kubernetes label values can't contain ':')
// and falls back to RFC3339 for older values.
func parseCreatedAt(s string) (time.Time, error) {
	if t, err := time.Parse("20060102T150405Z", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// removeManifest deletes the environment manifest file from the local repo clone.
func (c *Controller) removeManifest(tenant, envName string) error {
	path := filepath.Join(c.repoPath, "manifests", "environments", tenant, fmt.Sprintf("%s.yaml", envName))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// gitPush commits the removed manifests and pushes to the remote.
func (c *Controller) gitPush(ctx context.Context, removed []unstructured.Unstructured) error {
	var names []string
	for _, app := range removed {
		names = append(names, app.GetName())
	}

	msg := fmt.Sprintf("cleanup: remove expired environments [%s]", strings.Join(names, ", "))

	cmds := [][]string{
		{"git", "-C", c.repoPath, "add", "-A"},
		{"git", "-C", c.repoPath, "commit", "-m", msg},
		{"git", "-C", c.repoPath, "push"},
	}

	for _, args := range cmds {
		cmd := execCommand(ctx, args[0], args[1:]...)
		cmd.Dir = c.repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("running %v: %s: %w", args, string(out), err)
		}
	}

	log.Printf("pushed cleanup commit: %s", msg)
	return nil
}
