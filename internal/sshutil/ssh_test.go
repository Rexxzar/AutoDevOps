package sshutil

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServer starts a minimal in-process SSH server on a random
// localhost port that accepts only authorizedKey for auth. exec requests are
// answered by execHandler; shell sessions become an echo loop (anything
// written to the channel is written back) — enough to exercise Exec and
// DialShell/PTYSession without a real EC2/Azure target.
func startTestSSHServer(t *testing.T, authorizedKey ssh.PublicKey, execHandler func(cmd string) (stdout, stderr string, exitCode uint32)) string {
	t.Helper()

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("wrap host key: %v", err)
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), authorizedKey.Marshal()) {
				return nil, fmt.Errorf("unauthorized key")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go acceptTestSSHConn(conn, config, execHandler)
		}
	}()

	return listener.Addr().String()
}

func acceptTestSSHConn(conn net.Conn, config *ssh.ServerConfig, execHandler func(cmd string) (string, string, uint32)) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go handleTestSSHSession(channel, requests, execHandler)
	}
}

func handleTestSSHSession(channel ssh.Channel, requests <-chan *ssh.Request, execHandler func(cmd string) (string, string, uint32)) {
	defer channel.Close()
	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			ssh.Unmarshal(req.Payload, &payload)
			req.Reply(true, nil)

			if execHandler != nil {
				stdout, stderr, code := execHandler(payload.Command)
				channel.Write([]byte(stdout))
				channel.Stderr().Write([]byte(stderr))
				channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: code}))
			}
			return
		case "shell":
			req.Reply(true, nil)
			go func() {
				buf := make([]byte, 1024)
				for {
					n, err := channel.Read(buf)
					if n > 0 {
						channel.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}()
		case "pty-req", "window-change", "env":
			req.Reply(true, nil)
		default:
			req.Reply(false, nil)
		}
	}
}

func TestEphemeralKeyPair(t *testing.T) {
	signer, authorizedKey, err := EphemeralKeyPair()
	if err != nil {
		t.Fatalf("EphemeralKeyPair: %v", err)
	}
	if signer.PublicKey() == nil {
		t.Fatal("signer has no public key")
	}

	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		t.Fatalf("parse authorized key %q: %v", authorizedKey, err)
	}
	if !bytes.Equal(parsed.Marshal(), signer.PublicKey().Marshal()) {
		t.Fatal("authorized key does not match signer's public key")
	}
}

func TestExec(t *testing.T) {
	signer, _, err := EphemeralKeyPair()
	if err != nil {
		t.Fatalf("EphemeralKeyPair: %v", err)
	}

	addr := startTestSSHServer(t, signer.PublicKey(), func(cmd string) (string, string, uint32) {
		if cmd == "fail" {
			return "", "boom\n", 1
		}
		return "hello " + cmd + "\n", "", 0
	})

	result, err := Exec(context.Background(), addr, "testuser", signer, "world")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Stdout != "hello world\n" || result.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	result, err = Exec(context.Background(), addr, "testuser", signer, "fail")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode != 1 || result.Stderr != "boom\n" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDialShell(t *testing.T) {
	signer, _, err := EphemeralKeyPair()
	if err != nil {
		t.Fatalf("EphemeralKeyPair: %v", err)
	}

	addr := startTestSSHServer(t, signer.PublicKey(), nil)

	sess, err := DialShell(context.Background(), addr, "testuser", signer, 24, 80)
	if err != nil {
		t.Fatalf("DialShell: %v", err)
	}
	defer sess.Close()

	if _, err := sess.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(sess, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("got %q, want %q", buf, "ping")
	}

	if err := sess.Resize(30, 100); err != nil {
		t.Fatalf("resize: %v", err)
	}
}
