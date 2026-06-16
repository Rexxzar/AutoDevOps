package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodInfo is a lightweight snapshot of a K8s pod for the host inventory
// listing — one entry per pod, regardless of whether AutoDevOps created it.
type PodInfo struct {
	Namespace   string
	Name        string
	Containers  []string // pod.Spec.Containers[*].Name
	Image       string   // first container's image
	Phase       string   // Running / Pending / Succeeded / Failed / Unknown
	CreatedAt   time.Time
	Labels      map[string]string
	Restartable bool // true when the pod has OwnerReferences (Deployment/ReplicaSet will recreate it)
}

// ListPods returns a snapshot of all pods across all non-system namespaces in
// the cluster, for the Local Resources host-inventory view.
func (c *Controller) ListPods(ctx context.Context) ([]PodInfo, error) {
	list, err := c.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s: list pods: %w", err)
	}
	out := make([]PodInfo, 0, len(list.Items))
	for _, pod := range list.Items {
		if isSystemNamespace(pod.Namespace) {
			continue
		}
		var containers []string
		var image string
		for i, ctr := range pod.Spec.Containers {
			containers = append(containers, ctr.Name)
			if i == 0 {
				image = ctr.Image
			}
		}
		out = append(out, PodInfo{
			Namespace:   pod.Namespace,
			Name:        pod.Name,
			Containers:  containers,
			Image:       image,
			Phase:       string(pod.Status.Phase),
			CreatedAt:   pod.CreationTimestamp.Time,
			Labels:      pod.Labels,
			Restartable: len(pod.OwnerReferences) > 0,
		})
	}
	return out, nil
}

// PodLogs returns the last tail lines of logs for the given pod/container.
// If container is empty and the pod has a single container, K8s resolves it
// automatically; for multi-container pods the caller must supply the name.
func (c *Controller) PodLogs(ctx context.Context, namespace, pod, containerName string, tail int) (string, error) {
	if tail <= 0 {
		tail = 300
	}
	tailInt64 := int64(tail)
	raw, err := c.clientset.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &tailInt64,
	}).DoRaw(ctx)
	if err != nil {
		return "", fmt.Errorf("k8s: logs for pod %s/%s: %w", namespace, pod, err)
	}
	return string(raw), nil
}

// RestartPod deletes the named pod. If the pod is managed by a Deployment /
// ReplicaSet / DaemonSet (Restartable=true in PodInfo), the controller
// immediately recreates it — effectively a restart. For bare pods without
// OwnerReferences deletion is permanent; the UI should only surface Restart
// when Restartable=true.
//
// Mirrors internal/k8s/healer.go's pod-delete approach for self-healing.
func (c *Controller) RestartPod(ctx context.Context, namespace, pod string) error {
	if err := c.clientset.CoreV1().Pods(namespace).Delete(ctx, pod, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("k8s: restart pod %s/%s: %w", namespace, pod, err)
	}
	return nil
}
