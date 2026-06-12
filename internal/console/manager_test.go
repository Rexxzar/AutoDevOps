package console

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSession is an in-memory Session double for exercising Manager without
// a real SSH/cloud connector. Its read side is an io.Pipe so pump() blocks
// until emit() is called or Close() is called (which yields io.EOF).
type fakeSession struct {
	pr *io.PipeReader
	pw *io.PipeWriter

	writeMu  sync.Mutex
	written  bytes.Buffer
	resizeCh chan [2]uint16
	closed   atomic.Bool
}

func newFakeSession() *fakeSession {
	pr, pw := io.Pipe()
	return &fakeSession{pr: pr, pw: pw, resizeCh: make(chan [2]uint16, 4)}
}

func (f *fakeSession) Read(p []byte) (int, error) { return f.pr.Read(p) }

func (f *fakeSession) Write(p []byte) (int, error) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	return f.written.Write(p)
}

func (f *fakeSession) Resize(rows, cols uint16) error {
	f.resizeCh <- [2]uint16{rows, cols}
	return nil
}

func (f *fakeSession) Close() error {
	f.closed.Store(true)
	return f.pw.Close()
}

func (f *fakeSession) isClosed() bool { return f.closed.Load() }

func (f *fakeSession) writtenString() string {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	return f.written.String()
}

func (f *fakeSession) emit(data string) {
	f.pw.Write([]byte(data))
}

// fakeConnector is a Connector double whose Open always returns session
// (or openErr) and whose Exec always returns execResult (or execErr).
type fakeConnector struct {
	session *fakeSession
	openErr error

	execResult ExecResult
	execErr    error
}

func (c *fakeConnector) Open(_ context.Context, _ Resource, _, _ uint16) (Session, error) {
	if c.openErr != nil {
		return nil, c.openErr
	}
	return c.session, nil
}

func (c *fakeConnector) Exec(_ context.Context, _ Resource, _ string) (ExecResult, error) {
	return c.execResult, c.execErr
}

// fakeRegistry implements connectorLookup, returning conn for every lookup
// (or no connector at all if conn is nil — simulating an unsupported
// resource type like RDS).
type fakeRegistry struct {
	conn Connector
}

func (r *fakeRegistry) Connector(_, _ string) (Connector, bool) {
	if r.conn == nil {
		return nil, false
	}
	return r.conn, true
}

func TestManagerOpenStreamInputResizeClose(t *testing.T) {
	session := newFakeSession()
	m := NewManager(&fakeRegistry{conn: &fakeConnector{session: session}})

	id, err := m.Open(context.Background(), Resource{Provider: "aws", ResourceType: "vm"}, 24, 80)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	out, done, ok := m.Stream(id)
	if !ok {
		t.Fatal("expected session to exist")
	}

	session.emit("hello")
	select {
	case chunk := <-out:
		if string(chunk) != "hello" {
			t.Fatalf("got %q, want %q", chunk, "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for output")
	}

	if err := m.Write(id, []byte("typed")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := session.writtenString(); got != "typed" {
		t.Fatalf("written = %q, want %q", got, "typed")
	}

	if err := m.Resize(id, 30, 100); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	select {
	case rc := <-session.resizeCh:
		if rc != [2]uint16{30, 100} {
			t.Fatalf("resize = %v, want {30 100}", rc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resize not delivered")
	}

	if err := m.Close(id); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("done channel not closed")
	}
	if !session.isClosed() {
		t.Fatal("expected underlying session to be closed")
	}

	if _, _, ok := m.Stream(id); ok {
		t.Fatal("expected session removed after close")
	}
	if err := m.Write(id, []byte("x")); err == nil {
		t.Fatal("expected error writing to closed session")
	}
}

func TestManagerExec(t *testing.T) {
	conn := &fakeConnector{execResult: ExecResult{Stdout: "ok\n", ExitCode: 0}}
	m := NewManager(&fakeRegistry{conn: conn})

	result, err := m.Exec(context.Background(), Resource{Provider: "aws", ResourceType: "vm"}, "echo ok")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Stdout != "ok\n" || result.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestManagerOpenUnsupportedResourceType(t *testing.T) {
	m := NewManager(&fakeRegistry{conn: nil})
	if _, err := m.Open(context.Background(), Resource{Provider: "aws", ResourceType: "rds"}, 24, 80); err == nil {
		t.Fatal("expected error for unsupported resource type")
	}
}

func TestManagerReapIdleSession(t *testing.T) {
	session := newFakeSession()
	m := NewManager(&fakeRegistry{conn: &fakeConnector{session: session}})

	id, err := m.Open(context.Background(), Resource{Provider: "aws", ResourceType: "vm"}, 24, 80)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	m.mu.Lock()
	m.sessions[id].lastUsed = time.Now().Add(-idleTimeout - time.Second)
	m.mu.Unlock()

	m.reap()

	if _, _, ok := m.Stream(id); ok {
		t.Fatal("expected idle session to be reaped")
	}
	if !session.isClosed() {
		t.Fatal("expected underlying session to be closed by reap")
	}
}

func TestManagerReapMaxDurationSession(t *testing.T) {
	session := newFakeSession()
	m := NewManager(&fakeRegistry{conn: &fakeConnector{session: session}})

	id, err := m.Open(context.Background(), Resource{Provider: "azure", ResourceType: "vm"}, 24, 80)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	m.mu.Lock()
	m.sessions[id].createdAt = time.Now().Add(-maxSessionDuration - time.Second)
	m.mu.Unlock()

	m.reap()

	if _, _, ok := m.Stream(id); ok {
		t.Fatal("expected expired session to be reaped")
	}
	if !session.isClosed() {
		t.Fatal("expected underlying session to be closed by reap")
	}
}
