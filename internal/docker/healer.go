package docker

import (
	"context"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

const (
	healBaseDelay   = 10 * time.Second
	healMaxDelay    = 5 * time.Minute
	healMaxAttempts = 5
	healJitter      = 0.2
)

type containerRecord struct {
	attempts      int
	nextAllowedAt time.Time
	gaveUp        bool
}

type healer struct {
	client  *dockerclient.Client
	hub     *sse.Hub
	mu      sync.Mutex
	records map[string]*containerRecord
}

func newHealer(client *dockerclient.Client, hub *sse.Hub) *healer {
	return &healer{
		client:  client,
		hub:     hub,
		records: make(map[string]*containerRecord),
	}
}

// handle is called on every ContainerHealth event. Triggers a restart when the
// container is stopped/failed and the backoff window has elapsed. "Missing"
// containers give up immediately (they were externally removed and can't be
// recreated without the original image ref). Mirrors k8s.healer.handle.
func (h *healer) handle(ctx context.Context, ch ContainerHealth) {
	switch ch.Status {
	case "running":
		h.reset(ch.Name)
		return
	case "stopped", "failed":
		// fall through to heal logic
	case "missing":
		// container was removed externally — can't recreate without image ref
		h.mu.Lock()
		rec := h.getOrCreate(ch.Name)
		if !rec.gaveUp {
			rec.gaveUp = true
			h.mu.Unlock()
			slog.Warn("docker: container missing, giving up", "name", ch.Name)
			h.hub.Publish(sse.Event{Type: "container.heal.gaveup", Payload: ch})
			return
		}
		h.mu.Unlock()
		return
	default:
		return
	}

	h.mu.Lock()
	rec := h.getOrCreate(ch.Name)

	if rec.gaveUp {
		h.mu.Unlock()
		return
	}
	if rec.attempts >= healMaxAttempts {
		rec.gaveUp = true
		h.mu.Unlock()
		slog.Warn("docker: heal gave up", "name", ch.Name, "attempts", healMaxAttempts)
		h.hub.Publish(sse.Event{Type: "container.heal.gaveup", Payload: ch})
		return
	}
	if time.Now().Before(rec.nextAllowedAt) {
		h.mu.Unlock()
		return
	}

	attempt := rec.attempts
	rec.attempts++
	rec.nextAllowedAt = time.Now().Add(backoffDelay(attempt))
	h.mu.Unlock()

	slog.Info("docker: restarting container",
		"name", ch.Name,
		"container", containerName(ch.Name),
		"attempt", attempt+1,
		"reason", ch.Reason,
	)

	go func() {
		cname := containerName(ch.Name)
		if err := h.client.ContainerStart(ctx, cname, container.StartOptions{}); err != nil {
			slog.Error("docker: restart container", "container", cname, "error", err)
			return
		}
		h.hub.Publish(sse.Event{Type: "container.heal.restart", Payload: map[string]any{
			"deployment_id": ch.DeploymentID,
			"resource_id":   ch.ResourceID,
			"kind":          ch.Kind,
			"name":          ch.Name,
			"attempt":       attempt + 1,
			"reason":        ch.Reason,
		}})
	}()
}

func (h *healer) getOrCreate(name string) *containerRecord {
	rec, ok := h.records[name]
	if !ok {
		rec = &containerRecord{}
		h.records[name] = rec
	}
	return rec
}

func (h *healer) reset(name string) {
	h.mu.Lock()
	delete(h.records, name)
	h.mu.Unlock()
}

// backoffDelay grows exponentially from healBaseDelay, capped at healMaxDelay,
// with ±20% jitter. Identical formula to k8s.backoffDelay.
func backoffDelay(attempt int) time.Duration {
	delay := float64(healBaseDelay) * math.Pow(2, float64(attempt))
	if delay > float64(healMaxDelay) {
		delay = float64(healMaxDelay)
	}
	jitter := delay * healJitter * (rand.Float64()*2 - 1)
	return time.Duration(delay + jitter)
}
