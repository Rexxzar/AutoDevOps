package k8s

import (
	"context"
	"fmt"
	"log/slog"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
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
// Namespace, optionally a Secret for secretFiles, Deployment (app container +
// health-checker sidecar), Service, cert-manager ClusterIssuer + Certificate
// (best-effort — silently skipped if cert-manager CRDs are not installed),
// and Ingress. Blocks until at least one pod reports Available or the context
// deadline is reached.
func (c *Controller) KubeDeploy(ctx context.Context, name, namespace, imageRef, domain string, extraEnv map[string]string, secretFiles map[string][]byte, k8sConfigJSON []byte) error {
	cfg := parseK8sConfig(k8sConfigJSON)

	// When a real public domain is set, central nginx terminates TLS and
	// proxies to this ingress over plain HTTP — the ingress host is the public
	// domain and no self-signed cert-manager cert is needed. Otherwise fall
	// back to the dev "<app>.<ingressDomain>" host with a self-signed cert.
	publicDomain := domain != ""
	host := name + "." + c.ingressDomain
	if publicDomain {
		host = domain
	}

	slog.Info("k8s: deploying", "name", name, "namespace", namespace, "image", imageRef, "host", host, "public_domain", publicDomain)

	if err := c.applyNamespace(ctx, buildResourceNamespace(name, namespace)); err != nil {
		return fmt.Errorf("k8s: apply namespace %s: %w", namespace, err)
	}
	if len(secretFiles) > 0 {
		if err := c.applySecret(ctx, buildSecret(name, namespace, secretFiles)); err != nil {
			return fmt.Errorf("k8s: apply secret %s: %w", name, err)
		}
	}
	if err := c.applyDeployment(ctx, buildDeployment(name, namespace, imageRef, extraEnv, secretFiles, cfg)); err != nil {
		return fmt.Errorf("k8s: apply deployment %s: %w", name, err)
	}
	if err := c.applyService(ctx, buildService(name, namespace)); err != nil {
		return fmt.Errorf("k8s: apply service %s: %w", name, err)
	}

	if cfg != nil && cfg.HPA != nil {
		if err := c.applyHPA(ctx, buildHPA(name, namespace, *cfg.HPA)); err != nil {
			return fmt.Errorf("k8s: apply hpa %s: %w", name, err)
		}
	}

	// cert-manager self-signed resources are only used for the dev (non-public)
	// host — skipped entirely when nginx owns the real cert.
	if !publicDomain {
		if err := c.applyCRD(ctx, issuerGVR, buildClusterIssuer()); err != nil {
			slog.Warn("k8s: cluster issuer not applied (cert-manager may not be installed)", "error", err)
		}
		if err := c.applyCRD(ctx, certGVR, buildCertificate(name, namespace, host)); err != nil {
			slog.Warn("k8s: certificate not applied (cert-manager may not be installed)", "error", err)
		}
	}

	if err := c.applyIngress(ctx, buildIngress(name, namespace, host, publicDomain)); err != nil {
		return fmt.Errorf("k8s: apply ingress %s: %w", name, err)
	}

	slog.Info("k8s: waiting for deployment to become available", "name", name, "namespace", namespace)
	if err := c.waitForDeployment(ctx, namespace, name); err != nil {
		return fmt.Errorf("k8s: wait for deployment %s/%s: %w", namespace, name, err)
	}

	slog.Info("k8s: deployment available", "name", name, "namespace", namespace, "host", host)
	return nil
}

// KubeDeployResource applies the K8s resources for a standalone "local"
// Resource (provider='local', resource_type='k8s'): Namespace, Deployment
// (single container, caller-supplied port, no sidecar), and Service. When
// exposeHTTP is true, also applies the same cert-manager ClusterIssuer /
// Certificate / Ingress as KubeDeploy. Blocks until at least one pod reports
// Available. Returns the Service's ClusterIP and (if exposed) the ingress URL
// for the caller to persist as resource outputs.
func (c *Controller) KubeDeployResource(ctx context.Context, name, namespace, imageRef string, port int32, exposeHTTP bool) (serviceIP, ingressURL string, err error) {
	slog.Info("k8s: deploying resource", "name", name, "namespace", namespace, "image", imageRef, "port", port, "expose_http", exposeHTTP)

	if err := c.applyNamespace(ctx, buildResourceNamespace(name, namespace)); err != nil {
		return "", "", fmt.Errorf("k8s: apply namespace %s: %w", namespace, err)
	}
	if err := c.applyDeployment(ctx, buildResourceDeployment(name, namespace, imageRef, port, nil, "", nil)); err != nil {
		return "", "", fmt.Errorf("k8s: apply deployment %s: %w", name, err)
	}
	if err := c.applyService(ctx, buildResourceService(name, namespace, port)); err != nil {
		return "", "", fmt.Errorf("k8s: apply service %s: %w", name, err)
	}

	svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("k8s: get service %s: %w", name, err)
	}
	serviceIP = svc.Spec.ClusterIP

	if exposeHTTP {
		host := name + "." + c.ingressDomain
		ingressURL = "https://" + host

		// cert-manager resources are best-effort: the platform still works over
		// plain HTTP if cert-manager is not installed in the cluster.
		if err := c.applyCRD(ctx, issuerGVR, buildClusterIssuer()); err != nil {
			slog.Warn("k8s: cluster issuer not applied (cert-manager may not be installed)", "error", err)
		}
		if err := c.applyCRD(ctx, certGVR, buildCertificate(name, namespace, host)); err != nil {
			slog.Warn("k8s: certificate not applied (cert-manager may not be installed)", "error", err)
		}
		if err := c.applyIngress(ctx, buildIngress(name, namespace, host, false)); err != nil {
			return "", "", fmt.Errorf("k8s: apply ingress %s: %w", name, err)
		}
	}

	slog.Info("k8s: waiting for resource deployment to become available", "name", name, "namespace", namespace)
	if err := c.waitForDeployment(ctx, namespace, name); err != nil {
		return "", "", fmt.Errorf("k8s: wait for deployment %s/%s: %w", namespace, name, err)
	}

	slog.Info("k8s: resource deployment available", "name", name, "namespace", namespace)
	return serviceIP, ingressURL, nil
}

