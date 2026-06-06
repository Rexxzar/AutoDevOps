#!/usr/bin/env bash
set -euo pipefail

REGISTRY_NAME="autodevops-registry"
CLUSTER_NAME="autodevops"

echo "Deleting kind cluster '${CLUSTER_NAME}'..."
kind delete cluster --name "$CLUSTER_NAME" || true

echo "Removing registry container..."
docker rm -f "$REGISTRY_NAME" 2>/dev/null || true

echo "Done."
