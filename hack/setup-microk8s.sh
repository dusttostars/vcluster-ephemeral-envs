#!/usr/bin/env bash
set -euo pipefail

echo "==> Installing MicroK8s..."
sudo snap install microk8s --classic --channel=1.29/stable

echo "==> Adding current user to microk8s group..."
sudo usermod -aG microk8s "$USER"
newgrp microk8s

echo "==> Waiting for MicroK8s to be ready..."
microk8s status --wait-ready

echo "==> Enabling addons..."
microk8s enable dns
microk8s enable helm3
microk8s enable storage
microk8s enable rbac
microk8s enable ingress

echo "==> Setting up kubectl alias..."
mkdir -p "$HOME/.kube"
microk8s config > "$HOME/.kube/config"

echo "==> Installing vcluster CLI..."
curl -L -o vcluster "https://github.com/loft-sh/vcluster/releases/latest/download/vcluster-linux-amd64"
chmod +x vcluster
sudo mv vcluster /usr/local/bin/

echo "==> MicroK8s is ready!"
kubectl cluster-info
