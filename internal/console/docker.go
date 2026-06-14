package console

import (
	"bytes"
	"context"
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// dockerShell is the command run for an interactive Open() session and as the
// interpreter for Exec()'s one-shot commands — all images used by local
// Docker resources (nginx, redis, postgres, busybox, etc.) ship /bin/sh.
const dockerShell = "/bin/sh"

// dockerConnector opens shells / runs commands on standalone local Docker
// containers (provider='local', resource_type='docker') via the Docker exec
// API — no SSH, no cloud credentials.
type dockerConnector struct {
	registry *Registry
}

// containerID reads the short container ID written to outputs by
// CreateContainer (internal/docker/deploy.go).
func (c *dockerConnector) containerID(res Resource) (string, error) {
	id, _ := res.Outputs["container_id"].(string)
	if id == "" {
		return "", fmt.Errorf("console: resource is missing container_id output — apply it first")
	}
	return id, nil
}

// dockerSession wraps a TTY exec session as a Session. Resize uses
// context.Background() since it's called after Open returns (the request
// context that started the session is long gone by then).
type dockerSession struct {
	client *dockerclient.Client
	execID string
	resp   types.HijackedResponse
}

func (s *dockerSession) Read(p []byte) (int, error)  { return s.resp.Reader.Read(p) }
func (s *dockerSession) Write(p []byte) (int, error) { return s.resp.Conn.Write(p) }

func (s *dockerSession) Resize(rows, cols uint16) error {
	return s.client.ContainerExecResize(context.Background(), s.execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

func (s *dockerSession) Close() error {
	s.resp.Close()
	return nil
}

func (c *dockerConnector) Open(ctx context.Context, res Resource, rows, cols uint16) (Session, error) {
	id, err := c.containerID(res)
	if err != nil {
		return nil, err
	}
	cli, err := c.registry.dockerClient()
	if err != nil {
		return nil, err
	}

	created, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{dockerShell},
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		ConsoleSize:  &[2]uint{uint(rows), uint(cols)},
	})
	if err != nil {
		return nil, fmt.Errorf("console: docker exec create: %w", err)
	}

	hijacked, err := cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return nil, fmt.Errorf("console: docker exec attach: %w", err)
	}

	return &dockerSession{client: cli, execID: created.ID, resp: hijacked}, nil
}

func (c *dockerConnector) Exec(ctx context.Context, res Resource, command string) (ExecResult, error) {
	id, err := c.containerID(res)
	if err != nil {
		return ExecResult{}, err
	}
	cli, err := c.registry.dockerClient()
	if err != nil {
		return ExecResult{}, err
	}

	created, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{dockerShell, "-c", command},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("console: docker exec create: %w", err)
	}

	hijacked, err := cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, fmt.Errorf("console: docker exec attach: %w", err)
	}
	defer hijacked.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, hijacked.Reader); err != nil {
		return ExecResult{}, fmt.Errorf("console: docker exec read output: %w", err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return ExecResult{}, fmt.Errorf("console: docker exec inspect: %w", err)
	}

	return ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: inspect.ExitCode}, nil
}
