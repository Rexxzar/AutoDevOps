package k8s

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/jackc/pgx/v5/pgxpool"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// PodStatus is the classified health state of a pod.
type PodStatus string

const (
	StatusHealthy     PodStatus = "healthy"
	StatusUnhealthy   PodStatus = "unhealthy"
	StatusPending     PodStatus = "pending"
	StatusTerminating PodStatus = "terminating"
	StatusSucceeded   PodStatus = "succeeded"
	StatusUnknown     PodStatus = "unknown"
)

// PodHealth is the classified health of a single pod, published via SSE and persisted.
type PodHealth struct {
	ClusterID    string    `json:"cluster_id"`
	Namespace    string    `json:"namespace"`
	PodName      string    `json:"pod_name"`
	Status       PodStatus `json:"status"`
	Reason       string    `json:"reason,omitempty"`
	RestartCount int32     `json:"restart_count"`
}

// Controller connects to a K8s cluster and runs the pod health Watch loop.
type Controller struct {
	clientset     kubernetes.Interface
	dynClient     dynamic.Interface
	healer        *healer
	serverURL     string
	clusterID     string
	ingressDomain string
	pool          *pgxpool.Pool
	hub           *sse.Hub
}

// ServerURL returns the K8s API server URL from the kubeconfig, without
// creating a full Controller. Used by main to register the cluster in the DB.
func ServerURL(kubeconfigPath string) (string, error) {
	cfg, err := buildConfig(kubeconfigPath)
	if err != nil {
		return "", fmt.Errorf("k8s: server url: %w", err)
	}
	return cfg.Host, nil
}

// New builds a Controller. Falls back to in-cluster config when kubeconfigPath
// is empty and the binary is running inside a pod. ingressDomain is the base
// DNS domain used when generating Ingress hostnames (e.g. "autodevops.local").
func New(kubeconfigPath, clusterID string, pool *pgxpool.Pool, hub *sse.Hub, ingressDomain string) (*Controller, error) {
	cfg, err := buildConfig(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("k8s: build config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: new clientset: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("k8s: new dynamic client: %w", err)
	}
	return &Controller{
		clientset:     clientset,
		dynClient:     dynClient,
		healer:        newHealer(clientset, pool, hub),
		serverURL:     cfg.Host,
		clusterID:     clusterID,
		ingressDomain: ingressDomain,
		pool:          pool,
		hub:           hub,
	}, nil
}

// Run starts the SharedInformer Watch loop and blocks until ctx is cancelled.
// It reconnects automatically on API server disconnects.
func (c *Controller) Run(ctx context.Context) error {
	factory := informers.NewSharedInformerFactory(c.clientset, 30*time.Second)
	podInformer := factory.Core().V1().Pods().Informer()

	if _, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if pod, ok := obj.(*corev1.Pod); ok {
				c.handlePod(ctx, pod)
			}
		},
		UpdateFunc: func(_, obj any) {
			if pod, ok := obj.(*corev1.Pod); ok {
				c.handlePod(ctx, pod)
			}
		},
		DeleteFunc: func(obj any) {
			if pod, ok := obj.(*corev1.Pod); ok {
				c.deletePodHealth(ctx, pod.Namespace, pod.Name)
			}
		},
	}); err != nil {
		return fmt.Errorf("k8s: add event handler: %w", err)
	}

	factory.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced) {
		return fmt.Errorf("k8s: timed out waiting for cache sync")
	}

	slog.Info("k8s: watch loop running", "cluster_id", c.clusterID, "server", c.serverURL)

	go c.reconcileLoop(ctx)

	<-ctx.Done()
	return nil
}

func (c *Controller) handlePod(ctx context.Context, pod *corev1.Pod) {
	if isSystemNamespace(pod.Namespace) {
		return
	}
	h := classify(pod)
	h.ClusterID = c.clusterID

	if err := c.upsertPodHealth(ctx, h); err != nil {
		slog.Error("k8s: upsert pod health", "pod", pod.Name, "error", err)
	}
	c.hub.Publish(sse.Event{Type: "pod.health", Payload: h})
	c.healer.handle(ctx, h)
}

