package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// proxyApp proxies HTTP requests to services running inside a vcluster.
// Route: /app/{tenant}/{name}/{rest...}
// Proxies to http://<service>.<namespace>.svc.cluster.local inside the vcluster.
func (h *handler) proxyApp(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")
	rest := r.PathValue("rest")

	if rest == "" {
		rest = "/"
	} else if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}

	// The syncer maps vcluster pods/services to the host namespace.
	// The app service is synced as: app-<name>-x-default-x-vcluster-<name>
	// in namespace tenant-<tenant>.
	// But we can also connect directly via the vcluster's kubeconfig.

	// Get the vcluster's ClusterIP to proxy through.
	ns := fmt.Sprintf("tenant-%s", tenant)
	svcName := fmt.Sprintf("vcluster-%s", name)

	svc, err := h.client.Resource(serviceGVR).Namespace(ns).Get(r.Context(), svcName, metav1.GetOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("vcluster service not found: %v", err), http.StatusNotFound)
		return
	}

	clusterIP := ""
	if spec, ok := svc.Object["spec"].(map[string]interface{}); ok {
		clusterIP, _ = spec["clusterIP"].(string)
	}
	if clusterIP == "" {
		http.Error(w, "could not determine vcluster ClusterIP", http.StatusInternalServerError)
		return
	}

	// Find a synced app service in the host namespace. vcluster's syncer
	// annotates every synced object with vcluster.loft.sh/object-name and
	// vcluster.loft.sh/object-namespace (preserving the original identity
	// even when the host name gets truncated+hashed to fit 63 chars), and
	// labels it with vcluster.loft.sh/managed-by=vcluster-<env>. We match
	// on those rather than string-munging the host-side name.
	managedBy := "vcluster-" + name
	svcs, err := h.client.Resource(serviceGVR).Namespace(ns).List(r.Context(), metav1.ListOptions{
		LabelSelector: "vcluster.loft.sh/managed-by=" + managedBy,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("listing services: %v", err), http.StatusInternalServerError)
		return
	}

	targetApp := r.URL.Query().Get("svc")

	var syncedSvc *unstructured.Unstructured
	for _, s := range svcs.Items {
		ann := s.GetAnnotations()
		origName := ann["vcluster.loft.sh/object-name"]
		origNS := ann["vcluster.loft.sh/object-namespace"]
		if origName == "" || origNS != "default" {
			continue
		}
		if strings.Contains(origName, "kube-dns") || strings.HasSuffix(origName, "-headless") {
			continue
		}
		if targetApp != "" && origName != targetApp {
			continue
		}
		if spec, ok := s.Object["spec"].(map[string]interface{}); ok {
			if ports, ok := spec["ports"].([]interface{}); ok && len(ports) > 0 {
				if p, ok := ports[0].(map[string]interface{}); ok {
					port := fmt.Sprintf("%v", p["port"])
					if port != "80" && port != "8080" && port != "3000" && port != "8090" && targetApp == "" {
						continue
					}
				}
			}
		}
		s := s
		syncedSvc = &s
		break
	}

	if syncedSvc == nil {
		http.Error(w, fmt.Sprintf("no app service found in %s for vcluster %s", ns, name), http.StatusNotFound)
		return
	}

	// Get the synced service's ClusterIP.
	appClusterIP := ""
	appPort := "80"
	if spec, ok := syncedSvc.Object["spec"].(map[string]interface{}); ok {
		appClusterIP, _ = spec["clusterIP"].(string)
		if ports, ok := spec["ports"].([]interface{}); ok && len(ports) > 0 {
			if p, ok := ports[0].(map[string]interface{}); ok {
				if port, ok := p["port"]; ok {
					appPort = fmt.Sprintf("%v", port)
				}
			}
		}
	}

	if appClusterIP == "" {
		http.Error(w, "could not determine app service ClusterIP", http.StatusInternalServerError)
		return
	}

	// Proxy the request to the app service.
	targetURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%s", appClusterIP, appPort),
		Path:   rest,
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("creating proxy request: %v", err), http.StatusInternalServerError)
		return
	}

	// Copy headers.
	for key, vals := range r.Header {
		for _, val := range vals {
			proxyReq.Header.Add(key, val)
		}
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("proxying request: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers.
	for key, vals := range resp.Header {
		for _, val := range vals {
			w.Header().Add(key, val)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// listDeployedApps returns the deployed app releases for a vcluster environment.
func (h *handler) listDeployedApps(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	name := r.PathValue("name")
	ns := fmt.Sprintf("tenant-%s", tenant)

	// Find synced services that match the app pattern.
	svcs, err := h.client.Resource(serviceGVR).Namespace(ns).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		http.Error(w, fmt.Sprintf("listing services: %v", err), http.StatusInternalServerError)
		return
	}

	type appInfo struct {
		Name      string `json:"name"`
		ProxyURL  string `json:"proxyUrl"`
		ClusterIP string `json:"clusterIP"`
		Port      string `json:"port"`
	}

	var apps []appInfo
	vclusterSuffix := fmt.Sprintf("-x-default-x-vcluster-%s", name)
	for _, s := range svcs.Items {
		sName := s.GetName()
		if strings.Contains(sName, vclusterSuffix) && !strings.Contains(sName, "kube-dns") {
			clusterIP := ""
			port := "80"
			if spec, ok := s.Object["spec"].(map[string]interface{}); ok {
				clusterIP, _ = spec["clusterIP"].(string)
				if ports, ok := spec["ports"].([]interface{}); ok && len(ports) > 0 {
					if p, ok := ports[0].(map[string]interface{}); ok {
						if pp, ok := p["port"]; ok {
							port = fmt.Sprintf("%v", pp)
						}
					}
				}
			}

			// Extract the app name from the synced service name.
			appName := strings.TrimSuffix(sName, vclusterSuffix)
			apps = append(apps, appInfo{
				Name:      appName,
				ProxyURL:  fmt.Sprintf("/app/%s/%s/?svc=%s", tenant, name, appName),
				ClusterIP: clusterIP,
				Port:      port,
			})
		}
	}

	if apps == nil {
		apps = []appInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apps)
}
