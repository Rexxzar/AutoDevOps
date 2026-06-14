package console

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// k8sContainerName is the single container name used by buildResourceDeployment
// (internal/k8s/manifests.go) for standalone "local" K8s resources.
const k8sContainerName = "app"

// k8sShell is the command run for an interactive Open() session and as the
// interpreter for Exec()'s one-shot commands.
const k8sShell = "/bin/sh"

// k8sConnector opens shells / runs commands on pods backing standalone local
// K8s workloads (provider='local', resource_type='k8s') via client-go's
// remotecommand (SPDY exec) — no SSH, no cloud credentials.
type k8sConnector struct {
	registry *Registry
}

// target resolves the namespace and the name of a Running pod for res, using
// the namespace recorded in outputs (KubeDeployResource) and the
// "app=<name>" selector buildResourceDeployment assigns to its pod template.
func (c *k8sConnector) target(ctx context.Context, clientset kubernetes.Interface, res Resource) (namespace, podName string, err error) {
	namespace, _ = res.Outputs["namespace"].(string)
	if namespace == "" || res.Name == "" {
		return "", "", fmt.Errorf("console: resource is missing namespace output — apply it first")
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + res.Name,
	})
	if err != nil {
		return "", "", fmt.Errorf("console: list pods in %s: %w", namespace, err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return namespace, pod.Name, nil
		}
	}
	return "", "", fmt.Errorf("console: no running pod found for resource %s in namespace %s", res.Name, namespace)
}

// sizeQueue adapts a channel of terminal sizes to remotecommand.TerminalSizeQueue.
type sizeQueue struct {
	ch chan remotecommand.TerminalSize
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

// k8sSession bridges a blocking remotecommand.StreamWithContext call (run in
// a goroutine) to the Session interface via io.Pipe. cancel tears down the
// stream on Close; resize is closed on Close so the executor's resize-relay
// goroutine exits too.
type k8sSession struct {
	cancel  context.CancelFunc
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	resize  chan remotecommand.TerminalSize

	mu     sync.Mutex
	closed bool
}

func (s *k8sSession) Read(p []byte) (int, error)  { return s.stdoutR.Read(p) }
func (s *k8sSession) Write(p []byte) (int, error) { return s.stdinW.Write(p) }

func (s *k8sSession) Resize(rows, cols uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	select {
	case s.resize <- remotecommand.TerminalSize{Width: cols, Height: rows}:
	default:
	}
	return nil
}

func (s *k8sSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancel()
	s.stdinW.Close()
	s.stdoutR.Close()
	close(s.resize)
	return nil
}

func (c *k8sConnector) Open(ctx context.Context, res Resource, rows, cols uint16) (Session, error) {
	clientset, restCfg, err := c.registry.k8sClientset()
	if err != nil {
		return nil, err
	}
	namespace, podName, err := c.target(ctx, clientset, res)
	if err != nil {
		return nil, err
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: k8sContainerName,
			Command:   []string{k8sShell},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(restCfg, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("console: build k8s exec: %w", err)
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	// The stream must outlive this request's context, so it gets its own
	// detached, cancellable context — torn down via Session.Close.
	streamCtx, cancel := context.WithCancel(context.Background())

	resizeCh := make(chan remotecommand.TerminalSize, 1)
	resizeCh <- remotecommand.TerminalSize{Width: cols, Height: rows}

	go func() {
		err := executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{
			Stdin:             stdinR,
			Stdout:            stdoutW,
			Stderr:            stdoutW,
			Tty:               true,
			TerminalSizeQueue: &sizeQueue{ch: resizeCh},
		})
		stdoutW.CloseWithError(err)
	}()

	return &k8sSession{cancel: cancel, stdinW: stdinW, stdoutR: stdoutR, resize: resizeCh}, nil
}

func (c *k8sConnector) Exec(ctx context.Context, res Resource, command string) (ExecResult, error) {
	clientset, restCfg, err := c.registry.k8sClientset()
	if err != nil {
		return ExecResult{}, err
	}
	namespace, podName, err := c.target(ctx, clientset, res)
	if err != nil {
		return ExecResult{}, err
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: k8sContainerName,
			Command:   []string{k8sShell, "-c", command},
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(restCfg, "POST", req.URL())
	if err != nil {
		return ExecResult{}, fmt.Errorf("console: build k8s exec: %w", err)
	}

	var stdout, stderr bytes.Buffer
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	exitCode := 0
	if streamErr != nil {
		var codeErr utilexec.CodeExitError
		if errors.As(streamErr, &codeErr) {
			exitCode = codeErr.Code
		} else {
			return ExecResult{}, fmt.Errorf("console: k8s exec: %w", streamErr)
		}
	}

	return ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, nil
}
