// Package notify persists user-facing notifications and pushes them live over
// the existing SSE hub. It is a thin shared leaf — depends only on the pgx pool
// and internal/sse (the same things every producing package already holds) — so
// deploy/k8s/docker/api can all emit notifications without new wiring or a
// message broker (consistent with the locked single-binary, SSE-only stack).
package notify

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rexxzar/autodevops/internal/sse"
)

// Severity levels for a Notification.
const (
	Info     = "info"
	Success  = "success"
	Warning  = "warning"
	Critical = "critical"
)

// Notification is one bell-surfaced event. Link is an in-app deep link
// (e.g. "/deployments/<id>") the UI navigates to when the item is clicked.
type Notification struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`     // deploy | heal | resource | compliance
	Severity  string    `json:"severity"` // info | success | warning | critical
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Link      string    `json:"link,omitempty"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// Publish persists n and broadcasts it over SSE as a "notification" event.
// It is BEST-EFFORT: any failure is logged and swallowed so a notification can
// never break the operation that triggered it (a deploy, heal, apply, etc.).
// hub may be nil (e.g. in tests) — persistence still happens.
func Publish(ctx context.Context, db *pgxpool.Pool, hub *sse.Hub, n Notification) {
	if db == nil {
		return
	}
	if n.Severity == "" {
		n.Severity = Info
	}

	err := db.QueryRow(ctx, `
		INSERT INTO notifications (type, severity, title, body, link)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id::text, read, created_at
	`, n.Type, n.Severity, n.Title, n.Body, n.Link).Scan(&n.ID, &n.Read, &n.CreatedAt)
	if err != nil {
		slog.Error("notify: persist notification", "title", n.Title, "error", err)
		return
	}

	if hub != nil {
		hub.Publish(sse.Event{Type: "notification", Payload: n})
	}
}
