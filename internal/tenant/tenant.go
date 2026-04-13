package tenant

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Tenant represents a team that owns ephemeral environments.
type Tenant struct {
	Name       string `json:"name"`
	MaxCPU     string `json:"maxCPU"`
	MaxMemory  string `json:"maxMemory"`
	MaxEnvs    int    `json:"maxEnvs"`
	MaxAgeSecs int    `json:"maxAgeSecs"`
}

// DefaultTenant returns a tenant with sensible defaults.
func DefaultTenant(name string) *Tenant {
	return &Tenant{
		Name:       name,
		MaxCPU:     "4",
		MaxMemory:  "8Gi",
		MaxEnvs:    5,
		MaxAgeSecs: 86400, // 24h
	}
}

// Namespace returns the Kubernetes namespace for this tenant.
func (t *Tenant) Namespace() string {
	return fmt.Sprintf("tenant-%s", t.Name)
}

// GenerateNamespace creates the Namespace manifest.
func (t *Tenant) GenerateNamespace() *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Namespace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: t.Namespace(),
			Labels: map[string]string{
				"ephemeral.io/tenant":     t.Name,
				"ephemeral.io/managed-by": "ephemeral-controller",
			},
		},
	}
}

// GenerateResourceQuota creates resource limits for the tenant namespace.
func (t *Tenant) GenerateResourceQuota() *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ResourceQuota",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-quota", t.Name),
			Namespace: t.Namespace(),
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourceRequestsCPU:    resource.MustParse(t.MaxCPU),
				corev1.ResourceRequestsMemory: resource.MustParse(t.MaxMemory),
				corev1.ResourceLimitsCPU:      resource.MustParse(t.MaxCPU),
				corev1.ResourceLimitsMemory:   resource.MustParse(t.MaxMemory),
				corev1.ResourcePods:           resource.MustParse(fmt.Sprintf("%d", t.MaxEnvs*10)),
			},
		},
	}
}

// GenerateRBAC creates a Role and RoleBinding scoping the tenant to their namespace.
func (t *Tenant) GenerateRBAC() (*rbacv1.Role, *rbacv1.RoleBinding) {
	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-tenant-role", t.Name),
			Namespace: t.Namespace(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "services", "configmaps", "secrets", "persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments", "statefulsets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"ingresses"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "delete"},
			},
		},
	}

	binding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-tenant-binding", t.Name),
			Namespace: t.Namespace(),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "Group",
				Name:     fmt.Sprintf("tenant:%s", t.Name),
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     fmt.Sprintf("%s-tenant-role", t.Name),
		},
	}

	return role, binding
}

// WriteManifests writes all tenant manifests (namespace, quota, rbac) to the repo.
func (t *Tenant) WriteManifests(baseDir string) error {
	dir := filepath.Join(baseDir, "manifests", "tenants", t.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating tenant dir: %w", err)
	}

	ns := t.GenerateNamespace()
	quota := t.GenerateResourceQuota()
	role, binding := t.GenerateRBAC()

	manifests := map[string]interface{}{
		"namespace.yaml":    ns,
		"resourcequota.yaml": quota,
		"role.yaml":         role,
		"rolebinding.yaml":  binding,
	}

	for filename, obj := range manifests {
		data, err := yaml.Marshal(obj)
		if err != nil {
			return fmt.Errorf("marshaling %s: %w", filename, err)
		}
		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, data, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", filename, err)
		}
		log.Printf("wrote %s", path)
	}

	return nil
}

// GenerateArgoProject creates an ArgoCD AppProject that restricts the tenant
// to only deploy into their own namespace.
func (t *Tenant) GenerateArgoProject(repoURL string) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "AppProject",
		"metadata": map[string]interface{}{
			"name":      fmt.Sprintf("tenant-%s", t.Name),
			"namespace": "argocd",
		},
		"spec": map[string]interface{}{
			"description": fmt.Sprintf("Ephemeral environments for tenant %s", t.Name),
			"sourceRepos": []string{
				repoURL,
				"https://charts.loft.sh",
			},
			"destinations": []map[string]string{
				{
					"server":    "https://kubernetes.default.svc",
					"namespace": t.Namespace(),
				},
			},
			"clusterResourceWhitelist": []map[string]string{},
			"namespaceResourceWhitelist": []map[string]string{
				{"group": "", "kind": "Service"},
				{"group": "", "kind": "Pod"},
				{"group": "", "kind": "ConfigMap"},
				{"group": "", "kind": "Secret"},
				{"group": "apps", "kind": "Deployment"},
				{"group": "apps", "kind": "StatefulSet"},
			},
		},
	}
}

// WriteArgoProject writes the ArgoCD AppProject manifest.
func (t *Tenant) WriteArgoProject(baseDir, repoURL string) error {
	project := t.GenerateArgoProject(repoURL)

	data, err := yaml.Marshal(project)
	if err != nil {
		return fmt.Errorf("marshaling argo project: %w", err)
	}

	dir := filepath.Join(baseDir, "argocd", "projects")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating projects dir: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%s.yaml", t.Name))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing argo project: %w", err)
	}

	log.Printf("wrote ArgoCD project: %s", path)
	return nil
}
