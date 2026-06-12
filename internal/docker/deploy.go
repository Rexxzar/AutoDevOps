package docker

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const dockerNetwork = "autodevops"

// hostPort derives a deterministic host port from the deployment name in the
// range 8100–8999 using FNV-32a hashing. Collisions are negligible for demo
// scale (< 10 deployments); an explicit port-reservation table would be added
// if the platform grew multi-tenant.
func hostPort(name string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(name))
	return 8100 + (h.Sum32() % 900)
}

// DockerDeploy deploys the built image as a Docker container. It:
//   - pings the daemon and returns ErrDockerNotFound if unreachable
//   - ensures the shared "autodevops" bridge network exists
//   - writes a docker-compose.yml next to the repo workspace (human reference)
//   - pulls the image from the host-accessible registry address
//   - stops + removes any existing container with the same name (idempotent re-deploy)
//   - creates and starts the container, mapping port 8080 → a deterministic host port
func (m *Manager) DockerDeploy(ctx context.Context, name, imageRef string) error {
	if _, err := m.client.Ping(ctx); err != nil {
		return &ErrDockerNotFound{Cause: err}
	}

	if err := m.ensureNetwork(ctx); err != nil {
		return fmt.Errorf("docker: ensure network: %w", err)
	}

	// The image_ref stored in DB uses the in-cluster pull address; Docker on the
	// host needs the externally-reachable push address for the same image.
	hostRef := strings.Replace(imageRef, m.registryPull, m.registryPush, 1)
	port := hostPort(name)

	composeDir := filepath.Join(m.workDir, name)
	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		return fmt.Errorf("docker: create compose dir: %w", err)
	}
	composePath := filepath.Join(composeDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(Compose(name, hostRef, port)), 0o644); err != nil {
		return fmt.Errorf("docker: write compose file: %w", err)
	}

	slog.Info("docker: pulling image", "name", name, "image", hostRef)
	reader, err := m.client.ImagePull(ctx, hostRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull image %s: %w", hostRef, err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	cname := containerName(name)

	// Remove any pre-existing container so re-deploys are clean.
	if err := m.removeContainer(ctx, cname); err != nil {
		return err
	}

	portKey := nat.Port("8080/tcp")
	cfg := &container.Config{
		Image: hostRef,
		ExposedPorts: nat.PortSet{
			portKey: struct{}{},
		},
		Env: []string{"PORT=8080"},
		Labels: map[string]string{
			"autodevops.io/managed-by": "autodevops",
			"autodevops.io/deployment": name,
		},
	}
	hostCfg := &container.HostConfig{
		NetworkMode: container.NetworkMode(dockerNetwork),
		PortBindings: nat.PortMap{
			portKey: []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", port)},
			},
		},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			dockerNetwork: {},
		},
	}

	resp, err := m.client.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, cname)
	if err != nil {
		return fmt.Errorf("docker: create container %s: %w", cname, err)
	}
	if err := m.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("docker: start container %s: %w", cname, err)
	}

	slog.Info("docker: container started",
		"name", name,
		"container", cname,
		"host_port", port,
		"id", resp.ID[:12],
	)
	return nil
}

func (m *Manager) ensureNetwork(ctx context.Context) error {
	if _, err := m.client.NetworkInspect(ctx, dockerNetwork, network.InspectOptions{}); err == nil {
		return nil
	} else if !dockerclient.IsErrNotFound(err) {
		return fmt.Errorf("inspect network: %w", err)
	}
	if _, err := m.client.NetworkCreate(ctx, dockerNetwork, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{"autodevops.io/managed-by": "autodevops"},
	}); err != nil {
		return fmt.Errorf("create network: %w", err)
	}
	return nil
}

// removeContainer stops and force-removes a container if it exists. A
// not-found error from either call is treated as already-removed, making
// this safe to call unconditionally before a (re-)create.
func (m *Manager) removeContainer(ctx context.Context, cname string) error {
	if err := m.client.ContainerStop(ctx, cname, container.StopOptions{}); err != nil && !dockerclient.IsErrNotFound(err) {
		slog.Warn("docker: stop existing container", "container", cname, "error", err)
	}
	if err := m.client.ContainerRemove(ctx, cname, container.RemoveOptions{Force: true}); err != nil && !dockerclient.IsErrNotFound(err) {
		return fmt.Errorf("docker: remove existing container %s: %w", cname, err)
	}
	return nil
}

// CreateContainer creates and starts a standalone container for a "local"
// Resource (Phase 2 — provider='local', resource_type='docker'). Unlike
// DockerDeploy, the image is used as-is (no registry address translation,
// since standalone resources use public images, not Paketo-built ones from
// the local registry) and no PORT env var is injected — arbitrary public
// images don't expect the Paketo PORT contract. containerPort maps to a
// deterministic host port via hostPort (8100-8999).
func (m *Manager) CreateContainer(ctx context.Context, name, imageRef string, containerPort uint16, resourceID string) (containerID string, hostPortOut uint32, err error) {
	if _, err := m.client.Ping(ctx); err != nil {
		return "", 0, &ErrDockerNotFound{Cause: err}
	}

	if err := m.ensureNetwork(ctx); err != nil {
		return "", 0, fmt.Errorf("docker: ensure network: %w", err)
	}

	port := hostPort(name)

	slog.Info("docker: pulling image", "name", name, "image", imageRef)
	reader, err := m.client.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("docker: pull image %s: %w", imageRef, err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	cname := containerName(name)
	if err := m.removeContainer(ctx, cname); err != nil {
		return "", 0, err
	}

	portKey := nat.Port(fmt.Sprintf("%d/tcp", containerPort))
	cfg := &container.Config{
		Image: imageRef,
		ExposedPorts: nat.PortSet{
			portKey: struct{}{},
		},
		Labels: map[string]string{
			"autodevops.io/managed-by": "autodevops",
			"autodevops.io/resource":   resourceID,
		},
	}
	hostCfg := &container.HostConfig{
		NetworkMode: container.NetworkMode(dockerNetwork),
		PortBindings: nat.PortMap{
			portKey: []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", port)},
			},
		},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			dockerNetwork: {},
		},
	}

	resp, err := m.client.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, cname)
	if err != nil {
		return "", 0, fmt.Errorf("docker: create container %s: %w", cname, err)
	}
	if err := m.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", 0, fmt.Errorf("docker: start container %s: %w", cname, err)
	}

	slog.Info("docker: resource container started",
		"name", name,
		"container", cname,
		"host_port", port,
		"id", resp.ID[:12],
	)
	return resp.ID[:12], port, nil
}

// RemoveContainer stops and removes the standalone container backing a
// "local" Resource. A missing container is treated as already-removed, not
// an error — the destroy path is idempotent.
func (m *Manager) RemoveContainer(ctx context.Context, name string) error {
	if _, err := m.client.Ping(ctx); err != nil {
		return &ErrDockerNotFound{Cause: err}
	}
	return m.removeContainer(ctx, containerName(name))
}
