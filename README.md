# vcluster-ephemeral-envs

Ephemeral Kubernetes environments powered by **vcluster**, managed via **GitOps (ArgoCD)**, running on **MicroK8s** with **self-hosted GitHub Actions runners (ARC)**.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  MicroK8s Cluster                                       │
│                                                         │
│  ┌──────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │  ArgoCD  │  │     ARC      │  │ Cleanup Controller │  │
│  │          │  │  (GH Runner  │  │  (Go, TTL-based)   │  │
│  │  GitOps  │  │   in-cluster)│  │                    │  │
│  └────┬─────┘  └──────┬───────┘  └────────┬───────────┘  │
│       │               │                   │              │
│       ▼               ▼                   ▼              │
│  ┌─────────────────────────────────────────────────────┐ │
│  │  tenant-team-a/         tenant-team-b/              │ │
│  │  ├── vcluster-feat-123  ├── vcluster-fix-789       │ │
│  │  └── vcluster-feat-456  └── (quotas, RBAC)         │ │
│  └─────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
```

## How it works

1. **PR opened** → GitHub Actions workflow (on self-hosted runner) generates a vcluster manifest and commits it
2. **ArgoCD detects** the new manifest → syncs → vcluster spins up in the tenant's namespace
3. **PR closed** → cleanup workflow removes the manifest → ArgoCD prunes the vcluster
4. **TTL expired** → cleanup controller removes the manifest and deletes the ArgoCD Application

No `kubectl apply` in pipelines — everything goes through git.

## Quick Start

### 1. Setup the platform

```bash
# Install MicroK8s with required addons
make setup-microk8s

# Install ArgoCD and bootstrap the app-of-apps
make setup-argocd

# Configure ARC (needs GITHUB_PAT)
export GITHUB_PAT=ghp_xxxxx
make setup-arc
```

### 2. Create a tenant

```bash
make tenant-create TENANT=team-a CPU=4 MEMORY=8Gi
```

### 3. Create an ephemeral environment

```bash
# Via make
make env-create TENANT=team-a BRANCH=feat-login TTL=2h

# Via CLI (after `make build`)
./bin/ephemeral create feat-login --tenant team-a --branch feat-login --ttl 2h

# Connect to the vcluster
vcluster connect feat-login -n tenant-team-a
```

### 4. Delete an environment

```bash
make env-delete TENANT=team-a ENV=feat-login
```

### 5. Run the full pipeline locally

```bash
make pipeline TENANT=team-a BRANCH=feat-login
```

## Project Structure

```
├── cmd/
│   ├── cli/                 # CLI to manage environments and tenants
│   └── controller/          # Cleanup controller entrypoint
├── internal/
│   ├── vcluster/            # vcluster manifest generation
│   ├── cleanup/             # TTL expiration and reconcile loop
│   └── tenant/              # Tenant namespace, quotas, RBAC generation
├── argocd/
│   ├── bootstrap/           # App-of-apps root Application
│   ├── apps/                # ArgoCD Applications (tenants, envs, controller, ARC)
│   └── projects/            # ArgoCD AppProjects per tenant
├── manifests/
│   ├── controller/          # Cleanup controller K8s manifests
│   ├── tenants/             # Per-tenant: namespace, quota, role, rolebinding
│   └── environments/        # Per-tenant vcluster ArgoCD Applications
├── scripts/                 # Shell scripts (used by CI and locally)
├── hack/                    # Platform setup scripts
├── .github/workflows/       # PR create/cleanup workflows (self-hosted runners)
├── Makefile                 # Single entry point for all operations
└── Dockerfile.*             # Container images for controller and CLI
```

## Multi-Tenancy

Each tenant gets:
- **Dedicated namespace** (`tenant-<name>`)
- **ResourceQuota** — CPU/memory limits
- **RBAC** — scoped Role + RoleBinding
- **ArgoCD AppProject** — can only deploy to their own namespace
- **Isolated environments directory** — `manifests/environments/<tenant>/`

## Auto-Cleanup

The cleanup controller runs in `ephemeral-system` and:
- Polls every 60s for ArgoCD Applications labeled with `ephemeral.io/managed-by`
- Compares `ephemeral.io/created-at` + `ephemeral.io/ttl` against current time
- Enforces a hard `--max-age` ceiling (default 24h) regardless of TTL
- Removes expired manifests from git and deletes the ArgoCD Application

## CI/CD

GitHub Actions workflows run on **self-hosted runners inside MicroK8s** (via ARC):
- `pr-environment.yaml` — creates an ephemeral vcluster when a PR is opened
- `pr-cleanup.yaml` — removes the environment when the PR is closed
- Tenant is determined by PR label `tenant:<name>` (defaults to `default`)
