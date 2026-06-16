// Package docker implements the Docker container target for AutoDevOps:
// deploying Paketo-built images as plain Docker containers, monitoring their
// health via ContainerInspect polling, and self-healing stopped containers
// using the same exponential-backoff pattern as internal/k8s. Mirrors the
// internal/k8s package structure (Manager ≈ Controller, healer ≈ healer).
package docker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Rexxzar/autodevops/internal/sse"
	dockertypes "github.com/docker/docker/api/types"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ContainerHealth is the classified state of a managed container, published
// via SSE as "container.health" after every poll tick — mirrors k8s.PodHealth.
// Kind distinguishes which entity Name/AppID/ResourceID refers to: "deployment"
// (git-deploy app, internal/deploy) or "resource" (standalone local Resource,
// provider='local' resource_type='docker').
type ContainerHealth struct {
	AppID         string `json:"app_id,omitempty"`
	ResourceID    string `json:"resource_id,omitempty"`
	Kind          string `json:"kind"`           // "deployment" | "resource"
	Name          string `json:"name"`           // app/resource name, not container name
	ContainerName string `json:"container_name,omitempty"`
	ContainerID   string `json:"container_id,omitempty"`
	Status        string `json:"status"` // "running" | "stopped" | "failed" | "missing" | "unknown"
	Reason        string `json:"reason,omitempty"`
}

// ErrDockerNotFound is returned by DockerDeploy when the daemon is unreachable.
// It carries install guidance so callers can surface a useful message rather
// than a raw connection-refused error — the alternative to the silent-install
// path explicitly rejected for Docker (privileged daemon, unlike Terraform's
// static binary which can be auto-installed via hc-install).
type ErrDockerNotFound struct {
	Cause error
}

func (e *ErrDockerNotFound) Error() string {
	return fmt.Sprintf(
		"Docker daemon not reachable (%v).\n"+
			"Install Docker and ensure the daemon is running:\n"+
			"  Linux:   curl -fsSL https://get.docker.com | sh\n"+
			"  Windows: https://docs.docker.com/desktop/install/windows-install/\n"+
			"  macOS:   https://docs.docker.com/desktop/install/mac-install/",
		e.Cause,
	)
}

func (e *ErrDockerNotFound) Unwrap() error { return e.Cause }

// Manager polls running container deployments and drives the self-healing loop.
type Manager struct {
	client       *dockerclient.Client
	pool         *pgxpool.Pool
	hub          *sse.Hub
	healer       *healer
	workDir      string
	registryPush string // host:port accessible from the Docker host (e.g. "localhost:5001")
	registryPull string // host:port stored in image_ref for K8s use (e.g. "registry.local:5000")
}

// New builds a Manager. The Docker client is constructed from the environment
// (DOCKER_HOST or the platform-default socket) with API version negotiation —
// identical to how the pack library connects for image builds. New never pings
// the daemon; ErrDockerNotFound surfaces at DockerDeploy time so the server
// starts even when Docker is absent.
func New(pool *pgxpool.Pool, hub *sse.Hub, workDir, registryPush, registryPull string) (*Manager, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker: new client: %w", err)
	}
	m := &Manager{
		client:       cli,
		pool:         pool,
		hub:          hub,
		workDir:      workDir,
		registryPush: registryPush,
		registryPull: registryPull,
	}
	m.healer = newHealer(cli, hub)
	return m, nil
}

// Client returns the underlying Docker client for use by packages that
// depend on internal/docker via injection (e.g. internal/deploy/tls.go).
func (m *Manager) Client() *dockerclient.Client { return m.client }

// BridgeGatewayIP returns the IPv4 gateway of Docker's default bridge network —
// the address lifecycle containers (running inside Docker) use to reach the host.
// Falls back to "172.17.0.1" if the network cannot be inspected.
func (m *Manager) BridgeGatewayIP(ctx context.Context) string {
	info, err := m.client.NetworkInspect(ctx, "bridge", dockernetwork.InspectOptions{})
	if err == nil {
		for _, cfg := range info.IPAM.Config {
			if cfg.Gateway != "" {
				return cfg.Gateway
			}
		}
	}
	return "172.17.0.1"
}

// Run starts the 30-second health-poll loop and blocks until ctx is cancelled.
// Mirrors k8s.Controller.Run.
func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.checkAll(ctx)
		}
	}
}

