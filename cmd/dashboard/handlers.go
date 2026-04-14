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

var secretGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "secrets",
}

var serviceGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "services",
}

type handler struct {
	client      dynamic.Interface
	repoPath    string
	githubToken string
	githubRepo  string
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

type tenantDetail struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	CPU        string `json:"cpu"`
	Memory     string `json:"memory"`
	Pods       string `json:"pods"`
	EnvCount   int    `json:"envCount"`
}

func (h *handler) listTenantsDetailed(w http.ResponseWriter, r *http.Request) {
	tenantsDir := filepath.Join(h.repoPath, "manifests", "tenants")
	entries, err := os.ReadDir(tenantsDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("reading tenants dir: %v", err), http.StatusInternalServerError)
		return
	}

	// Count environments per tenant.
	envCounts := map[string]int{}
	envsDir := filepath.Join(h.repoPath, "manifests", "environments")
	if envEntries, err := os.ReadDir(envsDir); err == nil {
		for _, e := range envEntries {
			if e.IsDir() {
				files, _ := os.ReadDir(filepath.Join(envsDir, e.Name()))
				count := 0
				for _, f := range files {
					if strings.HasSuffix(f.Name(), ".yaml") {
						count++
					}
				}
				envCounts[e.Name()] = count
			}
		}
	}

	var details []tenantDetail
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		td := tenantDetail{
			Name:      name,
			Namespace: fmt.Sprintf("tenant-%s", name),
			EnvCount:  envCounts[name],
		}

		// Read resource quota if available.
		quotaPath := filepath.Join(tenantsDir, name, "resourcequota.yaml")
		if data, err := os.ReadFile(quotaPath); err == nil {
			content := string(data)
			for _, line := range strings.Split(content, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "limits.cpu:") {
					td.CPU = strings.Trim(strings.TrimPrefix(line, "limits.cpu:"), " \"")
				} else if strings.HasPrefix(line, "limits.memory:") {
					td.Memory = strings.Trim(strings.TrimPrefix(line, "limits.memory:"), " \"")
				} else if strings.HasPrefix(line, "pods:") {
					td.Pods = strings.Trim(strings.TrimPrefix(line, "pods:"), " \"")
				}
			}
		}

		details = append(details, td)
	}

	if details == nil {
		details = []tenantDetail{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(details)
}

type settingsResponse struct {
	GitHubRepo string `json:"githubRepo"`
	Registry   string `json:"registry"`
	RepoPath   string `json:"repoPath"`
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settingsResponse{
		GitHubRepo: h.githubRepo,
		Registry:   "localhost:32000",
		RepoPath:   h.repoPath,
	})
}

type createTenantRequest struct {
	Name   string `json:"name"`
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

func (h *handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if req.CPU == "" {
		req.CPU = "4"
	}
	if req.Memory == "" {
		req.Memory = "8Gi"
	}

	namespace := fmt.Sprintf("tenant-%s", req.Name)
	targetDir := filepath.Join(h.repoPath, "manifests", "tenants", req.Name)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		http.Error(w, fmt.Sprintf("creating tenant dir: %v", err), http.StatusInternalServerError)
		return
	}

	// Namespace
	nsYAML := fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    ephemeral.io/tenant: "%s"
    ephemeral.io/managed-by: "ephemeral-controller"
`, namespace, req.Name)

	// ResourceQuota
	quotaYAML := fmt.Sprintf(`apiVersion: v1
kind: ResourceQuota
metadata:
  name: %s-quota
  namespace: %s
spec:
  hard:
    requests.cpu: "%s"
    requests.memory: "%s"
    limits.cpu: "%s"
    limits.memory: "%s"
    pods: "50"
`, req.Name, namespace, req.CPU, req.Memory, req.CPU, req.Memory)

	// Role
	roleYAML := fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %s-tenant-role
  namespace: %s
rules:
  - apiGroups: [""]
    resources: ["pods", "services", "configmaps", "secrets", "persistentvolumeclaims"]
    verbs: ["get", "list", "watch", "create", "update", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets"]
    verbs: ["get", "list", "watch", "create", "update", "delete"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get", "list", "watch", "create", "update", "delete"]
`, req.Name, namespace)

	// RoleBinding
	rbYAML := fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %s-tenant-binding
  namespace: %s
subjects:
  - kind: Group
    name: "tenant:%s"
    apiGroup: rbac.authorization.k8s.io
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %s-tenant-role
`, req.Name, namespace, req.Name, req.Name)

	files := map[string]string{
		"namespace.yaml":     nsYAML,
		"resourcequota.yaml": quotaYAML,
		"role.yaml":          roleYAML,
		"rolebinding.yaml":   rbYAML,
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(targetDir, name), []byte(content), 0644); err != nil {
			http.Error(w, fmt.Sprintf("writing %s: %v", name, err), http.StatusInternalServerError)
			return
		}
	}

	// ArgoCD AppProject
	projectsDir := filepath.Join(h.repoPath, "argocd", "projects")
	os.MkdirAll(projectsDir, 0755)

	repoURL := "https://github.com/" + h.githubRepo + ".git"
	projectYAML := fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: tenant-%s
  namespace: argocd
spec:
  description: "Ephemeral environments for tenant %s"
  sourceRepos:
    - "%s"
    - "https://charts.loft.sh"
  destinations:
    - server: https://kubernetes.default.svc
      namespace: %s
  clusterResourceWhitelist:
    - group: "rbac.authorization.k8s.io"
      kind: "ClusterRole"
    - group: "rbac.authorization.k8s.io"
      kind: "ClusterRoleBinding"
  namespaceResourceWhitelist:
    - group: ""
      kind: "*"
    - group: "apps"
      kind: "*"
    - group: "rbac.authorization.k8s.io"
      kind: "*"
`, req.Name, req.Name, repoURL, namespace)

	if err := os.WriteFile(filepath.Join(projectsDir, req.Name+".yaml"), []byte(projectYAML), 0644); err != nil {
		http.Error(w, fmt.Sprintf("writing project: %v", err), http.StatusInternalServerError)
		return
	}

	// Environments directory
	envsDir := filepath.Join(h.repoPath, "manifests", "environments", req.Name)
	os.MkdirAll(envsDir, 0755)
	os.WriteFile(filepath.Join(envsDir, ".gitkeep"), []byte{}, 0644)

	// Git commit and push
	if err := gitCommitPush(r.Context(), h.repoPath, targetDir,
		fmt.Sprintf("tenant: create %s (cpu=%s, mem=%s)", req.Name, req.CPU, req.Memory),
	); err != nil {
		log.Printf("git push failed for tenant %s: %v", req.Name, err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("Tenant %s created (namespace: %s)", req.Name, namespace),
	})
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
