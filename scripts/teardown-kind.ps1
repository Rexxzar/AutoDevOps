$REGISTRY_NAME = "autodevops-registry"
$CLUSTER_NAME  = "autodevops"

Write-Host "Deleting kind cluster '$CLUSTER_NAME'..."
kind delete cluster --name $CLUSTER_NAME 2>$null

Write-Host "Removing registry container..."
docker rm -f $REGISTRY_NAME 2>$null

Write-Host "Done."