func (c *Controller) upsertPodHealth(ctx context.Context, h PodHealth) error {
	_, err := c.pool.Exec(ctx, `
		INSERT INTO pod_health
			(cluster_id, namespace, pod_name, status, restart_count, last_restart_at, observed_at)
		VALUES ($1, $2, $3, $4, $5, NULL, now())
		ON CONFLICT (cluster_id, namespace, pod_name) DO UPDATE SET
			status          = EXCLUDED.status,
			restart_count   = EXCLUDED.restart_count,
			last_restart_at = CASE
				WHEN EXCLUDED.restart_count > pod_health.restart_count THEN now()
				ELSE pod_health.last_restart_at
			END,
			observed_at     = now()
	`, h.ClusterID, h.Namespace, h.PodName, string(h.Status), h.RestartCount)
	if err != nil {
		return fmt.Errorf("k8s: upsert pod health: %w", err)
	}
	return nil
}

func (c *Controller) deletePodHealth(ctx context.Context, namespace, podName string) {
	_, err := c.pool.Exec(ctx,
		"DELETE FROM pod_health WHERE cluster_id=$1 AND namespace=$2 AND pod_name=$3",
		c.clusterID, namespace, podName,
	)
	if err != nil {
		slog.Error("k8s: delete pod health", "namespace", namespace, "pod", podName, "error", err)
	}
}

// classify derives PodHealth from pod status fields. Pure function — no I/O.
func classify(pod *corev1.Pod) PodHealth {
	h := PodHealth{
		Namespace: pod.Namespace,
		PodName:   pod.Name,
		Status:    StatusUnknown,
	}

	if pod.DeletionTimestamp != nil {
		h.Status = StatusTerminating
		return h
	}

	for _, cs := range pod.Status.ContainerStatuses {
		h.RestartCount += cs.RestartCount
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "CrashLoopBackOff", "OOMKilled", "Error",
				"CreateContainerError", "ImagePullBackOff", "ErrImagePull":
				h.Status = StatusUnhealthy
				h.Reason = w.Reason
				return h
			}
		}
	}

	switch pod.Status.Phase {
	case corev1.PodRunning:
		for _, cs := range pod.Status.ContainerStatuses {
			if !cs.Ready {
				h.Status = StatusUnhealthy
				h.Reason = "container not ready"
				return h
			}
		}
		h.Status = StatusHealthy
	case corev1.PodPending:
		h.Status = StatusPending
	case corev1.PodSucceeded:
		h.Status = StatusSucceeded
	case corev1.PodFailed:
		h.Status = StatusUnhealthy
		h.Reason = pod.Status.Message
	default:
		h.Status = StatusUnknown
	}

	return h
}

// reconcileLoop periodically compares pod_health DB rows against actual K8s
// pods and prunes stale entries. Catches pods that were deleted while the
// watch loop was disconnected or the server was down.
func (c *Controller) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reconcilePodHealth(ctx)
		}
	}
}

func (c *Controller) reconcilePodHealth(ctx context.Context) {
	rows, err := c.pool.Query(ctx,
		"SELECT namespace, pod_name FROM pod_health WHERE cluster_id = $1",
		c.clusterID,
	)
	if err != nil {
		slog.Error("k8s: reconcile query pod_health", "error", err)
		return
	}
	defer rows.Close()

	type podKey struct{ namespace, name string }
	var dbPods []podKey
	for rows.Next() {
		var k podKey
		if err := rows.Scan(&k.namespace, &k.name); err != nil {
			slog.Error("k8s: reconcile scan", "error", err)
			return
		}
		dbPods = append(dbPods, k)
	}

	pruned := 0
	for _, k := range dbPods {
		_, err := c.clientset.CoreV1().Pods(k.namespace).Get(ctx, k.name, metav1.GetOptions{})
		if err != nil && kerrors.IsNotFound(err) {
			c.deletePodHealth(ctx, k.namespace, k.name)
			pruned++
		}
	}
	if pruned > 0 {
		slog.Info("k8s: reconcile pruned stale pods from pod_health", "count", pruned)
	}
}

func isSystemNamespace(ns string) bool {
	switch ns {
	case "kube-system", "kube-public", "kube-node-lease",
		"ingress-nginx", "local-path-storage", "cert-manager":
		return true
	}
	return false
}

func buildConfig(kubeconfigPath string) (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	if kubeconfigPath == "" {
		home, _ := os.UserHomeDir()
		kubeconfigPath = filepath.Join(home, ".kube", "config")
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}
