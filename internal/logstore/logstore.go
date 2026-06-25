// Package logstore tees slog records into the `logs` DB table so the dashboard
// can show the system log with filters, while still writing to stdout. It wraps
// any base slog.Handler; install it once via slog.SetDefault and every slog call
// in the codebase is captured centrally — no per-call wiring.
//
// Writes are asynchronous and best-effort: records go through a buffered channel
// drained by a single writer goroutine, and are DROPPED if the buffer is full,
// so DB logging can never block or slow the server (slog fires on every request).
// Writer/trim errors are reported to stderr directly — never via slog — to avoid
// an infinite logging loop.
package logstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	bufferSize     = 2048
	retentionTrim  = 7 * 24 * time.Hour
	trimInterval   = 1 * time.Hour
	maxMessageSize = 8192
)

type entry struct {
	level   string
	message string
	attrs   map[string]any
	at      time.Time
}

// Handler is a slog.Handler that forwards to a base handler (stdout) and queues
// each record for async persistence.
type Handler struct {
	base  slog.Handler
	sink  chan entry
	attrs []slog.Attr
}

// New wraps base and returns a Handler plus its channel. Call Run(ctx) once to
// start the writer. Install with slog.SetDefault(slog.New(handler)).
func New(base slog.Handler, db *pgxpool.Pool) (*Handler, *Writer) {
	sink := make(chan entry, bufferSize)
	return &Handler{base: base, sink: sink}, &Writer{db: db, sink: sink}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.base.Enabled(ctx, l) }

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// Always write to stdout first (preserves today's logging behaviour).
	baseErr := h.base.Handle(ctx, r)

	m := make(map[string]any, len(h.attrs)+r.NumAttrs())
	for _, a := range h.attrs {
		m[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})

	msg := r.Message
	if len(msg) > maxMessageSize {
		msg = msg[:maxMessageSize]
	}

	select {
	case h.sink <- entry{level: r.Level.String(), message: msg, attrs: m, at: r.Time}:
	default:
		// Buffer full — drop rather than block the caller.
	}
	return baseErr
}

func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(as))
	merged = append(merged, h.attrs...)
	merged = append(merged, as...)
	return &Handler{base: h.base.WithAttrs(as), sink: h.sink, attrs: merged}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{base: h.base.WithGroup(name), sink: h.sink, attrs: h.attrs}
}

// Writer drains queued records into the logs table and trims old rows.
type Writer struct {
	db   *pgxpool.Pool
	sink chan entry
}

// Run drains the queue until ctx is cancelled, persisting each record. It also
// trims rows older than the retention window on a ticker.
func (w *Writer) Run(ctx context.Context) {
	trim := time.NewTicker(trimInterval)
	defer trim.Stop()
	w.trim(ctx) // trim once at startup

	for {
		select {
		case <-ctx.Done():
			return
		case e := <-w.sink:
			w.write(ctx, e)
		case <-trim.C:
			w.trim(ctx)
		}
	}
}

func (w *Writer) write(ctx context.Context, e entry) {
	attrsJSON, err := json.Marshal(e.attrs)
	if err != nil {
		attrsJSON = []byte("{}")
	}
	if _, err := w.db.Exec(ctx,
		"INSERT INTO logs (level, source, message, attrs, created_at) VALUES ($1, $2, $3, $4, $5)",
		e.level, deriveSource(e.message), e.message, attrsJSON, e.at,
	); err != nil {
		// NEVER use slog here — that would recurse into this writer.
		fmt.Fprintf(os.Stderr, "logstore: write failed: %v\n", err)
	}
}

func (w *Writer) trim(ctx context.Context) {
	cutoff := time.Now().Add(-retentionTrim)
	if _, err := w.db.Exec(ctx, "DELETE FROM logs WHERE created_at < $1", cutoff); err != nil {
		fmt.Fprintf(os.Stderr, "logstore: trim failed: %v\n", err)
	}
}

// deriveSource extracts the component from a log message's "<component>: ..."
// prefix (the convention used across the codebase: "docker:", "k8s:", ...).
// Falls back to "app" when there's no recognisable prefix.
func deriveSource(message string) string {
	i := strings.Index(message, ":")
	if i <= 0 || i > 24 {
		return "app"
	}
	prefix := message[:i]
	// A prefix is a single lowercase-ish token (no spaces) like "docker"/"k8s".
	if strings.ContainsAny(prefix, " \t/") {
		return "app"
	}
	return prefix
}
