.PHONY: help setup setup-microk8s setup-argocd setup-arc build build-cli build-controller build-dashboard \
       tenant-create env-create env-delete docker-build docker-push dashboard

REPO_URL ?= https://github.com/dusttostars/vcluster-ephemeral-envs.git
REGISTRY ?= ghcr.io/dusttostars/vcluster-ephemeral-envs

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# --- Setup ---

setup: setup-microk8s setup-argocd setup-arc ## Full platform setup (MicroK8s + ArgoCD + ARC)

setup-microk8s: ## Install and configure MicroK8s
	bash hack/setup-microk8s.sh

setup-argocd: ## Install ArgoCD and apply bootstrap app-of-apps
	bash hack/setup-argocd.sh

setup-arc: ## Configure ARC (GitHub Actions runners in-cluster)
	bash hack/setup-arc.sh

# --- Build ---

build: build-cli build-controller build-dashboard ## Build all Go binaries

build-cli: ## Build the ephemeral CLI
	go build -o bin/ephemeral ./cmd/cli

build-controller: ## Build the cleanup controller
	go build -o bin/controller ./cmd/controller

build-dashboard: ## Build the dashboard web server
	go build -o bin/dashboard ./cmd/dashboard

dashboard: build-dashboard ## Run the dashboard locally
	./bin/dashboard --repo-path=. --addr=:8090

# --- Docker ---

docker-build: ## Build Docker images
	docker build -t $(REGISTRY)/controller:latest -f Dockerfile.controller .
	docker build -t $(REGISTRY)/cli:latest -f Dockerfile.cli .

docker-push: ## Push Docker images
	docker push $(REGISTRY)/controller:latest
	docker push $(REGISTRY)/cli:latest

# --- Tenant Management ---

tenant-create: ## Create a tenant (TENANT=name [CPU=4] [MEMORY=8Gi])
	@test -n "$(TENANT)" || (echo "Usage: make tenant-create TENANT=team-a" && exit 1)
	bash scripts/create-tenant.sh "$(TENANT)" "$(CPU)" "$(MEMORY)"

# --- Environment Management ---

env-create: ## Create an ephemeral env (TENANT=name BRANCH=name [TTL=2h])
	@test -n "$(TENANT)" || (echo "Usage: make env-create TENANT=team-a BRANCH=feat-123" && exit 1)
	@test -n "$(BRANCH)" || (echo "Usage: make env-create TENANT=team-a BRANCH=feat-123" && exit 1)
	bash scripts/create-env.sh "$(TENANT)" "$(BRANCH)" "$(TTL)"

env-delete: ## Delete an ephemeral env (TENANT=name ENV=name)
	@test -n "$(TENANT)" || (echo "Usage: make env-delete TENANT=team-a ENV=feat-123" && exit 1)
	@test -n "$(ENV)" || (echo "Usage: make env-delete TENANT=team-a ENV=feat-123" && exit 1)
	bash scripts/delete-env.sh "$(TENANT)" "$(ENV)"

# --- Pipeline (run locally what CI does) ---

pipeline: ## Run the full PR pipeline locally (TENANT=name BRANCH=name)
	@test -n "$(TENANT)" || (echo "Usage: make pipeline TENANT=team-a BRANCH=feat-123" && exit 1)
	@test -n "$(BRANCH)" || (echo "Usage: make pipeline TENANT=team-a BRANCH=feat-123" && exit 1)
	@echo "==> Step 1: Ensure tenant exists"
	bash scripts/create-tenant.sh "$(TENANT)" || true
	@echo "==> Step 2: Create ephemeral environment"
	bash scripts/create-env.sh "$(TENANT)" "$(BRANCH)" "$(TTL)"
	@echo "==> Pipeline complete. ArgoCD will sync the vcluster."
