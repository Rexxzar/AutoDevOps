package docker

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// DefaultNetwork is the shared bridge network used when a "local" Resource
// doesn't specify resource_opts.network — created on demand by ensureNetwork.
const DefaultNetwork = "autodevops"

// hostPort derives a deterministic host port from the deployment name in the
// range 8100–8999 using FNV-32a hashing. Collisions are negligible for demo
// scale (< 10 deployments); an explicit port-reservation table would be added
// if the platform grew multi-tenant.
func hostPort(name string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(name))
	return 8100 + (h.Sum32() % 900)
}

// AppHostPort returns the deterministic host port DockerDeploy bound
// container port 8080 to for the given stack/app — lets the API layer
// surface a clickable "http://localhost:<port>" URL for local_docker
// deployment apps, mirroring the Ingress URL shown for local_k8s apps.
func AppHostPort(stackName, appName string) uint32 {
	return hostPort(appContainerName(stackName, appName))
}

// envSlice converts a map of environment variables into a deterministically
// sorted []string in "KEY=VALUE" form for container.Config.Env — sorted so
// repeated (re-)creates of the same container produce identical config.
func envSlice(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s=%s", k, env[k]))
	}
	return out
}

// sanitizeSecretKey turns a container path ("/opt/secrets/firebase.json")
// into a filesystem-safe filename ("opt_secrets_firebase.json") for the
// host-side secrets directory written by writeSecretFiles.
func sanitizeSecretKey(containerPath string) string {
	return strings.ReplaceAll(strings.TrimPrefix(containerPath, "/"), "/", "_")
}

// writeSecretFiles writes each decrypted secret file to
// workDir/<stackName>/<appName>/secrets/<sanitized> (0o600, overwritten on
// every call so re-deploys pick up updated content) and returns one
// read-only bind mount per file, target = the original container path.
func (m *Manager) writeSecretFiles(stackName, appName string, secretFiles map[string][]byte) ([]mount.Mount, error) {
	if len(secretFiles) == 0 {
		return nil, nil
	}
	dir := filepath.Join(m.workDir, stackName, appName, "secrets")
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("docker: resolve secrets dir: %w", err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, fmt.Errorf("docker: create secrets dir: %w", err)
	}
	mounts := make([]mount.Mount, 0, len(secretFiles))
	for containerPath, content := range secretFiles {
		hostPath := filepath.Join(absDir, sanitizeSecretKey(containerPath))
		if err := os.WriteFile(hostPath, content, 0o600); err != nil {
			return nil, fmt.Errorf("docker: write secret file %q: %w", containerPath, err)
		}
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: hostPath, Target: containerPath, ReadOnly: true})
	}
	return mounts, nil
}

// DockerDeploy deploys the built image as a Docker container on the stack's
// per-stack network. It:
//   - pings the daemon and returns ErrDockerNotFound if unreachable
//   - ensures the per-stack "autodevops-<stackName>" bridge network exists
//   - writes a docker-compose.yml next to the repo workspace (human reference)
//   - pulls the image from the host-accessible registry address
//   - stops + removes any existing container with the same name (idempotent re-deploy)
//   - creates and starts the container, mapping port 8080 → a deterministic host port,
//     attached to the stack network with a network alias of appName for
//     short-name resolution by other apps in the stack
func (m *Manager) DockerDeploy(ctx context.Context, stackName, appName, imageRef string, extraEnv map[string]string, secretFiles map[string][]byte) error {
	if _, err := m.client.Ping(ctx); err != nil {
		return &ErrDockerNotFound{Cause: err}
	}

	netName := stackNetworkName(stackName)
	if err := m.ensureNetworkNamed(ctx, netName); err != nil {
		return fmt.Errorf("docker: ensure network: %w", err)
	}

	// The image_ref stored in DB uses the in-cluster pull address; Docker on the
	// host needs the externally-reachable push address for the same image.
	hostRef := strings.Replace(imageRef, m.registryPull, m.registryPush, 1)
	cname := appContainerName(stackName, appName)
	port := hostPort(cname)

	composeDir := filepath.Join(m.workDir, stackName, appName)
	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		return fmt.Errorf("docker: create compose dir: %w", err)
	}
	composePath := filepath.Join(composeDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(Compose(stackName, appName, hostRef, port)), 0o644); err != nil {
		return fmt.Errorf("docker: write compose file: %w", err)
	}

	// Skip the registry pull if the image is already in the local daemon (e.g.,
	// just built by buildImage). Pull is only needed for re-deploys where the
	// daemon no longer has the image but the registry does.
	if _, _, inspErr := m.client.ImageInspectWithRaw(ctx, hostRef); inspErr != nil {
		slog.Info("docker: pulling image", "name", appName, "image", hostRef)
		reader, err := m.client.ImagePull(ctx, hostRef, image.PullOptions{})
		if err != nil {
			return fmt.Errorf("docker: pull image %s: %w", hostRef, err)
		}
		io.Copy(io.Discard, reader)
		reader.Close()
	}

	// Remove any pre-existing container so re-deploys are clean.
	if err := m.removeContainer(ctx, cname); err != nil {
		return err
	}

	secretMounts, err := m.writeSecretFiles(stackName, appName, secretFiles)
	if err != nil {
		return err
	}

	portKey := nat.Port("8080/tcp")
	env := append([]string{"PORT=8080"}, envSlice(extraEnv)...)

	cfg := &container.Config{
		Image: hostRef,
		ExposedPorts: nat.PortSet{
			portKey: struct{}{},
		},
		Env: env,
		Labels: map[string]string{
			"autodevops.io/managed-by": "autodevops",
			"autodevops.io/stack":      stackName,
			"autodevops.io/app":        appName,
		},
	}
	hostCfg := &container.HostConfig{
		NetworkMode: container.NetworkMode(netName),
		PortBindings: nat.PortMap{
			portKey: []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", port)},
			},
		},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		Mounts:        secretMounts,
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			netName: {Aliases: []string{appName}},
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
		"name", appName,
		"container", cname,
		"host_port", port,
		"id", resp.ID[:12],
	)
	return nil
}

