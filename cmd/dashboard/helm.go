package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type installChartRequest struct {
	RepoURL     string `json:"repoURL"`
	ChartName   string `json:"chartName"`
	Version     string `json:"version"`
	ReleaseName string `json:"releaseName"`
	Namespace   string `json:"namespace"`
	Values      string `json:"values"`
}

type helmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Chart     string `json:"chart"`
	Version   string `json:"version"`
	Status    string `json:"status"`
	Updated   string `json:"updated"`
}

type chartCatalogEntry struct {
	Name        string `json:"name"`
	RepoURL     string `json:"repoURL"`
	ChartName   string `json:"chartName"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

var defaultCatalog = []chartCatalogEntry{
	{Name: "NGINX Ingress", RepoURL: "https://kubernetes.github.io/ingress-nginx", ChartName: "ingress-nginx", Description: "Ingress controller for Kubernetes"},
	{Name: "PostgreSQL", RepoURL: "https://charts.bitnami.com/bitnami", ChartName: "postgresql", Description: "PostgreSQL database"},
	{Name: "Redis", RepoURL: "https://charts.bitnami.com/bitnami", ChartName: "redis", Description: "Redis in-memory data store"},
	{Name: "MySQL", RepoURL: "https://charts.bitnami.com/bitnami", ChartName: "mysql", Description: "MySQL database"},
	{Name: "MongoDB", RepoURL: "https://charts.bitnami.com/bitnami", ChartName: "mongodb", Description: "MongoDB document database"},
	{Name: "RabbitMQ", RepoURL: "https://charts.bitnami.com/bitnami", ChartName: "rabbitmq", Description: "RabbitMQ message broker"},
	{Name: "Prometheus", RepoURL: "https://prometheus-community.github.io/helm-charts", ChartName: "prometheus", Description: "Monitoring and alerting"},
	{Name: "Grafana", RepoURL: "https://grafana.github.io/helm-charts", ChartName: "grafana", Description: "Observability dashboards"},
}

// getVClusterKubeconfig extracts the vcluster kubeconfig from the vc-{name}
// secret and writes it to a temp file. Returns the path and a cleanup function.
func (h *handler) getVClusterKubeconfig(ctx context.Context, tenant, name string) (string, func(), error) {
	ns := fmt.Sprintf("tenant-%s", tenant)
	secretName := fmt.Sprintf("vc-vcluster-%s", name)

	secret, err := h.client.Resource(secretGVR).Namespace(ns).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("getting secret %s/%s: %w", ns, secretName, err)
	}

	data, ok := secret.Object["data"].(map[string]interface{})
	if !ok {
		return "", nil, fmt.Errorf("secret %s has no data field", secretName)
	}

	configB64, ok := data["config"].(string)
	if !ok {
		return "", nil, fmt.Errorf("secret %s has no config key", secretName)
	}

	configBytes, err := base64.StdEncoding.DecodeString(configB64)
	if err != nil {
		return "", nil, fmt.Errorf("decoding kubeconfig: %w", err)
	}

	// The kubeconfig references the vcluster via internal DNS (e.g.
	// vcluster-demo.tenant-team-a.svc.cluster.local) which may not
	// resolve from the dashboard host. Replace with the ClusterIP.
	svcName := fmt.Sprintf("vcluster-%s", name)
	svc, err := h.client.Resource(serviceGVR).Namespace(ns).Get(ctx, svcName, metav1.GetOptions{})
	if err == nil {
		if spec, ok := svc.Object["spec"].(map[string]interface{}); ok {
			if clusterIP, ok := spec["clusterIP"].(string); ok && clusterIP != "" {
				internalDNS := fmt.Sprintf("%s.%s.svc.cluster.local", svcName, ns)
				configBytes = []byte(strings.Replace(string(configBytes), internalDNS, clusterIP, 1))
			}
		}
	}

	tmp, err := os.CreateTemp("", "vc-kubeconfig-*.yaml")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp file: %w", err)
	}

	if _, err := tmp.Write(configBytes); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("writing kubeconfig: %w", err)
	}
	tmp.Close()

	cleanup := func() { os.Remove(tmp.Name()) }
	return tmp.Name(), cleanup, nil
}

func (h *handler) chartCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(defaultCatalog)
}

func (h *handler) listCharts(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	kubeconfig, cleanup, err := h.getVClusterKubeconfig(r.Context(), tenant, name)
	if err != nil {
		http.Error(w, fmt.Sprintf("getting kubeconfig: %v", err), http.StatusInternalServerError)
		return
	}
	defer cleanup()

	cmd := exec.CommandContext(r.Context(), "helm", "list",
		"--kubeconfig", kubeconfig,
		"--all-namespaces",
		"--output", "json",
	)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		http.Error(w, fmt.Sprintf("helm list failed: %v: %s", err, stderr), http.StatusInternalServerError)
		return
	}

	// helm list --output json returns [] when empty or a JSON array of releases.
	var releases []helmRelease
	if err := json.Unmarshal(out, &releases); err != nil {
		// If it's not valid JSON, return empty list.
		releases = []helmRelease{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(releases)
}

func (h *handler) installChart(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")

	var req installChartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.RepoURL == "" || req.ChartName == "" || req.ReleaseName == "" {
		http.Error(w, "repoURL, chartName, and releaseName are required", http.StatusBadRequest)
		return
	}

	if req.Namespace == "" {
		req.Namespace = "default"
	}

	kubeconfig, cleanup, err := h.getVClusterKubeconfig(r.Context(), tenant, name)
	if err != nil {
		http.Error(w, fmt.Sprintf("getting kubeconfig: %v", err), http.StatusInternalServerError)
		return
	}
	defer cleanup()

	args := []string{
		"install", req.ReleaseName, req.ChartName,
		"--repo", req.RepoURL,
		"--kubeconfig", kubeconfig,
		"--namespace", req.Namespace,
		"--create-namespace",
	}

	if req.Version != "" {
		args = append(args, "--version", req.Version)
	}

	// Write values to a temp file if provided.
	if strings.TrimSpace(req.Values) != "" {
		valuesFile, err := os.CreateTemp("", "helm-values-*.yaml")
		if err != nil {
			http.Error(w, fmt.Sprintf("creating values file: %v", err), http.StatusInternalServerError)
			return
		}
		defer os.Remove(valuesFile.Name())

		if _, err := valuesFile.WriteString(req.Values); err != nil {
			valuesFile.Close()
			http.Error(w, fmt.Sprintf("writing values: %v", err), http.StatusInternalServerError)
			return
		}
		valuesFile.Close()

		args = append(args, "--values", valuesFile.Name())
	}

	cmd := exec.CommandContext(r.Context(), "helm", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("helm install failed: %s", string(out))
		http.Error(w, fmt.Sprintf("helm install failed: %s", string(out)), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("Chart %s installed as %s in namespace %s", req.ChartName, req.ReleaseName, req.Namespace),
	})
}

func (h *handler) uninstallChart(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")
	release := r.PathValue("release")
	namespace := r.URL.Query().Get("namespace")

	if release == "" {
		http.Error(w, "release name is required", http.StatusBadRequest)
		return
	}

	if namespace == "" {
		namespace = "default"
	}

	kubeconfig, cleanup, err := h.getVClusterKubeconfig(r.Context(), tenant, name)
	if err != nil {
		http.Error(w, fmt.Sprintf("getting kubeconfig: %v", err), http.StatusInternalServerError)
		return
	}
	defer cleanup()

	cmd := exec.CommandContext(r.Context(), "helm", "uninstall", release,
		"--kubeconfig", kubeconfig,
		"--namespace", namespace,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("helm uninstall failed: %s", string(out))
		http.Error(w, fmt.Sprintf("helm uninstall failed: %s", string(out)), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": fmt.Sprintf("Release %s uninstalled", release),
	})
}
