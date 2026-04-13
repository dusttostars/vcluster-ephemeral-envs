package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/dusttostars/vcluster-ephemeral-envs/internal/vcluster"
)

var argoAppGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

type handler struct {
	client   dynamic.Interface
	repoPath string
}

type envResponse struct {
	Name      string `json:"name"`
	Tenant    string `json:"tenant"`
	Branch    string `json:"branch"`
	TTL       string `json:"ttl"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	Age       string `json:"age"`
}

type createRequest struct {
	Name   string `json:"name"`
	Tenant string `json:"tenant"`
	Branch string `json:"branch"`
	TTL    string `json:"ttl"`
}

func (h *handler) listEnvironments(w http.ResponseWriter, r *http.Request) {
	apps, err := h.client.Resource(argoAppGVR).Namespace("argocd").List(r.Context(), metav1.ListOptions{
		LabelSelector: "ephemeral.io/managed-by=ephemeral-controller",
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("listing applications: %v", err), http.StatusInternalServerError)
		return
	}

	var envs []envResponse
	for _, app := range apps.Items {
		labels := app.GetLabels()

		createdAtStr := labels["ephemeral.io/created-at"]
		age := ""
		status := "Unknown"

		if createdAtStr != "" {
			if t, err := parseCreatedAt(createdAtStr); err == nil {
				age = humanDuration(time.Since(t))

				ttlStr := labels["ephemeral.io/ttl"]
				if ttl, err := time.ParseDuration(ttlStr); err == nil {
					remaining := time.Until(t.Add(ttl))
					if remaining <= 0 {
						status = "Expired"
					} else if remaining < 30*time.Minute {
						status = "Expiring"
					} else {
						status = "Running"
					}
				}
			}
		}

		// Check ArgoCD sync status.
		syncStatus, _, _ := nestedString(app.Object, "status", "sync", "status")
		healthStatus, _, _ := nestedString(app.Object, "status", "health", "status")
		if healthStatus == "Degraded" || syncStatus == "OutOfSync" {
			if status == "Running" {
				status = "Syncing"
			}
		}

		name := app.GetName()
		name = strings.TrimPrefix(name, "vcluster-")

		envs = append(envs, envResponse{
			Name:      name,
			Tenant:    labels["ephemeral.io/tenant"],
			Branch:    labels["ephemeral.io/branch"],
			TTL:       labels["ephemeral.io/ttl"],
			Status:    status,
			CreatedAt: createdAtStr,
			Age:       age,
		})
	}

	if envs == nil {
		envs = []envResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(envs)
}

func (h *handler) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Tenant == "" || req.Branch == "" {
		http.Error(w, "name, tenant, and branch are required", http.StatusBadRequest)
		return
	}

	if req.TTL == "" {
		req.TTL = "2h"
	}

	ttl, err := time.ParseDuration(req.TTL)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid TTL: %v", err), http.StatusBadRequest)
		return
	}

	env := vcluster.NewEnvironment(req.Name, req.Tenant, req.Branch, ttl)

	path, err := env.WriteManifest(h.repoPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("writing manifest: %v", err), http.StatusInternalServerError)
		return
	}

	// Git add, commit, push.
	if err := gitCommitPush(r.Context(), h.repoPath, path,
		fmt.Sprintf("env: create %s for tenant %s (ttl=%s)", req.Name, req.Tenant, req.TTL),
	); err != nil {
		log.Printf("git push failed (manifest written): %v", err)
	}

	// Apply the ArgoCD Application directly so it appears immediately
	// without waiting for ArgoCD to poll git.
	app := env.GenerateArgoApp()
	unstructured := toUnstructured(app)
	if unstructured != nil {
		_, err := h.client.Resource(argoAppGVR).Namespace("argocd").Create(
			r.Context(), unstructured, metav1.CreateOptions{},
		)
		if err != nil {
			log.Printf("direct apply failed (will rely on ArgoCD sync): %v", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("Environment %s created for tenant %s", req.Name, req.Tenant),
		"path":    path,
	})
}

func (h *handler) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	if tenant == "" || name == "" {
		http.Error(w, "tenant and name are required", http.StatusBadRequest)
		return
	}

	manifestPath := filepath.Join(h.repoPath, "manifests", "environments", tenant, fmt.Sprintf("%s.yaml", name))
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		http.Error(w, "environment not found", http.StatusNotFound)
		return
	}

	if err := os.Remove(manifestPath); err != nil {
		http.Error(w, fmt.Sprintf("removing manifest: %v", err), http.StatusInternalServerError)
		return
	}

	if err := gitCommitPush(r.Context(), h.repoPath, manifestPath,
		fmt.Sprintf("env: delete %s for tenant %s", name, tenant),
	); err != nil {
		log.Printf("git push failed (manifest removed): %v", err)
	}

	// Also delete the ArgoCD Application directly for faster cleanup.
	argoName := fmt.Sprintf("vcluster-%s", name)
	if err := h.client.Resource(argoAppGVR).Namespace("argocd").Delete(
		r.Context(), argoName, metav1.DeleteOptions{},
	); err != nil {
		log.Printf("failed to delete argo app %s: %v", argoName, err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("Environment %s deleted from tenant %s", name, tenant),
	})
}

func (h *handler) listTenants(w http.ResponseWriter, r *http.Request) {
	tenantsDir := filepath.Join(h.repoPath, "manifests", "tenants")
	entries, err := os.ReadDir(tenantsDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("reading tenants dir: %v", err), http.StatusInternalServerError)
		return
	}

	var tenants []string
	for _, e := range entries {
		if e.IsDir() {
			tenants = append(tenants, e.Name())
		}
	}

	if tenants == nil {
		tenants = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tenants)
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh %dm ago", h, m)
		}
		return fmt.Sprintf("%dh ago", h)
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func parseCreatedAt(s string) (time.Time, error) {
	// Try RFC3339 first (e.g. 2026-04-10T21:00:00Z).
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// K8s labels can't contain colons, so the controller may store a compact format.
	if t, err := time.Parse("20060102T150405Z", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %s", s)
}

func toUnstructured(app *vcluster.HelmRelease) *unstructured.Unstructured {
	data, err := json.Marshal(app)
	if err != nil {
		return nil
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	return &unstructured.Unstructured{Object: obj}
}

func nestedString(obj map[string]interface{}, fields ...string) (string, bool, error) {
	current := obj
	for i, f := range fields {
		if i == len(fields)-1 {
			val, ok := current[f]
			if !ok {
				return "", false, nil
			}
			s, ok := val.(string)
			return s, ok, nil
		}
		next, ok := current[f]
		if !ok {
			return "", false, nil
		}
		current, ok = next.(map[string]interface{})
		if !ok {
			return "", false, nil
		}
	}
	return "", false, nil
}
