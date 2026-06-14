// Package sshutil provides ephemeral-ed25519-key SSH primitives shared by
// internal/console (Resource Console EC2/Azure VM connectors, see the design notes)
// Dev Notes "Resource Console design") and internal/deploy (cloud_vm SSH
// deployer, see Phase 5b). Keeping these here avoids a fourth duplicate of
// this logic across internal/ packages.
package sshutil

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

const DialTimeout = 15 * time.Second

// EphemeralKeyPair generates a throwaway ed25519 keypair held only in memory
// for the lifetime of one console session/exec call or deploy. It is never
// persisted — same "relay, don't generate" boundary as handleGenerateSSHKey
// (internal/api/sshkey.go), except here even the private key never leaves the
// process as PEM.
func EphemeralKeyPair() (ssh.Signer, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("sshutil: generate ephemeral key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, "", fmt.Errorf("sshutil: wrap ephemeral key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", fmt.Errorf("sshutil: derive ephemeral public key: %w", err)
	}
	return signer, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

// ClientConfig builds an SSH client config for the ephemeral key. Host key
// verification is intentionally skipped: the ephemeral key plus the
// cloud-provider-authenticated push of its public half (EC2 Instance Connect
// / Azure Run Command) is the trust boundary here — AutoDevOps has no prior
// host key to pin, and reaching this point already required valid cloud
// account credentials.
func ClientConfig(user string, signer ssh.Signer) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         DialTimeout,
	}
}

// DialContext dials addr and completes the SSH handshake, honoring ctx
// cancellation during the TCP connect (golang.org/x/crypto/ssh has no
// context-aware Dial of its own).
func DialContext(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: DialTimeout}
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

// PTYSession is an io.ReadWriteCloser backed by an interactive SSH shell with
// a PTY allocated — used by the EC2 and Azure VM console connectors for
// POST /resources/{id}/console/sessions.
type PTYSession struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

// DialShell opens addr as user with signer, allocates a PTY of rows x cols,
// and starts an interactive shell. The returned session's Read/Write stream
// the PTY's combined stdout+stderr / stdin.
func DialShell(ctx context.Context, addr, user string, signer ssh.Signer, rows, cols uint16) (*PTYSession, error) {
	client, err := DialContext(ctx, addr, ClientConfig(user, signer))
	if err != nil {
		return nil, fmt.Errorf("sshutil: ssh dial %s: %w", addr, err)
	}

	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("sshutil: ssh new session: %w", err)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", int(rows), int(cols), modes); err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("sshutil: request pty: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("sshutil: stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("sshutil: stdout pipe: %w", err)
	}

	if err := session.Shell(); err != nil {
		session.Close()
		client.Close()
		return nil, fmt.Errorf("sshutil: start shell: %w", err)
	}

	return &PTYSession{client: client, session: session, stdin: stdin, stdout: stdout}, nil
}

func (s *PTYSession) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *PTYSession) Write(p []byte) (int, error) { return s.stdin.Write(p) }

func (s *PTYSession) Resize(rows, cols uint16) error {
	return s.session.WindowChange(int(rows), int(cols))
}

func (s *PTYSession) Close() error {
	s.session.Close()
	return s.client.Close()
}

// IsAuthFailure reports whether err is an SSH authentication rejection (as
// opposed to a dial/network failure). golang.org/x/crypto/ssh has no typed
// error for this case — "ssh: unable to authenticate" is the stable message
// substring returned by clientAuthenticate when no auth method succeeds.
func IsAuthFailure(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unable to authenticate")
}

// ExecResult is the result of a one-shot command run via Exec.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Exec dials addr as user with signer, runs command without a PTY, and
// captures stdout/stderr/exit-code — used by the EC2 and Azure VM console
// connectors for POST /resources/{id}/console/exec.
func Exec(ctx context.Context, addr, user string, signer ssh.Signer, command string) (ExecResult, error) {
	client, err := DialContext(ctx, addr, ClientConfig(user, signer))
	if err != nil {
		return ExecResult{}, fmt.Errorf("sshutil: ssh dial %s: %w", addr, err)
	}
	defer client.Close()

	return ExecOnClient(client, command, nil)
}

// ExecOnClient runs command without a PTY on an already-connected client,
// optionally streaming stdin, and captures stdout/stderr/exit-code. Unlike
// Exec, the caller owns client's lifecycle — used by the cloud_vm SSH
// deployer (internal/deploy) to run multiple commands (docker load, docker
// run) over one connection.
func ExecOnClient(client *ssh.Client, command string, stdin io.Reader) (ExecResult, error) {
	session, err := client.NewSession()
	if err != nil {
		return ExecResult{}, fmt.Errorf("sshutil: ssh new session: %w", err)
	}
	defer session.Close()

	if stdin != nil {
		session.Stdin = stdin
	}

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	exitCode := 0
	if err := session.Run(command); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitStatus()
		} else {
			return ExecResult{}, fmt.Errorf("sshutil: run command: %w", err)
		}
	}
	return ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, nil
}
