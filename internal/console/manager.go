package console

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// idleTimeout closes a session after this long with no input/output
	// activity — the primary safety net for forgotten terminals.
	idleTimeout = 10 * time.Minute
	// maxSessionDuration force-closes a session regardless of activity.
	maxSessionDuration = time.Hour
	// reapInterval is how often Run checks for idle/expired sessions.
	reapInterval = 30 * time.Second

	outputBufferSize = 64
	readBufferSize   = 4096
)

// Manager owns the in-memory registry of open interactive console sessions.
// Nothing here is persisted: every session is ephemeral and force-closed
// after idleTimeout of inactivity or maxSessionDuration total lifetime.
type Manager struct {
	registry connectorLookup

	mu       sync.Mutex
	sessions map[string]*managedSession
}

type managedSession struct {
	Session
	createdAt time.Time
	lastUsed  time.Time
	out       chan []byte
	done      chan struct{}
}

// NewManager constructs a Manager backed by registry for connector lookups.
func NewManager(registry connectorLookup) *Manager {
	return &Manager{registry: registry, sessions: make(map[string]*managedSession)}
}

// Open starts an interactive session for res and returns its ID. A
// background goroutine pumps the underlying Session's output into a channel
// consumed by Stream (the SSE handler); when the remote side closes or errors,
// the session is removed automatically.
func (m *Manager) Open(ctx context.Context, res Resource, rows, cols uint16) (string, error) {
	connector, ok := m.registry.Connector(res.Provider, res.ResourceType)
	if !ok {
		return "", fmt.Errorf("console: no console support for %s/%s", res.Provider, res.ResourceType)
	}
	sess, err := connector.Open(ctx, res, rows, cols)
	if err != nil {
		return "", err
	}

	id := uuid.NewString()
	now := time.Now()
	ms := &managedSession{
		Session:   sess,
		createdAt: now,
		lastUsed:  now,
		out:       make(chan []byte, outputBufferSize),
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[id] = ms
	m.mu.Unlock()

	go m.pump(id, ms)
	return id, nil
}

// pump reads from ms.Session until it errors (or the session is closed) and
// forwards chunks to ms.out for Stream to relay over SSE.
func (m *Manager) pump(id string, ms *managedSession) {
	buf := make([]byte, readBufferSize)
	for {
		n, err := ms.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case ms.out <- chunk:
				m.touch(id)
			case <-ms.done:
				return
			}
		}
		if err != nil {
			m.Close(id)
			return
		}
	}
}

// Stream returns the output channel and done channel for sessionID, for the
// SSE handler to relay to the browser. done is closed when the session ends
// (remote close, error, idle/max timeout, or explicit Close).
func (m *Manager) Stream(sessionID string) (out <-chan []byte, done <-chan struct{}, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ms, exists := m.sessions[sessionID]
	if !exists {
		return nil, nil, false
	}
	return ms.out, ms.done, true
}

// Write sends p to sessionID's stdin (e.g. keystrokes from xterm.js).
func (m *Manager) Write(sessionID string, p []byte) error {
	m.mu.Lock()
	ms, ok := m.sessions[sessionID]
	if ok {
		ms.lastUsed = time.Now()
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("console: session %s not found", sessionID)
	}
	_, err := ms.Write(p)
	return err
}

// Resize changes sessionID's PTY size.
func (m *Manager) Resize(sessionID string, rows, cols uint16) error {
	m.mu.Lock()
	ms, ok := m.sessions[sessionID]
	if ok {
		ms.lastUsed = time.Now()
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("console: session %s not found", sessionID)
	}
	return ms.Resize(rows, cols)
}

// Close ends sessionID's session and releases its resources. Safe to call
// more than once or on an unknown ID.
func (m *Manager) Close(sessionID string) error {
	m.mu.Lock()
	ms, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	close(ms.done)
	return ms.Session.Close()
}

func (m *Manager) touch(id string) {
	m.mu.Lock()
	if ms, ok := m.sessions[id]; ok {
		ms.lastUsed = time.Now()
	}
	m.mu.Unlock()
}

// Exec runs a one-shot command against res — no session bookkeeping needed.
func (m *Manager) Exec(ctx context.Context, res Resource, command string) (ExecResult, error) {
	connector, ok := m.registry.Connector(res.Provider, res.ResourceType)
	if !ok {
		return ExecResult{}, fmt.Errorf("console: no console support for %s/%s", res.Provider, res.ResourceType)
	}
	return connector.Exec(ctx, res, command)
}

// Run reaps idle/expired sessions until ctx is cancelled. Start as a
// goroutine alongside the other long-running loops in cmd/server/main.go.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reap()
		}
	}
}

func (m *Manager) reap() {
	now := time.Now()
	m.mu.Lock()
	var expired []string
	for id, ms := range m.sessions {
		if now.Sub(ms.lastUsed) > idleTimeout || now.Sub(ms.createdAt) > maxSessionDuration {
			expired = append(expired, id)
		}
	}
	m.mu.Unlock()
	for _, id := range expired {
		m.Close(id)
	}
}
