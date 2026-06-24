// Package healmetrics records self-healing episodes (a target went unhealthy ->
// recovered, or the healer gave up) for MTTD/MTTR reporting, and emits the
// crash/recovered bell notifications. It is a metrics+notification observer that
// runs ALONGSIDE the healer: it never influences restart/backoff decisions, and
// every DB/notify operation is best-effort (logged, never returned) so it cannot
// affect self-healing. Depends only on the pgx pool, internal/sse and
// internal/notify — shared leaves, importable by k8s and docker.
package healmetrics

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rexxzar/autodevops/internal/notify"
	"github.com/Rexxzar/autodevops/internal/sse"
)

type openEpisode struct {
	id         string
	detectedAt time.Time
}

// Observer tracks at most one open episode per target key (kind+scope+app) in
// memory, mapping it to the heal_episodes row it opened.
type Observer struct {
	db   *pgxpool.Pool
	hub  *sse.Hub
	mu   sync.Mutex
	open map[string]openEpisode
}

func NewObserver(db *pgxpool.Pool, hub *sse.Hub) *Observer {
	return &Observer{db: db, hub: hub, open: make(map[string]openEpisode)}
}

func key(kind, scope, app string) string { return kind + ":" + scope + "/" + app }

// Observe records a definite health transition for a target. healthy=false opens
// a new episode if none is open for this target (and fires a "crashed"
// notification); healthy=true closes the open one with a recovery time (and fires
// a "self-healed" notification). Callers should only call this for unambiguous
// states (skip pending/unknown). failureAt is best-effort — a zero value stores
// NULL (MTTD then unknown for that episode).
func (o *Observer) Observe(ctx context.Context, kind, app, scope, reason string, healthy bool, failureAt time.Time) {
	if o == nil || o.db == nil || app == "" {
		return
	}
	k := key(kind, scope, app)

	o.mu.Lock()
	ep, isOpen := o.open[k]
	o.mu.Unlock()

	if healthy {
		if !isOpen {
			return
		}
		o.mu.Lock()
		delete(o.open, k)
		o.mu.Unlock()
		if _, err := o.db.Exec(ctx,
			"UPDATE heal_episodes SET recovered_at = now() WHERE id = $1 AND recovered_at IS NULL", ep.id,
		); err != nil {
			slog.Error("healmetrics: close episode", "key", k, "error", err)
		}
		o.notifyRecovered(ctx, kind, app, time.Since(ep.detectedAt))
		return
	}

	// Unhealthy — open a new episode unless one is already tracking this target.
	if isOpen {
		return
	}
	detected := time.Now()
	var fa *time.Time
	if !failureAt.IsZero() {
		fa = &failureAt
	}
	var newID string
	if err := o.db.QueryRow(ctx, `
		INSERT INTO heal_episodes (target_kind, target_name, scope, reason, failure_at, detected_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text
	`, kind, app, scope, reason, fa, detected).Scan(&newID); err != nil {
		slog.Error("healmetrics: open episode", "key", k, "error", err)
		return
	}
	o.mu.Lock()
	o.open[k] = openEpisode{id: newID, detectedAt: detected}
	o.mu.Unlock()
	o.notifyCrashed(ctx, kind, app, reason)
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
	ep, ok := o.open[k]
	o.mu.Unlock()
	if !ok {
		return
	}
	if _, err := o.db.Exec(ctx, query, ep.id); err != nil {
		slog.Error("healmetrics: update episode", "key", k, "error", err)
	}
}

func (o *Observer) notifyCrashed(ctx context.Context, kind, app, reason string) {
	noun := "Container"
	if kind == "pod" {
		noun = "Pod"
	}
	body := "Detected unhealthy — self-healing is attempting recovery."
	if reason != "" {
		body = "Detected unhealthy (" + reason + ") — self-healing is attempting recovery."
	}
	notify.Publish(ctx, o.db, o.hub, notify.Notification{
		Type:     "heal",
		Severity: notify.Warning,
		Title:    noun + " went down: " + app,
		Body:     body,
		Link:     healLink(kind),
	})
}

func (o *Observer) notifyRecovered(ctx context.Context, kind, app string, took time.Duration) {
	notify.Publish(ctx, o.db, o.hub, notify.Notification{
		Type:     "heal",
		Severity: notify.Success,
		Title:    "Self-healed: " + app + " recovered",
		Body:     "Back to healthy in " + humanizeDuration(took) + ".",
		Link:     healLink(kind),
	})
}

func healLink(kind string) string {
	if kind == "pod" {
		return "/clusters"
	}
	return "/deployments"
}

func humanizeDuration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 1 {
		return "<1s"
	}
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}
