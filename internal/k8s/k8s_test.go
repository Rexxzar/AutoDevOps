package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClassify_healthy(t *testing.T) {
	h := classify(runningPod("default", "app", true, 0, ""))
	if h.Status != StatusHealthy {
		t.Fatalf("expected healthy, got %s", h.Status)
	}
	if h.RestartCount != 0 {
		t.Fatalf("expected 0 restarts, got %d", h.RestartCount)
	}
}

func TestClassify_crashLoopBackOff(t *testing.T) {
	h := classify(runningPod("default", "app", false, 5, "CrashLoopBackOff"))
	if h.Status != StatusUnhealthy {
		t.Fatalf("expected unhealthy, got %s", h.Status)
	}
	if h.Reason != "CrashLoopBackOff" {
		t.Fatalf("expected reason CrashLoopBackOff, got %s", h.Reason)
	}
	if h.RestartCount != 5 {
		t.Fatalf("expected restart count 5, got %d", h.RestartCount)
	}
}

func TestClassify_imagePullBackOff(t *testing.T) {
	h := classify(runningPod("default", "app", false, 0, "ImagePullBackOff"))
	if h.Status != StatusUnhealthy {
		t.Fatalf("expected unhealthy, got %s", h.Status)
	}
	if h.Reason != "ImagePullBackOff" {
		t.Fatalf("expected reason ImagePullBackOff, got %s", h.Reason)
	}
}

func TestClassify_containerNotReady(t *testing.T) {
	h := classify(runningPod("default", "app", false, 0, ""))
	if h.Status != StatusUnhealthy {
		t.Fatalf("expected unhealthy (not ready), got %s", h.Status)
	}
	if h.Reason != "container not ready" {
		t.Fatalf("expected reason 'container not ready', got %s", h.Reason)
	}
}

func TestClassify_pending(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	h := classify(pod)
	if h.Status != StatusPending {
		t.Fatalf("expected pending, got %s", h.Status)
	}
}

func TestClassify_terminating(t *testing.T) {
	now := metav1.Now()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "default",
			Name:              "app",
			DeletionTimestamp: &now,
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	h := classify(pod)
	if h.Status != StatusTerminating {
		t.Fatalf("expected terminating, got %s", h.Status)
	}
}

func TestClassify_succeeded(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "job-pod"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	h := classify(pod)
	if h.Status != StatusSucceeded {
		t.Fatalf("expected succeeded, got %s", h.Status)
	}
}

func TestClassify_failed(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app"},
		Status: corev1.PodStatus{
			Phase:   corev1.PodFailed,
			Message: "OOMKilled",
		},
	}
	h := classify(pod)
	if h.Status != StatusUnhealthy {
		t.Fatalf("expected unhealthy, got %s", h.Status)
	}
}

func TestClassify_systemNamespaceSkipped(t *testing.T) {
	for _, ns := range []string{"kube-system", "kube-public", "kube-node-lease", "ingress-nginx"} {
		if !isSystemNamespace(ns) {
			t.Fatalf("expected %s to be a system namespace", ns)
		}
	}
	if isSystemNamespace("default") {
		t.Fatal("expected default to not be a system namespace")
	}
}

// runningPod builds a pod with Running phase and one container status entry.
func runningPod(ns, name string, ready bool, restarts int32, waitingReason string) *corev1.Pod {
	cs := corev1.ContainerStatus{
		Name:         "app",
		Ready:        ready,
		RestartCount: restarts,
	}
	if waitingReason != "" {
		cs.State = corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: waitingReason},
		}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{cs},
		},
	}
}
