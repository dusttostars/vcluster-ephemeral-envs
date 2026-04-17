package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/dusttostars/vcluster-ephemeral-envs/internal/render"
)

var (
	eeGVR = schema.GroupVersionResource{
		Group:    "ephemeral.io",
		Version:  "v1alpha1",
		Resource: "ephemeralenvironments",
	}
	argoAppGVR = schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}
	namespaceGVR    = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	resourceQuotaGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "resourcequotas"}
	secretGVR       = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	serviceGVR      = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
)

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
	Name     string            `json:"name"`
	Tenant   string            `json:"tenant"`
	Branch   string            `json:"branch"`
	TTL      string            `json:"ttl"`
	Image    string            `json:"image"`
	Replicas int32             `json:"replicas"`
	Port     int32             `json:"port"`
	Env      map[string]string `json:"env"`
}

func (h *handler) listEnvironments(w http.ResponseWriter, r *http.Request) {
	envs, err := h.client.Resource(eeGVR).Namespace("").List(r.Context(), metav1.ListOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("listing environments: %v", err), http.StatusInternalServerError)
		return
	}

	out := make([]envResponse, 0, len(envs.Items))
	for _, env := range envs.Items {
		spec, _, _ := unstructured.NestedMap(env.Object, "spec")
		tenant, _ := spec["tenant"].(string)
		branch, _ := spec["branch"].(string)
		ttlStr, _ := spec["ttl"].(string)
		phase, _, _ := unstructured.NestedString(env.Object, "status", "phase")
		if phase == "" {
			phase = "Pending"
		}

		created := env.GetCreationTimestamp()
		age := ""
		if !created.IsZero() {
			age = humanDuration(time.Since(created.Time))
			if ttl, err := time.ParseDuration(ttlStr); err == nil {
				remaining := time.Until(created.Add(ttl))
				if remaining <= 0 && phase == "Running" {
					phase = "Expired"
				} else if remaining < 30*time.Minute && phase == "Running" {
					phase = "Expiring"
				}
			}
		}

		out = append(out, envResponse{
			Name:      env.GetName(),
			Tenant:    tenant,
			Branch:    branch,
			TTL:       ttlStr,
			Status:    phase,
			CreatedAt: created.UTC().Format(time.RFC3339),
			Age:       age,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
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
	if _, err := time.ParseDuration(req.TTL); err != nil {
		http.Error(w, fmt.Sprintf("invalid TTL: %v", err), http.StatusBadRequest)
		return
	}
	if req.Image == "" {
		req.Image = "nginx:alpine"
	}
	if req.Replicas == 0 {
		req.Replicas = 1
	}
	if req.Port == 0 {
		req.Port = 80
	}

	raw, err := render.Template("cr.yaml.tmpl", render.Params{
		Name:     req.Name,
		Tenant:   req.Tenant,
		Branch:   req.Branch,
		TTL:      req.TTL,
		Image:    req.Image,
		Replicas: req.Replicas,
		Port:     req.Port,
		Env:      req.Env,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("rendering CR: %v", err), http.StatusInternalServerError)
		return
	}

	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(raw, obj); err != nil {
		http.Error(w, fmt.Sprintf("parsing rendered CR: %v", err), http.StatusInternalServerError)
		return
	}

	ns := fmt.Sprintf("tenant-%s", req.Tenant)
	_, err = h.client.Resource(eeGVR).Namespace(ns).Create(r.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("creating EphemeralEnvironment: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("Environment %s created for tenant %s", req.Name, req.Tenant),
	})
}

func (h *handler) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")
	if tenant == "" || name == "" {
		http.Error(w, "tenant and name are required", http.StatusBadRequest)
		return
	}

	ns := fmt.Sprintf("tenant-%s", tenant)
	err := h.client.Resource(eeGVR).Namespace(ns).Delete(r.Context(), name, metav1.DeleteOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("deleting EphemeralEnvironment: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("Environment %s deleted from tenant %s", name, tenant),
	})
}

func (h *handler) listTenants(w http.ResponseWriter, r *http.Request) {
	namespaces, err := h.client.Resource(namespaceGVR).List(r.Context(), metav1.ListOptions{
		LabelSelector: "ephemeral.io/tenant",
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("listing namespaces: %v", err), http.StatusInternalServerError)
		return
	}

	tenants := make([]string, 0, len(namespaces.Items))
	for _, ns := range namespaces.Items {
		if tenant := ns.GetLabels()["ephemeral.io/tenant"]; tenant != "" {
			tenants = append(tenants, tenant)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tenants)
}

type tenantDetail struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	CPU       string `json:"cpu"`
	Memory    string `json:"memory"`
	Pods      string `json:"pods"`
	EnvCount  int    `json:"envCount"`
}

func (h *handler) listTenantsDetailed(w http.ResponseWriter, r *http.Request) {
	namespaces, err := h.client.Resource(namespaceGVR).List(r.Context(), metav1.ListOptions{
		LabelSelector: "ephemeral.io/tenant",
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("listing namespaces: %v", err), http.StatusInternalServerError)
		return
	}

	// Count envs per tenant in one list call.
	allEnvs, _ := h.client.Resource(eeGVR).Namespace("").List(r.Context(), metav1.ListOptions{})
	envCounts := map[string]int{}
	if allEnvs != nil {
		for _, env := range allEnvs.Items {
			spec, _, _ := unstructured.NestedMap(env.Object, "spec")
			if tenant, ok := spec["tenant"].(string); ok {
				envCounts[tenant]++
			}
		}
	}

	details := make([]tenantDetail, 0, len(namespaces.Items))
	for _, ns := range namespaces.Items {
		tenant := ns.GetLabels()["ephemeral.io/tenant"]
		if tenant == "" {
			continue
		}
		td := tenantDetail{
			Name:      tenant,
			Namespace: ns.GetName(),
			EnvCount:  envCounts[tenant],
		}
		if quotas, err := h.client.Resource(resourceQuotaGVR).Namespace(ns.GetName()).List(r.Context(), metav1.ListOptions{}); err == nil {
			for _, q := range quotas.Items {
				hard, _, _ := unstructured.NestedMap(q.Object, "spec", "hard")
				if v, ok := hard["limits.cpu"].(string); ok {
					td.CPU = v
				}
				if v, ok := hard["limits.memory"].(string); ok {
					td.Memory = v
				}
				if v, ok := hard["pods"].(string); ok {
					td.Pods = v
				}
			}
		}
		details = append(details, td)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(details)
}

type settingsResponse struct {
	GitHubRepo string `json:"githubRepo"`
	Registry   string `json:"registry"`
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settingsResponse{
		GitHubRepo: h.githubRepo,
		Registry:   "localhost:32000",
	})
}

// createTenant is not yet reimplemented on the K8s-native flow. The dashboard
// previously wrote manifests and git-pushed them; next step is to render a
// tenant chart (namespace + quota + RBAC + ArgoCD AppProject) and apply it
// via the dynamic client. Until then, use the CLI (`ephemeral tenant create`).
func (h *handler) createTenant(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "createTenant not implemented via dashboard yet; use `ephemeral tenant create`", http.StatusNotImplemented)
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

// Suppress unused warnings for GVRs the dashboard still references
// from proxy/deploy/helm code paths.
var _ = []schema.GroupVersionResource{argoAppGVR, secretGVR, serviceGVR}

// Keep strings import from getting nuked if refactor drops uses above.
var _ = strings.TrimPrefix
