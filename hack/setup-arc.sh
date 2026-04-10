#!/usr/bin/env bash
set -euo pipefail

GITHUB_PAT="${GITHUB_PAT:?Set GITHUB_PAT environment variable}"
GITHUB_REPO="${GITHUB_REPO:-https://github.com/dusttostars/vcluster-ephemeral-envs}"

echo "==> Creating arc-runners secret with GitHub PAT..."
kubectl create namespace arc-runners --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret generic arc-github-token \
  --namespace arc-runners \
  --from-literal=github_token="$GITHUB_PAT" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "==> ARC will be deployed by ArgoCD via the app-of-apps."
echo "==> Make sure the arc-runners.yaml Application has the correct:"
echo "    - githubConfigUrl: $GITHUB_REPO"
echo "    - githubConfigSecret reference"
echo ""
echo "==> ARC setup complete. ArgoCD will sync the runner scale set."
