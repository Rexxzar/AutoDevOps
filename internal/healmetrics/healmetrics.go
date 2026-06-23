// Package healmetrics records self-healing episodes (a target went unhealthy ->
// recovered, or the healer gave up) for MTTD/MTTR reporting. It is a metrics-only
// observer that runs ALONGSIDE the healer: it never influences restart/backoff
// decisions, and every DB operation is best-effort (logged, never returned) so it
// cannot affect self-healing. Depends only on the pgx pool — a shared leaf like
// internal/notify, importable by k8s and docker without crossing concerns.
package healmetrics

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Observer tracks at most one open episode per target key (kind+scope+app) in
// memory, mapping it to the heal_episodes row it opened.
type Observer struct {
	db   *pgxpool.Pool
	mu   sync.Mutex
	open map[string]string // key -> episode id
}

func NewObserver(db *pgxpool.Pool) *Observer {
	return &Observer{db: db, open: make(map[string]string)}
}

func key(kind, scope, app string) string { return kind + ":" + scope + "/" + app }

// Observe records a definite health transition for a target. healthy=false opens
// a new episode if none is open for this target; healthy=true closes the open one
// with a recovery time. Callers should only call this for unambiguous states
// (skip pending/unknown) so transient flaps don't create episodes. failureAt is
// best-effort — a zero value stores NULL (MTTD then unknown for that episode).
func (o *Observer) Observe(ctx context.Context, kind, app, scope, reason string, healthy bool, failureAt time.Time) {
	if o == nil || o.db == nil || app == "" {
		return
	}
	k := key(kind, scope, app)

	o.mu.Lock()
	id, isOpen := o.open[k]
	o.mu.Unlock()

	if healthy {
		if !isOpen {
			return
		}
		o.mu.Lock()
		delete(o.open, k)
		o.mu.Unlock()
		if _, err := o.db.Exec(ctx,
			"UPDATE heal_episodes SET recovered_at = now() WHERE id = $1 AND recovered_at IS NULL", id,
		); err != nil {
			slog.Error("healmetrics: close episode", "key", k, "error", err)
		}
		return
	}

	// Unhealthy — open a new episode unless one is already tracking this target.
	if isOpen {
		return
	}
	var fa *time.Time
	if !failureAt.IsZero() {
		fa = &failureAt
	}
	var newID string
	if err := o.db.QueryRow(ctx, `
		INSERT INTO heal_episodes (target_kind, target_name, scope, reason, failure_at, detected_at)
		VALUES ($1, $2, $3, $4, $5, now())
		RETURNING id::text
	`, kind, app, scope, reason, fa).Scan(&newID); err != nil {
		slog.Error("healmetrics: open episode", "key", k, "error", err)
		return
	}
	o.mu.Lock()
	o.open[k] = newID
	o.mu.Unlock()
}

// RecordRestart increments the attempt count on the open episode for a target.
func (o *Observer) RecordRestart(ctx context.Context, kind, app, scope string) {
	o.bump(ctx, key(kind, scope, app), "UPDATE heal_episodes SET attempts = attempts + 1 WHERE id = $1")
}

// RecordGaveUp marks the open episode as gave_up (the episode stays open so a
// later manual recovery can still close it with a recovered_at).
func (o *Observer) RecordGaveUp(ctx context.Context, kind, app, scope string) {
	o.bump(ctx, key(kind, scope, app), "UPDATE heal_episodes SET gave_up = true WHERE id = $1")
}

func (o *Observer) bump(ctx context.Context, k, query string) {
	if o == nil || o.db == nil {
		return
	}
	o.mu.Lock()
	id, ok := o.open[k]
	o.mu.Unlock()
	if !ok {
		return
	}
	if _, err := o.db.Exec(ctx, query, id); err != nil {
		slog.Error("healmetrics: update episode", "key", k, "error", err)
	}
}