// KubeDeployImageApp applies the K8s resources for a Phase-4 "image"-kind app
// (e.g. a postgres:16 db container) within a stack's existing namespace:
// optionally a PVC (buildResourcePVC, when volumePath is set), optionally a
// Secret for secretFiles, Deployment (buildResourceDeployment —
// caller-supplied port + env + volume/secret mounts, no sidecar) and Service
// (buildResourceService — port 80 -> the container's port, so resolveLinks'
// local_k8s->local_k8s case needs no special-casing for image apps). No
// Ingress is created — image apps are link targets for sibling apps, not
// externally exposed. Blocks until at least one pod reports Available.
func (c *Controller) KubeDeployImageApp(ctx context.Context, name, namespace, imageRef string, port int32, env map[string]string, volumePath string, secretFiles map[string][]byte) error {
	slog.Info("k8s: deploying image app", "name", name, "namespace", namespace, "image", imageRef, "port", port, "volume_path", volumePath)

	if err := c.applyNamespace(ctx, buildResourceNamespace(name, namespace)); err != nil {
		return fmt.Errorf("k8s: apply namespace %s: %w", namespace, err)
	}
	if volumePath != "" {
		if err := c.applyPVC(ctx, buildResourcePVC(name, namespace)); err != nil {
			return fmt.Errorf("k8s: apply pvc %s: %w", name, err)
		}
	}
	if len(secretFiles) > 0 {
		if err := c.applySecret(ctx, buildSecret(name, namespace, secretFiles)); err != nil {
			return fmt.Errorf("k8s: apply secret %s: %w", name, err)
		}
	}
	if err := c.applyDeployment(ctx, buildResourceDeployment(name, namespace, imageRef, port, env, volumePath, secretFiles)); err != nil {
		return fmt.Errorf("k8s: apply deployment %s: %w", name, err)
	}
	if err := c.applyService(ctx, buildResourceService(name, namespace, port)); err != nil {
		return fmt.Errorf("k8s: apply service %s: %w", name, err)
	}

	slog.Info("k8s: waiting for image app deployment to become available", "name", name, "namespace", namespace)
	if err := c.waitForDeployment(ctx, namespace, name); err != nil {
		return fmt.Errorf("k8s: wait for deployment %s/%s: %w", namespace, name, err)
	}

	slog.Info("k8s: image app deployment available", "name", name, "namespace", namespace)
	return nil
}

