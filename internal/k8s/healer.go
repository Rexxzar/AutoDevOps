package k8s

import (
	"context"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Rexxzar/autodevops/internal/sse"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	healBaseDelay   = 10 * time.Second
	healMaxDelay    = 5 * time.Minute
	healMaxAttempts = 5
	healJitter      = 0.2
)

type podRecord struct {
	attempts      int
	nextAllowedAt time.Time
	gaveUp        bool
}

type healer struct {
	clientset kubernetes.Interface
	hub       *sse.Hub
	mu        sync.Mutex
	records   map[string]*podRecord
}

func newHealer(clientset kubernetes.Interface, hub *sse.Hub) *healer {
	return &healer{
		clientset: clientset,
		hub:       hub,
		records:   make(map[string]*podRecord),
	}
}

// handle is called on every pod health event. Triggers a restart when the pod
// is unhealthy and the backoff window has elapsed.
func (h *healer) handle(ctx context.Context, ph PodHealth) {
	switch ph.Status {
	case StatusHealthy, StatusSucceeded:
		h.reset(ph.Namespace, ph.PodName)
		return
	case StatusUnhealthy:
		if ph.Reason == "ErrImagePull" || ph.Reason == "ImagePullBackOff" {
			return
		}
	default:
		// Pending, Terminating, Unknown — do nothing
		return
	}

	key := ph.Namespace + "/" + ph.PodName

	h.mu.Lock()
	rec, ok := h.records[key]
	if !ok {
		rec = &podRecord{}
		h.records[key] = rec
	}

	if rec.gaveUp {
		h.mu.Unlock()
		return
	}

	if rec.attempts >= healMaxAttempts {
		rec.gaveUp = true
		h.mu.Unlock()
		slog.Warn("k8s: heal gave up", "pod", ph.PodName, "namespace", ph.Namespace, "attempts", healMaxAttempts)
		h.hub.Publish(sse.Event{Type: "pod.heal.gaveup", Payload: ph})
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

	slog.Info("k8s: restarting pod",
		"pod", ph.PodName,
		"namespace", ph.Namespace,
		"attempt", attempt+1,
		"reason", ph.Reason,
	)

	go func() {
		if err := h.clientset.CoreV1().Pods(ph.Namespace).Delete(ctx, ph.PodName, metav1.DeleteOptions{}); err != nil {
			slog.Error("k8s: delete pod for restart", "pod", ph.PodName, "error", err)
			return
		}
		h.hub.Publish(sse.Event{Type: "pod.heal.restart", Payload: map[string]any{
			"cluster_id": ph.ClusterID,
			"namespace":  ph.Namespace,
			"pod_name":   ph.PodName,
			"attempt":    attempt + 1,
			"reason":     ph.Reason,
		}})
	}()
}

func (h *healer) reset(namespace, podName string) {
	h.mu.Lock()
	delete(h.records, namespace+"/"+podName)
	h.mu.Unlock()
}

// backoffDelay returns the wait time before the next restart attempt.
// Grows exponentially from healBaseDelay, capped at healMaxDelay, with ±20% jitter.
func backoffDelay(attempt int) time.Duration {
	delay := float64(healBaseDelay) * math.Pow(2, float64(attempt))
	if delay > float64(healMaxDelay) {
		delay = float64(healMaxDelay)
	}
	jitter := delay * healJitter * (rand.Float64()*2 - 1)
	return time.Duration(delay + jitter)
}
