#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "==> Creating argocd namespace..."
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -

echo "==> Installing ArgoCD..."
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

echo "==> Waiting for ArgoCD to be ready..."
kubectl wait --for=condition=available deployment/argocd-server -n argocd --timeout=300s

echo "==> Getting ArgoCD admin password..."
ARGOCD_PASS=$(kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath="{.data.password}" | base64 -d)
echo "  Admin password: $ARGOCD_PASS"

echo "==> Applying bootstrap app-of-apps..."
kubectl apply -f "$REPO_ROOT/argocd/bootstrap/app-of-apps.yaml"

echo "==> ArgoCD is ready!"
echo "  Access: kubectl port-forward svc/argocd-server -n argocd 8080:443"
echo "  Login:  admin / $ARGOCD_PASS"
