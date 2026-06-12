package console

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const sshDialTimeout = 15 * time.Second

// ephemeralKeyPair generates a throwaway ed25519 keypair held only in memory
// for the lifetime of one console session/exec call. It is never persisted —
// same "relay, don't generate" boundary as handleGenerateSSHKey
// (internal/api/sshkey.go), except here even the private key never leaves
// the process as PEM.
func ephemeralKeyPair() (ssh.Signer, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("console: generate ephemeral key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, "", fmt.Errorf("console: wrap ephemeral key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", fmt.Errorf("console: derive ephemeral public key: %w", err)
	}
	return signer, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

// sshClientConfig builds an SSH client config for the ephemeral key. Host key
// verification is intentionally skipped: the ephemeral key plus the
// cloud-provider-authenticated push of its public half (EC2 Instance Connect
// / Azure Run Command) is the trust boundary here — AutoDevOps has no prior
// host key to pin, and reaching this point already required valid cloud
// account credentials.
func sshClientConfig(user string, signer ssh.Signer) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         sshDialTimeout,
	}
}

// sshDialContext dials addr and completes the SSH handshake, honoring ctx
// cancellation during the TCP connect (golang.org/x/crypto/ssh has no
// context-aware Dial of its own).
func sshDialContext(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: sshDialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// sshPTYSession is a Session backed by an interactive SSH shell with a PTY
// allocated — used by the EC2 and Azure VM connectors for
// POST /resources/{id}/console/sessions.
type sshPTYSession struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

// dialSSHShell opens addr as user with signer, allocates a PTY of rows x
// cols, and starts an interactive shell. The returned session's Read/Write
// stream the PTY's combined stdout+stderr / stdin.
func dialSSHShell(ctx context.Context, addr, user string, signer ssh.Signer, rows, cols uint16) (*sshPTYSession, error) {
	client, err := sshDialContext(ctx, addr, sshClientConfig(user, signer))
	if err != nil {
		return nil, fmt.Errorf("console: ssh dial %s: %w", addr, err)
	}

	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("console: ssh new session: %w", err)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", int(rows), int(cols), modes); err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("console: request pty: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("console: stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("console: stdout pipe: %w", err)
	}

	if err := session.Shell(); err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("console: start shell: %w", err)
	}

	return &sshPTYSession{client: client, session: session, stdin: stdin, stdout: stdout}, nil
}

func (s *sshPTYSession) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *sshPTYSession) Write(p []byte) (int, error) { return s.stdin.Write(p) }

func (s *sshPTYSession) Resize(rows, cols uint16) error {
	return s.session.WindowChange(int(rows), int(cols))
}

func (s *sshPTYSession) Close() error {
	s.session.Close()
	return s.client.Close()
}

// isAuthFailure reports whether err is an SSH authentication rejection (as
// opposed to a dial/network failure). golang.org/x/crypto/ssh has no typed
// error for this case — "ssh: unable to authenticate" is the stable message
// substring returned by clientAuthenticate when no auth method succeeds.
func isAuthFailure(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unable to authenticate")
}

// sshExec dials addr as user with signer, runs command without a PTY, and
// captures stdout/stderr/exit-code — used by the EC2 and Azure VM connectors
// for POST /resources/{id}/console/exec.
func sshExec(ctx context.Context, addr, user string, signer ssh.Signer, command string) (ExecResult, error) {
	client, err := sshDialContext(ctx, addr, sshClientConfig(user, signer))
	if err != nil {
		return ExecResult{}, fmt.Errorf("console: ssh dial %s: %w", addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return ExecResult{}, fmt.Errorf("console: ssh new session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	exitCode := 0
	if err := session.Run(command); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitStatus()
		} else {
			return ExecResult{}, fmt.Errorf("console: run command: %w", err)
		}
	}
	return ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, nil
}