func (m *Manager) checkAll(ctx context.Context) {
	rows, err := m.pool.Query(ctx, `
		SELECT da.id::text, d.name, da.name
		FROM deployment_apps da
		JOIN deployments d ON d.id = da.deployment_id
		WHERE da.target_kind = 'local_docker' AND da.status = 'running'
	`)
	if err != nil {
		slog.Error("docker: query running container apps", "error", err)
	} else {
		for rows.Next() {
			var appID, stackName, appName string
			if err := rows.Scan(&appID, &stackName, &appName); err != nil {
				slog.Error("docker: scan app row", "error", err)
				continue
			}
			m.checkContainer(ctx, appID, appName, "deployment", appContainerName(stackName, appName))
		}
		rows.Close()
	}

	resRows, err := m.pool.Query(ctx, `
		SELECT id::text, name FROM provisioned_resources
		WHERE provider = 'local' AND resource_type = 'docker'
		  AND status = 'applied' AND dismissed_at IS NULL
	`)
	if err != nil {
		slog.Error("docker: query local docker resources", "error", err)
		return
	}
	defer resRows.Close()

	for resRows.Next() {
		var resID, name string
		if err := resRows.Scan(&resID, &name); err != nil {
			slog.Error("docker: scan resource row", "error", err)
			continue
		}
		m.checkContainer(ctx, resID, name, "resource", containerName(name))
	}
}

func (m *Manager) checkContainer(ctx context.Context, id, name, kind, cname string) {
	inspect, err := m.client.ContainerInspect(ctx, cname)
	h := classify(id, name, kind, cname, inspect, err)
	m.hub.Publish(sse.Event{Type: "container.health", Payload: h})
	m.healer.handle(ctx, h)
}

// sanitizeDockerName lower-cases and replaces underscores with hyphens, the
// shared transform behind containerName, stackNetworkName, and appContainerName.
func sanitizeDockerName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

// containerName returns the deterministic Docker container name for a
// standalone "local" Resource: "autodevops-<name>" with underscores→hyphens
// and lower-cased.
func containerName(name string) string {
	return "autodevops-" + sanitizeDockerName(name)
}

// stackNetworkName returns the deterministic per-stack Docker bridge network
// name: "autodevops-<stackName>" with underscores→hyphens and lower-cased.
// Shared by every app in the stack that targets local Docker.
func stackNetworkName(stackName string) string {
	return "autodevops-" + sanitizeDockerName(stackName)
}

// appContainerName returns the deterministic Docker container name for an app
// within a stack: "<stackName>-<appName>" with underscores→hyphens and
// lower-cased. Docker container names are global, so the stack name keeps
// same-named apps in different stacks from colliding; the app's network alias
// (just appName, set at container-create time) is what other apps in the
// stack use for short-name resolution.
func appContainerName(stackName, appName string) string {
	return sanitizeDockerName(stackName) + "-" + sanitizeDockerName(appName)
}

// classify converts a ContainerInspect result into a ContainerHealth value.
// Pure function — no I/O. id is the app or resource UUID depending on kind
// ("deployment" | "resource"); cname is the container's Docker name, already
// resolved by the caller (appContainerName for apps, containerName for
// resources).
func classify(id, name, kind, cname string, inspect dockertypes.ContainerJSON, inspectErr error) ContainerHealth {
	h := ContainerHealth{Name: name, Kind: kind, ContainerName: cname}
	if kind == "resource" {
		h.ResourceID = id
	} else {
		h.AppID = id
	}
	if inspectErr != nil {
		if dockerclient.IsErrNotFound(inspectErr) {
			h.Status = "missing"
			h.Reason = "container not found"
		} else {
			h.Status = "unknown"
			h.Reason = inspectErr.Error()
		}
		return h
	}
	if len(inspect.ID) >= 12 {
		h.ContainerID = inspect.ID[:12]
	}
	switch inspect.State.Status {
	case "running":
		h.Status = "running"
	case "exited":
		h.Status = "stopped"
		h.Reason = fmt.Sprintf("exit code %d", inspect.State.ExitCode)
	case "dead":
		h.Status = "failed"
		h.Reason = inspect.State.Error
	default:
		h.Status = inspect.State.Status
	}
	return h
}
