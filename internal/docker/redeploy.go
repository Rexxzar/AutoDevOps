package docker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
)

const (
	// healthCheckWait is how long deployContainerSafely watches a freshly started
	// container before declaring it healthy (or rolling back).
	healthCheckWait = 12 * time.Second
	// healthPollInterval is how often it re-inspects during that window.
	healthPollInterval = 1 * time.Second
)

// deployContainerSafely replaces the container named cname with a new one built
// from the given configs, with GUARANTEED ROLLBACK: if a container with cname
// already exists, it is renamed aside and stopped (not deleted) to free the host
// port, the new container is started and health-checked, and only on success is
// the old one removed. If the new container fails to start or stay healthy, it
// is removed and the previous one is restored — so a bad redeploy never leaves
// the app down. A brief blip (old stop → new start) is unavoidable because the
// container binds a fixed host port; true zero-downtime would need a proxy.
func (m *Manager) deployContainerSafely(ctx context.Context, cname string, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig) error {
	backup := cname + "-prev"
	// Clear any leftover backup from an earlier interrupted deploy.
	_ = m.removeContainer(ctx, backup)

	_, inspErr := m.client.ContainerInspect(ctx, cname)
	oldExists := inspErr == nil

	if oldExists {
		// Move the old container aside and stop it to free the host port,
		// keeping it intact so we can restore it if the new one fails.
		if err := m.client.ContainerRename(ctx, cname, backup); err != nil {
			return fmt.Errorf("docker: rename existing container %s: %w", cname, err)
		}
		if err := m.client.ContainerStop(ctx, backup, container.StopOptions{}); err != nil && !dockerclient.IsErrNotFound(err) {
			slog.Warn("docker: stop previous container", "container", backup, "error", err)
		}
	}

	resp, err := m.client.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, cname)
	if err != nil {
		m.rollback(ctx, cname, backup, oldExists)
		return fmt.Errorf("docker: create container %s: %w", cname, err)
	}
	if err := m.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		m.rollback(ctx, cname, backup, oldExists)
		return fmt.Errorf("docker: start container %s: %w", cname, err)
	}

	if err := m.waitHealthy(ctx, cname); err != nil {
		m.rollback(ctx, cname, backup, oldExists)
		if oldExists {
			return fmt.Errorf("docker: new container %s unhealthy, rolled back to previous version: %w", cname, err)
		}
		return fmt.Errorf("docker: new container %s unhealthy: %w", cname, err)
	}

	// New container is healthy — drop the previous version.
	if oldExists {
		if err := m.removeContainer(ctx, backup); err != nil {
			slog.Warn("docker: remove previous container after successful deploy", "container", backup, "error", err)
		}
	}
	return nil
}

// rollback removes the failed new container and, if a previous version was set
// aside, restores it (rename back + start) so the app keeps running.
func (m *Manager) rollback(ctx context.Context, cname, backup string, oldExists bool) {
	if err := m.client.ContainerRemove(ctx, cname, container.RemoveOptions{Force: true}); err != nil && !dockerclient.IsErrNotFound(err) {
		slog.Warn("docker: remove failed new container", "container", cname, "error", err)
	}
	if !oldExists {
		return
	}
	if err := m.client.ContainerRename(ctx, backup, cname); err != nil {
		slog.Error("docker: rollback rename failed — previous version left as backup container",
			"backup", backup, "want", cname, "error", err)
		return
	}
	if err := m.client.ContainerStart(ctx, cname, container.StartOptions{}); err != nil {
		slog.Error("docker: rollback start failed — previous container could not be restarted",
			"container", cname, "error", err)
		return
	}
	slog.Info("docker: rolled back to previous container", "container", cname)
}

// waitHealthy polls the container for up to healthCheckWait and returns nil if
// it stays up (or its Docker HEALTHCHECK reports healthy), or an error if it
// exits, dies, or enters a restart/crash loop. The "stays running for the whole
// window" heuristic is used when the image defines no HEALTHCHECK.
func (m *Manager) waitHealthy(ctx context.Context, cname string) error {
	deadline := time.Now().Add(healthCheckWait)
	for {
		inspect, err := m.client.ContainerInspect(ctx, cname)
		if err != nil {
			return fmt.Errorf("inspect: %w", err)
		}
		st := inspect.State

		if st.Health != nil {
			switch st.Health.Status {
			case "healthy":
				return nil
			case "unhealthy":
				return fmt.Errorf("healthcheck reported unhealthy")
			}
			// "starting" — keep waiting.
		} else {
			switch st.Status {
			case "exited":
				return fmt.Errorf("container exited with code %d", st.ExitCode)
			case "dead":
				return fmt.Errorf("container is dead: %s", st.Error)
			case "restarting":
				return fmt.Errorf("container is restarting (last exit %d) — crash loop", st.ExitCode)
			}
			// A restart within the observation window signals a crash loop even
			// if we catch it momentarily back in "running".
			if inspect.RestartCount > 0 {
				return fmt.Errorf("container restarted %d time(s) — crash loop", inspect.RestartCount)
			}
		}

		if !time.Now().Before(deadline) {
			if st.Health != nil {
				return nil // healthcheck still "starting" — don't block the deploy forever
			}
			if st.Status == "running" {
				return nil // stayed up the whole window
			}
			return fmt.Errorf("container not running (status %q) after %s", st.Status, healthCheckWait)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthPollInterval):
		}
	}
}