// KubeDeleteApp removes a single app's Deployment, Service, Ingress, and
// cert-manager Certificate from a shared stack namespace — the namespace
// itself is left intact since sibling apps in the same stack may still be
// using it (whole-stack teardown calls KubeDestroyResource on the namespace
// separately, once every app has been removed). Missing resources are
// treated as already-removed, not an error.
func (c *Controller) KubeDeleteApp(ctx context.Context, namespace, name string) error {
	if err := c.clientset.AppsV1().Deployments(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("k8s: delete deployment %s/%s: %w", namespace, name, err)
	}
	if err := c.clientset.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("k8s: delete service %s/%s: %w", namespace, name, err)
	}
	if err := c.clientset.AutoscalingV2().HorizontalPodAutoscalers(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("k8s: delete hpa %s/%s: %w", namespace, name, err)
	}
	if err := c.removeIngress(ctx, namespace, name); err != nil {
		return fmt.Errorf("k8s: %w", err)
	}
	return nil
}

// KubeDestroyResource deletes the namespace for a standalone "local" Resource,
// cascading to its Deployment/Service/Ingress/Certificate. A missing namespace
// is treated as already-removed, not an error.
func (c *Controller) KubeDestroyResource(ctx context.Context, namespace string) error {
	if err := c.clientset.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("k8s: delete namespace %s: %w", namespace, err)
	}
	return nil
}

// KubeUpdateResource edits a standalone "local" Resource in place — it's a
// thin wrapper over KubeDeployResource, which already create-or-updates the
// Namespace/Deployment/Service/Ingress idempotently. The one thing
// KubeDeployResource never does is remove resources, so if exposeHTTP has
// been toggled off since the last apply, the previously-created Ingress and
// Certificate are deleted here.
func (c *Controller) KubeUpdateResource(ctx context.Context, name, namespace, imageRef string, port int32, exposeHTTP bool) (serviceIP, ingressURL string, err error) {
	serviceIP, ingressURL, err = c.KubeDeployResource(ctx, name, namespace, imageRef, port, exposeHTTP)
	if err != nil {
		return "", "", err
	}
	if !exposeHTTP {
		if err := c.removeIngress(ctx, namespace, name); err != nil {
			slog.Warn("k8s: remove ingress after expose_http disabled", "name", name, "namespace", namespace, "error", err)
		}
	}
	return serviceIP, ingressURL, nil
}

// removeIngress deletes the Ingress and cert-manager Certificate (named
// name+"-tls", per buildCertificate) for a standalone "local" Resource —
// used by KubeUpdateResource when expose_http is toggled off. Missing
// resources are treated as already-removed, not an error.
func (c *Controller) removeIngress(ctx context.Context, namespace, name string) error {
	if err := c.clientset.NetworkingV1().Ingresses(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("delete ingress %s: %w", name, err)
	}
	if err := c.dynClient.Resource(certGVR).Namespace(namespace).Delete(ctx, name+"-tls", metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("delete certificate %s: %w", name+"-tls", err)
	}
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

// applyPVC creates a PersistentVolumeClaim, treating AlreadyExists as a no-op
// — PVC specs (size, storage class, access modes) are immutable after
// creation, so unlike applyDeployment/applyService there is no update path.
func (c *Controller) applyPVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	_, err := c.clientset.CoreV1().PersistentVolumeClaims(pvc.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if kerrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// applySecret creates a Secret, and on AlreadyExists fetches the existing
// object's ResourceVersion and updates in place — mirrors applyService.
// Secrets are mutable by default (Immutable left unset), so re-applies with
// changed Data (e.g. after handleUpdateSecretFiles) take effect on the next
// pod restart/redeploy.
func (c *Controller) applySecret(ctx context.Context, secret *corev1.Secret) error {
	_, err := c.clientset.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if !kerrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := c.clientset.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get existing secret: %w", err)
	}
	secret.ResourceVersion = existing.ResourceVersion
	_, err = c.clientset.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
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

// applyHPA creates or updates a HorizontalPodAutoscaler, mirroring
// applyService's AlreadyExists→Get(ResourceVersion)→Update pattern.
func (c *Controller) applyHPA(ctx context.Context, hpa *autoscalingv2.HorizontalPodAutoscaler) error {
	api := c.clientset.AutoscalingV2().HorizontalPodAutoscalers(hpa.Namespace)
	_, err := api.Create(ctx, hpa, metav1.CreateOptions{})
	if !kerrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := api.Get(ctx, hpa.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get existing hpa: %w", err)
	}
	hpa.ResourceVersion = existing.ResourceVersion
	_, err = api.Update(ctx, hpa, metav1.UpdateOptions{})
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
