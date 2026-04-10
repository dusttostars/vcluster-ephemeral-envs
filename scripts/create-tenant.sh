#!/usr/bin/env bash
set -euo pipefail

TENANT="${1:?Usage: create-tenant.sh <tenant-name> [max-cpu] [max-memory]}"
MAX_CPU="${2:-4}"
MAX_MEMORY="${3:-8Gi}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_URL="${REPO_URL:-https://github.com/willsbctm/vcluster-ephemeral-envs.git}"

NAMESPACE="tenant-${TENANT}"
TARGET_DIR="${REPO_ROOT}/manifests/tenants/${TENANT}"
mkdir -p "$TARGET_DIR"

echo "==> Creating tenant: ${TENANT}"
echo "    Namespace: ${NAMESPACE}"
echo "    CPU Limit: ${MAX_CPU}"
echo "    Mem Limit: ${MAX_MEMORY}"

# Namespace
cat > "${TARGET_DIR}/namespace.yaml" <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: ${NAMESPACE}
  labels:
    ephemeral.io/tenant: "${TENANT}"
    ephemeral.io/managed-by: "ephemeral-controller"
EOF

# ResourceQuota
cat > "${TARGET_DIR}/resourcequota.yaml" <<EOF
apiVersion: v1
kind: ResourceQuota
metadata:
  name: ${TENANT}-quota
  namespace: ${NAMESPACE}
spec:
  hard:
    requests.cpu: "${MAX_CPU}"
    requests.memory: "${MAX_MEMORY}"
    limits.cpu: "${MAX_CPU}"
    limits.memory: "${MAX_MEMORY}"
    pods: "50"
EOF

# Role
cat > "${TARGET_DIR}/role.yaml" <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${TENANT}-tenant-role
  namespace: ${NAMESPACE}
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
EOF

# RoleBinding
cat > "${TARGET_DIR}/rolebinding.yaml" <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${TENANT}-tenant-binding
  namespace: ${NAMESPACE}
subjects:
  - kind: Group
    name: "tenant:${TENANT}"
    apiGroup: rbac.authorization.k8s.io
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ${TENANT}-tenant-role
EOF

# ArgoCD AppProject
PROJECTS_DIR="${REPO_ROOT}/argocd/projects"
mkdir -p "$PROJECTS_DIR"

cat > "${PROJECTS_DIR}/${TENANT}.yaml" <<EOF
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: tenant-${TENANT}
  namespace: argocd
spec:
  description: "Ephemeral environments for tenant ${TENANT}"
  sourceRepos:
    - "${REPO_URL}"
    - "https://charts.loft.sh"
  destinations:
    - server: https://kubernetes.default.svc
      namespace: ${NAMESPACE}
  namespaceResourceWhitelist:
    - group: ""
      kind: "*"
    - group: "apps"
      kind: "*"
EOF

# Environments directory
mkdir -p "${REPO_ROOT}/manifests/environments/${TENANT}"
touch "${REPO_ROOT}/manifests/environments/${TENANT}/.gitkeep"

echo "==> Tenant manifests written to ${TARGET_DIR}"
echo "==> ArgoCD project written to ${PROJECTS_DIR}/${TENANT}.yaml"
echo ""
echo "==> Committing and pushing..."

cd "$REPO_ROOT"
git add "manifests/tenants/${TENANT}" "argocd/projects/${TENANT}.yaml" "manifests/environments/${TENANT}/.gitkeep"
git commit -m "tenant: create ${TENANT} (cpu=${MAX_CPU}, mem=${MAX_MEMORY})"
git push

echo "==> Done! ArgoCD will sync the tenant resources."
