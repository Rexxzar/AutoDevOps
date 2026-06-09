package k8s

import (
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// paketoPort is the default container port for all Paketo-built images.
	// Paketo sets PORT=8080 and all supported runtimes listen on it by default.
	paketoPort = int32(8080)

	ingressClassName = "nginx"
	clusterIssuerName = "selfsigned-issuer"
)

// namespaceName derives the deterministic K8s namespace for a deployment:
// "autodevops-<name>" with underscores→hyphens and lower-cased, capped at 63
// characters to satisfy the DNS label format.
func namespaceName(deploymentName string) string {
	ns := "autodevops-" + strings.ToLower(strings.ReplaceAll(deploymentName, "_", "-"))
	if len(ns) > 63 {
		ns = ns[:63]
	}
	return ns
}

func managedLabels(name string) map[string]string {
	return map[string]string{
		"app":                       name,
		"autodevops.io/managed-by": "autodevops",
	}
}

func buildNamespace(deploymentName string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   namespaceName(deploymentName),
			Labels: managedLabels(deploymentName),
		},
	}
}

func buildDeployment(name, namespace, imageRef string) *appsv1.Deployment {
	replicas := int32(1)
	labels := managedLabels(name)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: imageRef,
							Ports: []corev1.ContainerPort{
								{ContainerPort: paketoPort, Protocol: corev1.ProtocolTCP},
							},
							Env: []corev1.EnvVar{
								{Name: "PORT", Value: "8080"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
							ImagePullPolicy: corev1.PullAlways,
						},
						buildHealthSidecar(name),
					},
				},
			},
		},
	}
}

// buildHealthSidecar returns a lightweight curl sidecar that polls the app's
// root every 30 s and logs the HTTP status — demonstrates the platform injecting
// observability without any application-side changes.
func buildHealthSidecar(appName string) corev1.Container {
	script := `while true; do ` +
		`code=$(curl -sf -o /dev/null -w '%{http_code}' http://localhost:8080/ 2>/dev/null || echo '000'); ` +
		`echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) app=` + appName + ` status=$code"; ` +
		`sleep 30; done`
	return corev1.Container{
		Name:    "health-checker",
		Image:   "curlimages/curl:8",
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{script},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("16Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
		},
	}
}

func buildService(name, namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    managedLabels(name),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports: []corev1.ServicePort{
				{
					Port:       80,
					TargetPort: intstr.FromInt32(paketoPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

func buildIngress(name, namespace, host string) *networkingv1.Ingress {
	pathType := networkingv1.PathTypePrefix
	className := ingressClassName
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    managedLabels(name),
			Annotations: map[string]string{
				"cert-manager.io/cluster-issuer":           clusterIssuerName,
				"nginx.ingress.kubernetes.io/ssl-redirect": "true",
			},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &className,
			TLS: []networkingv1.IngressTLS{
				{Hosts: []string{host}, SecretName: name + "-tls"},
			},
			Rules: []networkingv1.IngressRule{
				{
					Host: host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: name,
											Port: networkingv1.ServiceBackendPort{Number: 80},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// buildCertificate returns a cert-manager Certificate object as Unstructured.
// The cert-manager CRDs may not be installed; all callers treat errors as
// best-effort (log + continue).
func buildCertificate(name, namespace, host string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]interface{}{
				"name":      name + "-tls",
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"secretName": name + "-tls",
				"issuerRef": map[string]interface{}{
					"name": clusterIssuerName,
					"kind": "ClusterIssuer",
				},
				"dnsNames": []interface{}{host},
			},
		},
	}
}

// buildClusterIssuer returns a self-signed ClusterIssuer for local/dev clusters.
func buildClusterIssuer() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "ClusterIssuer",
			"metadata": map[string]interface{}{
				"name": clusterIssuerName,
			},
			"spec": map[string]interface{}{
				"selfSigned": map[string]interface{}{},
			},
		},
	}
}
