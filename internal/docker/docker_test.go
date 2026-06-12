package docker

import (
	"errors"
	"strings"
	"testing"

	dockertypes "github.com/docker/docker/api/types"
)

func TestContainerName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"my-app", "autodevops-my-app"},
		{"my_app", "autodevops-my-app"},
		{"MyApp", "autodevops-myapp"},
		{"MY_SERVICE", "autodevops-my-service"},
	}
	for _, tc := range cases {
		got := containerName(tc.in)
		if got != tc.want {
			t.Errorf("containerName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHostPort(t *testing.T) {
	// Deterministic: same name always produces the same port.
	p1 := hostPort("my-app")
	p2 := hostPort("my-app")
	if p1 != p2 {
		t.Errorf("hostPort not deterministic: %d != %d", p1, p2)
	}
	// Port is in the expected range.
	if p1 < 8100 || p1 >= 9000 {
		t.Errorf("hostPort = %d, want 8100–8999", p1)
	}
	// Different names produce different ports (not guaranteed for all inputs but
	// holds for any realistic deployment count — verify a few common names).
	ports := map[uint32]string{}
	for _, name := range []string{"api", "frontend", "backend", "worker", "scheduler"} {
		p := hostPort(name)
		if prev, collision := ports[p]; collision {
			t.Errorf("hostPort collision: %q and %q both map to %d", name, prev, p)
		}
		ports[p] = name
	}
}

func TestCompose(t *testing.T) {
	out := Compose("my-app", "localhost:5001/my-app:abc123", 8200)

	checks := []string{
		"my-app:",
		"image: localhost:5001/my-app:abc123",
		"container_name: autodevops-my-app",
		"8200:8080",
		"PORT=8080",
		"autodevops.io/managed-by: autodevops",
		"autodevops.io/deployment: my-app",
		"networks:\n  autodevops:",
		"external: true",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("Compose output missing %q\n---\n%s", want, out)
		}
	}
}

func TestErrDockerNotFoundMessage(t *testing.T) {
	err := &ErrDockerNotFound{Cause: errors.New("connection refused")}
	msg := err.Error()

	for _, want := range []string{"connection refused", "Linux", "Windows", "macOS", "https://"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ErrDockerNotFound.Error() missing %q\n%s", want, msg)
		}
	}
	// Unwrap must return the original cause.
	if !errors.Is(err, err.Cause) {
		t.Error("ErrDockerNotFound.Unwrap() should return Cause")
	}
}

func TestClassifyRunning(t *testing.T) {
	h := classify("dep1", "my-app", "deployment", dockertypes.ContainerJSON{
		ContainerJSONBase: &dockertypes.ContainerJSONBase{
			ID:    "abc123def456789",
			State: &dockertypes.ContainerState{Status: "running"},
		},
	}, nil)
	if h.Status != "running" {
		t.Errorf("classify running = %q, want running", h.Status)
	}
	if h.ContainerID != "abc123def456" {
		t.Errorf("ContainerID = %q, want abc123def456", h.ContainerID)
	}
	if h.DeploymentID != "dep1" || h.Kind != "deployment" {
		t.Errorf("DeploymentID/Kind = %q/%q, want dep1/deployment", h.DeploymentID, h.Kind)
	}
}

func TestClassifyExited(t *testing.T) {
	h := classify("dep1", "my-app", "deployment", dockertypes.ContainerJSON{
		ContainerJSONBase: &dockertypes.ContainerJSONBase{
			ID:    "abc123def456789",
			State: &dockertypes.ContainerState{Status: "exited", ExitCode: 1},
		},
	}, nil)
	if h.Status != "stopped" {
		t.Errorf("classify exited = %q, want stopped", h.Status)
	}
	if !strings.Contains(h.Reason, "1") {
		t.Errorf("classify exited reason = %q, want exit code in message", h.Reason)
	}
}

func TestClassifyNotFound(t *testing.T) {
	notFoundErr := &notFoundError{}
	h := classify("dep1", "my-app", "deployment", dockertypes.ContainerJSON{}, notFoundErr)
	if h.Status != "missing" {
		t.Errorf("classify not found = %q, want missing", h.Status)
	}
}

func TestClassifyResourceKind(t *testing.T) {
	h := classify("res1", "my-resource", "resource", dockertypes.ContainerJSON{
		ContainerJSONBase: &dockertypes.ContainerJSONBase{
			ID:    "abc123def456789",
			State: &dockertypes.ContainerState{Status: "running"},
		},
	}, nil)
	if h.ResourceID != "res1" || h.Kind != "resource" {
		t.Errorf("ResourceID/Kind = %q/%q, want res1/resource", h.ResourceID, h.Kind)
	}
	if h.DeploymentID != "" {
		t.Errorf("DeploymentID = %q, want empty for resource kind", h.DeploymentID)
	}
}

// notFoundError satisfies the errdefs.IsNotFound interface used by the Docker
// SDK: the interface requires NotFound() with no return value.
type notFoundError struct{}

func (e *notFoundError) Error() string { return "No such container" }
func (e *notFoundError) NotFound()     {}
