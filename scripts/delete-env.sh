#!/usr/bin/env bash
set -euo pipefail

TENANT="${1:?Usage: delete-env.sh <tenant> <env-name>}"
ENV_NAME="${2:?Usage: delete-env.sh <tenant> <env-name>}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

MANIFEST="${REPO_ROOT}/manifests/environments/${TENANT}/${ENV_NAME}.yaml"

if [ ! -f "$MANIFEST" ]; then
  echo "Manifest not found: $MANIFEST"
  exit 1
fi

echo "==> Removing ephemeral environment: ${ENV_NAME} (tenant=${TENANT})"

rm "$MANIFEST"

cd "$REPO_ROOT"
git config user.email "ephemeral-bot@users.noreply.github.com"
git config user.name "ephemeral-bot"
git add "manifests/environments/${TENANT}/${ENV_NAME}.yaml"
git commit -m "env: delete ${ENV_NAME} for tenant ${TENANT}"
git push origin HEAD:master

echo "==> Done! ArgoCD will prune the vcluster."