// DockerDeployImageApp deploys a Phase-4 "image"-kind app (e.g. a postgres:16
// db container) onto the stack's per-stack network, the counterpart to
// DockerDeploy for apps with no build/clone pipeline. Unlike DockerDeploy,
// imageRef is used as-is (no registry address translation — image apps pull
// public images, not Paketo-built ones from the local registry) and no PORT
// env var is injected; env holds the user-supplied image_env_secret_id
// contents (e.g. POSTGRES_PASSWORD). The container is bound to the same
// deterministic host port as DockerDeploy would for this stack/app
// (hostPort(appContainerName(...))) and given a network alias of appName for
// short-name resolution by sibling apps. volumePath, if non-empty, mounts a
// named volume "<cname>-data" at that path — Docker creates the named volume
// implicitly on first use, and re-deploys (removeContainer + re-create) reuse
// the same volume since it's keyed by name, not by container.
func (m *Manager) DockerDeployImageApp(ctx context.Context, stackName, appName, imageRef string, containerPort uint16, env map[string]string, volumePath string, secretFiles map[string][]byte) error {
	if _, err := m.client.Ping(ctx); err != nil {
		return &ErrDockerNotFound{Cause: err}
	}

	netName := stackNetworkName(stackName)
	if err := m.ensureNetworkNamed(ctx, netName); err != nil {
		return fmt.Errorf("docker: ensure network: %w", err)
	}

	cname := appContainerName(stackName, appName)
	port := hostPort(cname)

	slog.Info("docker: pulling image", "name", appName, "image", imageRef)
	reader, err := m.client.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull image %s: %w", imageRef, err)
	}
	io.Copy(io.Discard, reader)
	reader.Close()

	// Remove any pre-existing container so re-deploys are clean.
	if err := m.removeContainer(ctx, cname); err != nil {
		return err
	}

	secretMounts, err := m.writeSecretFiles(stackName, appName, secretFiles)
	if err != nil {
		return err
	}

	portKey := nat.Port(fmt.Sprintf("%d/tcp", containerPort))
	cfg := &container.Config{
		Image: imageRef,
		ExposedPorts: nat.PortSet{
			portKey: struct{}{},
		},
		Env: envSlice(env),
		Labels: map[string]string{
			"autodevops.io/managed-by": "autodevops",
			"autodevops.io/stack":      stackName,
			"autodevops.io/app":        appName,
		},
	}
	hostCfg := &container.HostConfig{
		NetworkMode: container.NetworkMode(netName),
		PortBindings: nat.PortMap{
			portKey: []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", port)},
			},
		},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}
	if volumePath != "" {
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{Type: mount.TypeVolume, Source: cname + "-data", Target: volumePath})
	}
	hostCfg.Mounts = append(hostCfg.Mounts, secretMounts...)
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			netName: {Aliases: []string{appName}},
		},
	}

	resp, err := m.client.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, cname)
	if err != nil {
		return fmt.Errorf("docker: create container %s: %w", cname, err)
	}
	if err := m.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("docker: start container %s: %w", cname, err)
	}

	slog.Info("docker: image app container started",
		"name", appName,
		"container", cname,
		"host_port", port,
		"port", containerPort,
		"id", resp.ID[:12],
	)
	return nil
}

func (m *Manager) ensureNetwork(ctx context.Context) error {
	return m.ensureNetworkNamed(ctx, DefaultNetwork)
}

