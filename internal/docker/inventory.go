package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// ContainerInfo is a lightweight snapshot of a Docker container for the host
// inventory listing — one entry per container regardless of whether AutoDevOps
// created it.
type ContainerInfo struct {
	ID        string
	Name      string            // Names[0] with leading "/" stripped
	Image     string
	State     string            // "running","exited","created","restarting","paused","removing","dead"
	Status    string            // human string e.g. "Up 2 hours"
	CreatedAt time.Time
	Labels    map[string]string
}

// ListContainers returns a snapshot of ALL containers on the Docker host
// (running and stopped) for the Local Resources host-inventory view.
func (m *Manager) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	list, err := m.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}
	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		name := c.ID
		if len(name) > 12 {
			name = name[:12]
		}
		if len(c.Names) > 0 {
			n := c.Names[0]
			if len(n) > 0 && n[0] == '/' {
				n = n[1:]
			}
			name = n
		}
		out = append(out, ContainerInfo{
			ID:        c.ID,
			Name:      name,
			Image:     c.Image,
			State:     c.State,
			Status:    c.Status,
			CreatedAt: time.Unix(c.Created, 0).UTC(),
			Labels:    c.Labels,
		})
	}
	return out, nil
}

// ContainerLogs returns the last tail lines of combined stdout+stderr for the
// given container ID (or name). If tail <= 0 the default of 300 lines is used.
// Docker multiplexes stdout/stderr in a framed format for non-TTY containers;
// ContainerLogs demultiplexes them into a single combined string.
func (m *Manager) ContainerLogs(ctx context.Context, id string, tail int) (string, error) {
	if tail <= 0 {
		tail = 300
	}
	info, err := m.client.ContainerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("docker: inspect container %s: %w", id, err)
	}
	rc, err := m.client.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return "", fmt.Errorf("docker: logs for container %s: %w", id, err)
	}
	defer rc.Close()

	var buf bytes.Buffer
	if info.Config != nil && info.Config.Tty {
		_, err = io.Copy(&buf, rc)
	} else {
		// Docker multiplexes stdout/stderr into 8-byte-header frames for
		// non-TTY containers; stdcopy demuxes both streams into the same
		// buffer so the result matches what "docker logs" prints.
		_, err = stdcopy.StdCopy(&buf, &buf, rc)
	}
	if err != nil {
		return "", fmt.Errorf("docker: read logs for container %s: %w", id, err)
	}
	return buf.String(), nil
}

// RestartContainer restarts the container with the given ID or name — mirrors
// the "docker restart" command for the host inventory's user-initiated restart.
func (m *Manager) RestartContainer(ctx context.Context, id string) error {
	if err := m.client.ContainerRestart(ctx, id, container.StopOptions{}); err != nil {
		return fmt.Errorf("docker: restart container %s: %w", id, err)
	}
	return nil
}
