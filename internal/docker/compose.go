package docker

import "fmt"

// Compose generates a docker-compose.yml string for a deployment.
// Written to disk as a human-readable reference artifact; actual deployment
// is driven by the Docker SDK (no docker-compose CLI dependency).
func Compose(name, imageRef string, port uint32) string {
	return fmt.Sprintf(`version: "3.8"
services:
  %s:
    image: %s
    container_name: autodevops-%s
    restart: unless-stopped
    networks:
      - autodevops
    ports:
      - "%d:8080"
    environment:
      - PORT=8080
    labels:
      autodevops.io/managed-by: autodevops
      autodevops.io/deployment: %s

networks:
  autodevops:
    external: true
`, name, imageRef, name, port, name)
}
