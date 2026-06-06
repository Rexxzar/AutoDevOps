package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/Rexxzar/autodevops/internal/sse"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBackoffDelay_growsExponentially(t *testing.T) {
	prev := time.Duration(0)
	for i := 0; i < 5; i++ {
		d := backoffDelay(i)
		if d <= 0 {
			t.Fatalf("attempt %d: delay must be positive, got %v", i, d)
		}
		if i > 0 && d <= prev/2 {
			t.Fatalf("attempt %d: delay %v should be larger than previous %v", i, d, prev)
		}
		prev = d
	}
}

func TestBackoffDelay_cappedAtMax(t *testing.T) {
	for _, attempt := range []int{10, 20, 50} {
		d := backoffDelay(attempt)
		max := float64(healMaxDelay) * (1 + healJitter)
		if float64(d) > max {
			t.Fatalf("attempt %d: delay %v exceeds max %v", attempt, d, healMaxDelay)
		}
	}
}

func TestHealer_triggersRestartOnUnhealthy(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app"}}
	clientset := fake.NewSimpleClientset(pod)
	hub := sse.NewHub()
	h := newHealer(clientset, hub)

	h.handle(context.Background(), PodHealth{
		Namespace: "default", PodName: "app",
		Status: StatusUnhealthy, Reason: "CrashLoopBackOff",
	})

	time.Sleep(100 * time.Millisecond)

	_, err := clientset.CoreV1().Pods("default").Get(context.Background(), "app", metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected pod to be deleted after heal")
	}
}

func TestHealer_respectsBackoff(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app"}}
	clientset := fake.NewSimpleClientset(pod)
	hub := sse.NewHub()
	h := newHealer(clientset, hub)

	ph := PodHealth{Namespace: "default", PodName: "app", Status: StatusUnhealthy}

	// First call triggers restart
	h.handle(context.Background(), ph)
	time.Sleep(50 * time.Millisecond)

	// Re-add the pod (simulating K8s recreating it)
	clientset.CoreV1().Pods("default").Create(context.Background(), pod, metav1.CreateOptions{})

	// Second immediate call must be blocked by backoff
	h.handle(context.Background(), ph)
	time.Sleep(50 * time.Millisecond)

	// Pod should still exist (second restart was blocked)
	_, err := clientset.CoreV1().Pods("default").Get(context.Background(), "app", metav1.GetOptions{})
	if err != nil {
		t.Fatal("pod should still exist — second restart should have been blocked by backoff")
	}
}

func TestHealer_givesUpAfterMaxAttempts(t *testing.T) {
	hub := sse.NewHub()
	h := newHealer(fake.NewSimpleClientset(), hub)

	// Collect SSE events
	events := make([]sse.Event, 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// drain for 200ms
		time.Sleep(200 * time.Millisecond)
	}()

	ph := PodHealth{Namespace: "default", PodName: "app", Status: StatusUnhealthy}

	// Force past max attempts by setting nextAllowedAt to zero each iteration
	key := "default/app"
	for i := 0; i <= healMaxAttempts; i++ {
		h.mu.Lock()
		if rec, ok := h.records[key]; ok {
			rec.nextAllowedAt = time.Time{} // force backoff to expire
		}
		h.mu.Unlock()
		h.handle(context.Background(), ph)
		time.Sleep(20 * time.Millisecond)
	}

	<-done
	_ = events

	h.mu.Lock()
	rec := h.records[key]
	h.mu.Unlock()

	if rec == nil || !rec.gaveUp {
		t.Fatal("expected healer to have given up after max attempts")
	}
}

func TestHealer_resetsOnHealthy(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app"}}
	clientset := fake.NewSimpleClientset(pod)
	hub := sse.NewHub()
	h := newHealer(clientset, hub)

	ph := PodHealth{Namespace: "default", PodName: "app", Status: StatusUnhealthy}
	h.handle(context.Background(), ph)
	time.Sleep(50 * time.Millisecond)

	// Pod recovers
	h.handle(context.Background(), PodHealth{
		Namespace: "default", PodName: "app", Status: StatusHealthy,
	})

	// Record should be cleared
	h.mu.Lock()
	_, exists := h.records["default/app"]
	h.mu.Unlock()
	if exists {
		t.Fatal("expected record to be cleared after pod became healthy")
	}
}
