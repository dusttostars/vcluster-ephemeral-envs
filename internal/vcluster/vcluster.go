package vcluster

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sigs.k8s.io/yaml"
)

// Environment represents an ephemeral vcluster environment.
type Environment struct {
	Name      string            `json:"name"`
	Tenant    string            `json:"tenant"`
	Branch    string            `json:"branch"`
	TTL       time.Duration     `json:"ttl"`
	CreatedAt time.Time         `json:"createdAt"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// HelmRelease represents a vcluster Helm release manifest for ArgoCD.
type HelmRelease struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   HelmMetadata      `json:"metadata"`
	Spec       HelmReleaseSpec   `json:"spec"`
}

type HelmMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type HelmReleaseSpec struct {
	Project     string          `json:"project"`
	Source      HelmSource      `json:"source"`
	Destination HelmDestination `json:"destination"`
	SyncPolicy  *SyncPolicy    `json:"syncPolicy,omitempty"`
}

type HelmSource struct {
	RepoURL        string `json:"repoURL"`
	Chart          string `json:"chart"`
	TargetRevision string `json:"targetRevision"`
	Helm           *HelmValues `json:"helm,omitempty"`
}

type HelmValues struct {
	Values string `json:"values,omitempty"`
}

type HelmDestination struct {
	Server    string `json:"server"`
	Namespace string `json:"namespace"`
}

type SyncPolicy struct {
	Automated *AutomatedSync `json:"automated,omitempty"`
}

type AutomatedSync struct {
	Prune    bool `json:"prune"`
	SelfHeal bool `json:"selfHeal"`
}

// sanitizeLabel replaces characters invalid in Kubernetes label values
// (e.g. slashes) with dashes.
func sanitizeLabel(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b = append(b, c)
		} else {
			b = append(b, '-')
		}
	}
	return string(b)
}

// NewEnvironment creates a new ephemeral environment definition.
func NewEnvironment(name, tenant, branch string, ttl time.Duration) *Environment {
	return &Environment{
		Name:      name,
		Tenant:    tenant,
		Branch:    branch,
		TTL:       ttl,
		CreatedAt: time.Now().UTC(),
		Labels: map[string]string{
			"ephemeral.io/tenant":     sanitizeLabel(tenant),
			"ephemeral.io/branch":     sanitizeLabel(branch),
			"ephemeral.io/ttl":        ttl.String(),
			"ephemeral.io/created-at": time.Now().UTC().Format("20060102T150405Z"),
			"ephemeral.io/managed-by": "ephemeral-controller",
		},
	}
}

// GenerateArgoApp generates an ArgoCD Application manifest that deploys a vcluster
// via the official Helm chart into the tenant's namespace.
func (e *Environment) GenerateArgoApp() *HelmRelease {
	namespace := fmt.Sprintf("tenant-%s", e.Tenant)

	values := fmt.Sprintf(`syncer:
  extraArgs:
    - --out-kube-config-server=https://vcluster-%s.%s.svc.cluster.local
  resources:
    limits:
      cpu: 500m
      memory: 512Mi
    requests:
      cpu: 100m
      memory: 256Mi
vcluster:
  image: rancher/k3s:v1.29.1-k3s2
  resources:
    limits:
      cpu: 500m
      memory: 512Mi
    requests:
      cpu: 100m
      memory: 128Mi
sync:
  nodes:
    enabled: false
  persistentvolumes:
    enabled: false
  ingresses:
    enabled: true
  services:
    enabled: true
`, e.Name, namespace)

	return &HelmRelease{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Metadata: HelmMetadata{
			Name:      fmt.Sprintf("vcluster-%s", e.Name),
			Namespace: "argocd",
			Labels:    e.Labels,
		},
		Spec: HelmReleaseSpec{
			Project: fmt.Sprintf("tenant-%s", e.Tenant),
			Source: HelmSource{
				RepoURL:        "https://charts.loft.sh",
				Chart:          "vcluster",
				TargetRevision: "0.19.*",
				Helm: &HelmValues{
					Values: values,
				},
			},
			Destination: HelmDestination{
				Server:    "https://kubernetes.default.svc",
				Namespace: namespace,
			},
			SyncPolicy: &SyncPolicy{
				Automated: &AutomatedSync{
					Prune:    true,
					SelfHeal: true,
				},
			},
		},
	}
}

// WriteManifest writes the ArgoCD Application manifest to the environments directory.
func (e *Environment) WriteManifest(baseDir string) (string, error) {
	app := e.GenerateArgoApp()

	data, err := yaml.Marshal(app)
	if err != nil {
		return "", fmt.Errorf("marshaling manifest: %w", err)
	}

	dir := filepath.Join(baseDir, "manifests", "environments", e.Tenant)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%s.yaml", e.Name))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("writing manifest: %w", err)
	}

	return path, nil
}

// RemoveManifest deletes the manifest file for this environment.
func (e *Environment) RemoveManifest(baseDir string) error {
	path := filepath.Join(baseDir, "manifests", "environments", e.Tenant, fmt.Sprintf("%s.yaml", e.Name))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing manifest: %w", err)
	}
	return nil
}

// IsExpired returns true if the environment has exceeded its TTL.
func (e *Environment) IsExpired() bool {
	return time.Now().UTC().After(e.CreatedAt.Add(e.TTL))
}
