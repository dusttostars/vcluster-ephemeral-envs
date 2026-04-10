#!/usr/bin/env bash
set -euo pipefail

TENANT="${1:?Usage: create-env.sh <tenant> <branch> [ttl]}"
BRANCH="${2:?Usage: create-env.sh <tenant> <branch> [ttl]}"
TTL="${3:-2h}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

ENV_NAME="${BRANCH//\//-}"

echo "==> Creating ephemeral environment"
echo "    Tenant: $TENANT"
echo "    Branch: $BRANCH"
echo "    Name:   $ENV_NAME"
echo "    TTL:    $TTL"

# Generate the ArgoCD Application manifest for the vcluster.
NAMESPACE="tenant-${TENANT}"
CREATED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
TARGET_DIR="${REPO_ROOT}/manifests/environments/${TENANT}"
mkdir -p "$TARGET_DIR"

cat > "${TARGET_DIR}/${ENV_NAME}.yaml" <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: vcluster-${ENV_NAME}
  namespace: argocd
  labels:
    ephemeral.io/tenant: "${TENANT}"
    ephemeral.io/branch: "${BRANCH}"
    ephemeral.io/ttl: "${TTL}"
    ephemeral.io/created-at: "${CREATED_AT}"
    ephemeral.io/managed-by: "ephemeral-controller"
spec:
  project: tenant-${TENANT}
  source:
    repoURL: https://charts.loft.sh
    chart: vcluster
    targetRevision: "0.19.*"
    helm:
      values: |
        syncer:
          extraArgs:
            - --name=${ENV_NAME}
        vcluster:
          image: rancher/k3s:v1.29.1-k3s2
        sync:
          ingresses:
            enabled: true
          services:
            enabled: true
  destination:
    server: https://kubernetes.default.svc
    namespace: ${NAMESPACE}
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
EOF

echo "==> Manifest written: ${TARGET_DIR}/${ENV_NAME}.yaml"
echo "==> Committing and pushing..."

cd "$REPO_ROOT"
git add "manifests/environments/${TENANT}/${ENV_NAME}.yaml"
git commit -m "env: create ${ENV_NAME} for tenant ${TENANT} (ttl=${TTL})"
git push

echo "==> Done! ArgoCD will sync the new vcluster."
echo "    Connect: vcluster connect ${ENV_NAME} -n ${NAMESPACE}"
