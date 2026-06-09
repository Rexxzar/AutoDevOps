package k8s

import (
	"context"
	"fmt"
	"log/slog"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	certGVR   = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	issuerGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}
)

// KubeDeploy applies the full set of K8s resources for a named deployment:
// Namespace, Deployment (app container + health-checker sidecar), Service,
// cert-manager ClusterIssuer + Certificate (best-effort — silently skipped if
// cert-manager CRDs are not installed), and Ingress. Blocks until at least one
// pod reports Available or the context deadline is reached.
func (c *Controller) KubeDeploy(ctx context.Context, name, namespace, imageRef string) error {
	host := name + "." + c.ingressDomain

	slog.Info("k8s: deploying", "name", name, "namespace", namespace, "image", imageRef, "host", host)

	if err := c.applyNamespace(ctx, buildNamespace(name)); err != nil {
		return fmt.Errorf("k8s: apply namespace %s: %w", namespace, err)
	}
	if err := c.applyDeployment(ctx, buildDeployment(name, namespace, imageRef)); err != nil {
		return fmt.Errorf("k8s: apply deployment %s: %w", name, err)
	}
	if err := c.applyService(ctx, buildService(name, namespace)); err != nil {
		return fmt.Errorf("k8s: apply service %s: %w", name, err)
	}

	// cert-manager resources are best-effort: the platform still works over plain
	// HTTP if cert-manager is not installed in the cluster.
	if err := c.applyCRD(ctx, issuerGVR, buildClusterIssuer()); err != nil {
		slog.Warn("k8s: cluster issuer not applied (cert-manager may not be installed)", "error", err)
	}
	if err := c.applyCRD(ctx, certGVR, buildCertificate(name, namespace, host)); err != nil {
		slog.Warn("k8s: certificate not applied (cert-manager may not be installed)", "error", err)
	}

	if err := c.applyIngress(ctx, buildIngress(name, namespace, host)); err != nil {
		return fmt.Errorf("k8s: apply ingress %s: %w", name, err)
	}

	slog.Info("k8s: waiting for deployment to become available", "name", name, "namespace", namespace)
	if err := c.waitForDeployment(ctx, namespace, name); err != nil {
		return fmt.Errorf("k8s: wait for deployment %s/%s: %w", namespace, name, err)
	}

	slog.Info("k8s: deployment available", "name", name, "namespace", namespace, "host", host)
	return nil
}

func (c *Controller) applyNamespace(ctx context.Context, ns *corev1.Namespace) error {
	_, err := c.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if kerrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (c *Controller) applyDeployment(ctx context.Context, dep *appsv1.Deployment) error {
	_, err := c.clientset.AppsV1().Deployments(dep.Namespace).Create(ctx, dep, metav1.CreateOptions{})
	if !kerrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.clientset.AppsV1().Deployments(dep.Namespace).Get(ctx, dep.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get existing deployment: %w", err)
	}
	dep.ResourceVersion = existing.ResourceVersion
	_, err = c.clientset.AppsV1().Deployments(dep.Namespace).Update(ctx, dep, metav1.UpdateOptions{})
	return err
}

func (c *Controller) applyService(ctx context.Context, svc *corev1.Service) error {
	_, err := c.clientset.CoreV1().Services(svc.Namespace).Create(ctx, svc, metav1.CreateOptions{})
	if !kerrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.clientset.CoreV1().Services(svc.Namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get existing service: %w", err)
	}
	svc.ResourceVersion = existing.ResourceVersion
	svc.Spec.ClusterIP = existing.Spec.ClusterIP // must preserve the allocated ClusterIP on update
	_, err = c.clientset.CoreV1().Services(svc.Namespace).Update(ctx, svc, metav1.UpdateOptions{})
	return err
}

func (c *Controller) applyIngress(ctx context.Context, ing *networkingv1.Ingress) error {
	_, err := c.clientset.NetworkingV1().Ingresses(ing.Namespace).Create(ctx, ing, metav1.CreateOptions{})
	if !kerrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.clientset.NetworkingV1().Ingresses(ing.Namespace).Get(ctx, ing.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get existing ingress: %w", err)
	}
	ing.ResourceVersion = existing.ResourceVersion
	_, err = c.clientset.NetworkingV1().Ingresses(ing.Namespace).Update(ctx, ing, metav1.UpdateOptions{})
	return err
}

// applyCRD creates a custom resource via the dynamic client, treating
// AlreadyExists as a no-op (cert-manager resources are idempotent here).
func (c *Controller) applyCRD(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	var rc dynamic.ResourceInterface
	if ns := obj.GetNamespace(); ns != "" {
		rc = c.dynClient.Resource(gvr).Namespace(ns)
	} else {
		rc = c.dynClient.Resource(gvr)
	}
	_, err := rc.Create(ctx, obj, metav1.CreateOptions{})
	if kerrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// waitForDeployment watches the named Deployment until at least one replica is
// Available or the context deadline fires. Uses a Watch rather than polling to
// avoid hammering the API server.
func (c *Controller) waitForDeployment(ctx context.Context, namespace, name string) error {
	timeout := int64(300) // 5-minute guard; context deadline takes precedence
	watcher, err := c.clientset.AppsV1().Deployments(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector:  "metadata.name=" + name,
		TimeoutSeconds: &timeout,
	})
	if err != nil {
		return fmt.Errorf("start watch: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed before %s/%s became available", namespace, name)
			}
			dep, ok := event.Object.(*appsv1.Deployment)
			if !ok {
				continue
			}
			if dep.Status.AvailableReplicas >= 1 {
				return nil
			}
		}
	}
}
