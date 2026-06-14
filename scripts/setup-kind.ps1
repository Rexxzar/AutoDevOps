param()

$REGISTRY_NAME    = "autodevops-registry"
$REGISTRY_PORT    = "5001"
$CLUSTER_NAME     = "autodevops"
$SCRIPT_DIR       = Split-Path -Parent $MyInvocation.MyCommand.Path
$INGRESS_MANIFEST = "https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/kind/deploy.yaml"

# 1. Local registry
docker inspect $REGISTRY_NAME 2>$null | Out-Null
if ($LASTEXITCODE -eq 0) {
    Write-Host "[1/5] Registry already running - skipping"
} else {
    Write-Host "[1/5] Starting local registry on localhost:${REGISTRY_PORT}..."
    docker run -d --restart=always -p "127.0.0.1:${REGISTRY_PORT}:5000" --name $REGISTRY_NAME registry:2
    if ($LASTEXITCODE -ne 0) { Write-Error "Failed to start registry"; exit 1 }
}

# 2. Kind cluster
$clusters = kind get clusters 2>$null
if ($clusters -contains $CLUSTER_NAME) {
    Write-Host "[2/5] Cluster '$CLUSTER_NAME' already exists - skipping"
} else {
    Write-Host "[2/5] Creating kind cluster '$CLUSTER_NAME'..."
    kind create cluster --name $CLUSTER_NAME --config "$SCRIPT_DIR\kind-config.yaml"
    if ($LASTEXITCODE -ne 0) { Write-Error "Failed to create cluster"; exit 1 }
}

# 3. Connect registry to kind Docker network with a `registry.local` alias
# (containerd's registry-mirror config in kind-config.yaml points at
# registry.local:5000 - without this alias, in-cluster image pulls fail
# with "no such host")
$networkJson = docker network inspect kind 2>$null
if ($networkJson -match "registry\.local") {
    Write-Host "[3/5] Registry already connected to kind network with registry.local alias - skipping"
} else {
    if ($networkJson -match $REGISTRY_NAME) {
        Write-Host "[3/5] Re-connecting registry to kind network with registry.local alias..."
        docker network disconnect kind $REGISTRY_NAME
    } else {
        Write-Host "[3/5] Connecting registry to kind network..."
    }
    docker network connect --alias registry.local kind $REGISTRY_NAME
    if ($LASTEXITCODE -ne 0) { Write-Error "Failed to connect registry to network"; exit 1 }
}

# 4. Nginx ingress controller
Write-Host "[4/5] Installing nginx ingress controller..."
kubectl apply -f $INGRESS_MANIFEST
if ($LASTEXITCODE -ne 0) { Write-Error "Failed to install ingress controller"; exit 1 }

# 5. Wait for ingress to be Ready
Write-Host "[5/5] Waiting for ingress controller to be Ready (up to 90s)..."
kubectl wait --namespace ingress-nginx --for=condition=ready pod --selector=app.kubernetes.io/component=controller --timeout=90s
if ($LASTEXITCODE -ne 0) { Write-Error "Ingress controller did not become ready in time"; exit 1 }

Write-Host ""
Write-Host "Cluster ready. kubeconfig written to $env:USERPROFILE\.kube\config"
Write-Host "Registry: localhost:${REGISTRY_PORT} (host) / registry.local:5000 (in-cluster)"