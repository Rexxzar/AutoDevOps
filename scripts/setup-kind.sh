#!/usr/bin/env bash
set -euo pipefail

REGISTRY_NAME="autodevops-registry"
REGISTRY_PORT="5001"
CLUSTER_NAME="autodevops"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INGRESS_MANIFEST="https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/kind/deploy.yaml"

# 1. Local registry
if docker inspect "$REGISTRY_NAME" &>/dev/null; then
  echo "[1/5] Registry already running — skipping"
else
  echo "[1/5] Starting local registry on localhost:${REGISTRY_PORT}..."
  docker run -d --restart=always -p "127.0.0.1:${REGISTRY_PORT}:5000" --name "$REGISTRY_NAME" registry:2
fi

# 2. Kind cluster
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
  echo "[2/5] Cluster '${CLUSTER_NAME}' already exists — skipping"
else
  echo "[2/5] Creating kind cluster '${CLUSTER_NAME}'..."
  kind create cluster --name "$CLUSTER_NAME" --config "$SCRIPT_DIR/kind-config.yaml"
fi

# 3. Connect registry to kind Docker network
if docker network inspect kind 2>/dev/null | grep -q "$REGISTRY_NAME"; then
  echo "[3/5] Registry already connected to kind network — skipping"
else
  echo "[3/5] Connecting registry to kind network..."
  docker network connect kind "$REGISTRY_NAME"
fi

# 4. Nginx ingress controller
echo "[4/5] Installing nginx ingress controller..."
kubectl apply -f "$INGRESS_MANIFEST"

# 5. Wait for ingress pod to be Ready
echo "[5/5] Waiting for ingress controller to be Ready (up to 90s)..."
kubectl wait --namespace ingress-nginx \
  --for=condition=ready pod \
  --selector=app.kubernetes.io/component=controller \
  --timeout=90s

echo ""
echo "Cluster ready. kubeconfig written to ~/.kube/config"
echo "Registry: localhost:${REGISTRY_PORT} (host)  /  registry.local:5000 (in-cluster)"
