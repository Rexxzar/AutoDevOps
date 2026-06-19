#!/usr/bin/env bash
set -euo pipefail

REGISTRY_NAME="autodevops-registry"
REGISTRY_PORT="5001"
CLUSTER_NAME="autodevops"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INGRESS_MANIFEST="https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/kind/deploy.yaml"

# Pick host ports that don't conflict with existing services
port_free() { ! ss -tlnH "sport = :$1" 2>/dev/null | grep -q .; }
if port_free 80 && port_free 443; then
  HTTP_PORT=80; HTTPS_PORT=443
else
  HTTP_PORT=8880; HTTPS_PORT=8443
  echo "[info] Ports 80/443 in use — using ${HTTP_PORT}/${HTTPS_PORT} for ingress instead"
fi

# 1. Local registry
if docker inspect "$REGISTRY_NAME" &>/dev/null; then
  echo "[1/6] Registry already running — skipping"
else
  echo "[1/6] Starting local registry on localhost:${REGISTRY_PORT}..."
  docker run -d --restart=always -p "127.0.0.1:${REGISTRY_PORT}:5000" \
    -e REGISTRY_STORAGE_DELETE_ENABLED=true \
    --name "$REGISTRY_NAME" registry:2
fi

# 2. Kind cluster — generate config with correct host ports
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
  echo "[2/6] Cluster '${CLUSTER_NAME}' already exists — skipping"
else
  echo "[2/6] Creating kind cluster '${CLUSTER_NAME}' (ingress on :${HTTP_PORT}/:${HTTPS_PORT})..."
  TMPCONFIG="$(mktemp /tmp/kind-config-XXXXXX.yaml)"
  sed "s/hostPort: 80$/hostPort: ${HTTP_PORT}/; s/hostPort: 443$/hostPort: ${HTTPS_PORT}/" \
    "$SCRIPT_DIR/kind-config.yaml" > "$TMPCONFIG"
  kind create cluster --name "$CLUSTER_NAME" --config "$TMPCONFIG"
  rm -f "$TMPCONFIG"
fi

# 3. Connect registry to kind Docker network with a `registry.local` alias
# (containerd's registry-mirror config in kind-config.yaml points at
# registry.local:5000 — without this alias, in-cluster image pulls fail
# with "no such host")
if docker network inspect kind 2>/dev/null | grep -q '"registry.local"'; then
  echo "[3/6] Registry already connected to kind network with registry.local alias — skipping"
else
  if docker network inspect kind 2>/dev/null | grep -q "$REGISTRY_NAME"; then
    echo "[3/6] Re-connecting registry to kind network with registry.local alias..."
    docker network disconnect kind "$REGISTRY_NAME"
  else
    echo "[3/6] Connecting registry to kind network..."
  fi
  docker network connect --alias registry.local kind "$REGISTRY_NAME"
fi

# 4. Nginx ingress controller
echo "[4/6] Installing nginx ingress controller..."
kubectl apply -f "$INGRESS_MANIFEST"

# 5. Wait for ingress pod to be Ready
echo "[5/6] Waiting for ingress controller to be Ready (up to 90s)..."
kubectl wait --namespace ingress-nginx \
  --for=condition=ready pod \
  --selector=app.kubernetes.io/component=controller \
  --timeout=90s

# 6. metrics-server — required for HorizontalPodAutoscaler (HPA) CPU metrics.
# kind nodes use self-signed kubelet certs, so metrics-server needs
# --kubelet-insecure-tls or it can't scrape (CrashLoop "x509: cannot validate").
METRICS_MANIFEST="https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
if kubectl get deployment metrics-server -n kube-system &>/dev/null; then
  echo "[6/6] metrics-server already installed — skipping"
else
  echo "[6/6] Installing metrics-server (for HPA)..."
  kubectl apply -f "$METRICS_MANIFEST"
  kubectl patch -n kube-system deployment metrics-server --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
  kubectl wait --namespace kube-system \
    --for=condition=available deployment/metrics-server \
    --timeout=120s || echo "[warn] metrics-server not ready yet — HPA may take a minute to get metrics"
fi

echo ""
echo "Cluster ready. kubeconfig written to ~/.kube/config"
echo "Registry:  localhost:${REGISTRY_PORT} (host)  /  registry.local:5000 (in-cluster)"
echo "Ingress:   http://localhost:${HTTP_PORT}  /  https://localhost:${HTTPS_PORT}"
