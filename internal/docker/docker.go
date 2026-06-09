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
	dockerclient "github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ContainerHealth is the classified state of a managed container, published
// via SSE as "container.health" after every poll tick — mirrors k8s.PodHealth.
type ContainerHealth struct {
	DeploymentID string `json:"deployment_id"`
	Name         string `json:"name"` // deployment name, not container name
	ContainerID  string `json:"container_id,omitempty"`
	Status       string `json:"status"` // "running" | "stopped" | "failed" | "missing" | "unknown"
	Reason       string `json:"reason,omitempty"`
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
		SELECT id::text, name FROM deployments
		WHERE target_type = 'container' AND status = 'running'
	`)
	if err != nil {
		slog.Error("docker: query running container deployments", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var depID, name string
		if err := rows.Scan(&depID, &name); err != nil {
			slog.Error("docker: scan deployment row", "error", err)
			continue
		}
		m.checkContainer(ctx, depID, name)
	}
}

func (m *Manager) checkContainer(ctx context.Context, deploymentID, name string) {
	inspect, err := m.client.ContainerInspect(ctx, containerName(name))
	h := classify(deploymentID, name, inspect, err)
	m.hub.Publish(sse.Event{Type: "container.health", Payload: h})
	m.healer.handle(ctx, h)
}

// containerName returns the deterministic Docker container name for a deployment:
// "autodevops-<name>" with underscores→hyphens and lower-cased.
func containerName(deploymentName string) string {
	return "autodevops-" + strings.ToLower(strings.ReplaceAll(deploymentName, "_", "-"))
}

// classify converts a ContainerInspect result into a ContainerHealth value.
// Pure function — no I/O.
func classify(deploymentID, name string, inspect dockertypes.ContainerJSON, inspectErr error) ContainerHealth {
	h := ContainerHealth{DeploymentID: deploymentID, Name: name}
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