// ensureNetworkNamed creates a bridge network with the given name if it
// doesn't already exist. Used both for the shared DefaultNetwork and for
// user-requested new networks (resource_opts.create_network).
func (m *Manager) ensureNetworkNamed(ctx context.Context, name string) error {
	if _, err := m.client.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return nil
	} else if !dockerclient.IsErrNotFound(err) {
		return fmt.Errorf("inspect network: %w", err)
	}
	if _, err := m.client.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{"autodevops.io/managed-by": "autodevops"},
	}); err != nil {
		return fmt.Errorf("create network: %w", err)
	}
	return nil
}

// NetworkInfo is a Docker bridge network available as a placement target for
// a "local" Docker Resource (resource_opts.network).
type NetworkInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListNetworks returns user-visible bridge networks (excluding "none"/"host",
// whose drivers don't support the PortBindings-based host-port model
// CreateContainer relies on) for the "Docker network" picker in the
// new-resource form.
func (m *Manager) ListNetworks(ctx context.Context) ([]NetworkInfo, error) {
	if _, err := m.client.Ping(ctx); err != nil {
		return nil, &ErrDockerNotFound{Cause: err}
	}
	nets, err := m.client.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: list networks: %w", err)
	}
	out := make([]NetworkInfo, 0, len(nets))
	for _, n := range nets {
		if n.Driver != "bridge" {
			continue
		}
		out = append(out, NetworkInfo{ID: n.ID[:12], Name: n.Name})
	}
	return out, nil
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
// images don't expect the Paketo PORT contract. containerPort is the port the
// container listens on; hostPortOverride, if non-zero, is bound on the host
// instead of the deterministic hostPort (8100-8999) hash. networkName, if
// non-empty, must name a bridge network (from ListNetworks) the container is
// attached to instead of the DefaultNetwork — unless createNetwork is true,
// in which case a new bridge network named networkName is created if it
// doesn't already exist.
func (m *Manager) CreateContainer(ctx context.Context, name, imageRef string, containerPort, hostPortOverride uint16, networkName string, createNetwork bool, resourceID string) (containerID string, hostPortOut uint32, err error) {
	if _, err := m.client.Ping(ctx); err != nil {
		return "", 0, &ErrDockerNotFound{Cause: err}
	}

	if networkName == "" {
		networkName = DefaultNetwork
		if err := m.ensureNetwork(ctx); err != nil {
			return "", 0, fmt.Errorf("docker: ensure network: %w", err)
		}
	} else if createNetwork {
		if err := m.ensureNetworkNamed(ctx, networkName); err != nil {
			return "", 0, fmt.Errorf("docker: create network %q: %w", networkName, err)
		}
	} else if _, err := m.client.NetworkInspect(ctx, networkName, network.InspectOptions{}); err != nil {
		if dockerclient.IsErrNotFound(err) {
			return "", 0, fmt.Errorf("docker: network %q not found", networkName)
		}
		return "", 0, fmt.Errorf("docker: inspect network %q: %w", networkName, err)
	}

	port := hostPort(name)
	if hostPortOverride != 0 {
		port = uint32(hostPortOverride)
	}

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
		NetworkMode: container.NetworkMode(networkName),
		PortBindings: nat.PortMap{
			portKey: []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", port)},
			},
		},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
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
	cname := containerName(name)
	if err := m.removeContainer(ctx, cname); err != nil {
		return err
	}
	m.removeVolume(ctx, cname)
	return nil
}

// RemoveAppContainer stops and removes a single app's container within a
// stack — the per-app counterpart to RemoveContainer (standalone Resources).
// A missing container is treated as already-removed, not an error.
func (m *Manager) RemoveAppContainer(ctx context.Context, stackName, appName string) error {
	if _, err := m.client.Ping(ctx); err != nil {
		return &ErrDockerNotFound{Cause: err}
	}
	cname := appContainerName(stackName, appName)
	if err := m.removeContainer(ctx, cname); err != nil {
		return err
	}
	m.removeVolume(ctx, cname)
	return nil
}

// removeVolume removes the data volume associated with a container.
// Best-effort: missing or in-use volumes are logged and ignored.
func (m *Manager) removeVolume(ctx context.Context, cname string) {
	volName := cname + "-data"
	if err := m.client.VolumeRemove(ctx, volName, true); err != nil && !dockerclient.IsErrNotFound(err) {
		slog.Warn("docker: remove volume", "volume", volName, "error", err)
	}
}

// RemoveStackNetwork removes the per-stack Docker bridge network — called by
// whole-stack destroy once every app's container has been removed.
// Best-effort: a missing network, or one Docker won't remove because it's
// still attached to a container, is logged and otherwise ignored so it
// doesn't block the rest of the destroy.
func (m *Manager) RemoveStackNetwork(ctx context.Context, stackName string) {
	name := stackNetworkName(stackName)
	if err := m.client.NetworkRemove(ctx, name); err != nil && !dockerclient.IsErrNotFound(err) {
		slog.Warn("docker: remove stack network", "network", name, "error", err)
	}
}
