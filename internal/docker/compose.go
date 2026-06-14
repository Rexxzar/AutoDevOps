package docker

import "fmt"

// Compose generates a docker-compose.yml string for an app within a stack.
// Written to disk as a human-readable reference artifact; actual deployment
// is driven by the Docker SDK (no docker-compose CLI dependency).
func Compose(stackName, appName, imageRef string, port uint32) string {
	netName := stackNetworkName(stackName)
	return fmt.Sprintf(`version: "3.8"
services:
  %s:
    image: %s
    container_name: %s
    restart: unless-stopped
    networks:
      %s:
        aliases:
          - %s
    ports:
      - "%d:8080"
    environment:
      - PORT=8080
    labels:
      autodevops.io/managed-by: autodevops
      autodevops.io/stack: %s
      autodevops.io/app: %s

networks:
  %s:
    external: true
`, appName, imageRef, appContainerName(stackName, appName), netName, appName, port, stackName, appName, netName)
}
