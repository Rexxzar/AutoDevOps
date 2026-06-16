package k8s

import (
	"sort"
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

// NamespaceName derives the deterministic K8s namespace for a deployment:
// "autodevops-<name>" with underscores→hyphens and lower-cased, capped at 63
// characters to satisfy the DNS label format.
func NamespaceName(deploymentName string) string {
	ns := "autodevops-" + strings.ToLower(strings.ReplaceAll(deploymentName, "_", "-"))
	if len(ns) > 63 {
		ns = ns[:63]
	}
	return ns
}

// ResourceNamespace derives the deterministic K8s namespace for a standalone
// "local" Resource (provider='local', resource_type='k8s'):
// "resource-<name>" with underscores→hyphens and lower-cased, capped at 63
// characters. Uses a distinct prefix from namespaceName ("autodevops-...") so
// standalone-resource namespaces can never collide with git-deploy namespaces.
func ResourceNamespace(name string) string {
	ns := "resource-" + strings.ToLower(strings.ReplaceAll(name, "_", "-"))
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

// buildResourceNamespace builds the Namespace for a stack's app or a
// standalone "local" Resource, using the caller-supplied namespace name
// (stack-derived for apps via internal/deploy's namespaceName, or
// ResourceNamespace(name) for standalone Resources).
func buildResourceNamespace(name, namespace string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			Labels: managedLabels(name),
		},
	}
}

// sortedEnvVars converts a map of extra environment variables (T-Phase3
// resolveLinks output) into a deterministically-ordered []corev1.EnvVar —
// sorted by key so repeated applies of the same link set produce an
// identical Deployment spec (avoids spurious diffs/restarts).
func sortedEnvVars(env map[string]string) []corev1.EnvVar {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vars := make([]corev1.EnvVar, 0, len(keys))
	for _, k := range keys {
		vars = append(vars, corev1.EnvVar{Name: k, Value: env[k]})
	}
	return vars
}

// sanitizeSecretKey turns a container path ("/opt/secrets/firebase.json")
// into a valid Kubernetes Secret data key ("opt_secrets_firebase.json") —
// duplicated in internal/docker (as a host filename) per the package
// boundary rule. Secret data keys must match [-._a-zA-Z0-9]+, which this
// always satisfies for normal filesystem paths.
func sanitizeSecretKey(containerPath string) string {
	return strings.ReplaceAll(strings.TrimPrefix(containerPath, "/"), "/", "_")
}

// buildSecret returns the Opaque Secret holding an app's uploaded secret
// files, keyed by sanitizeSecretKey(path) -> file content. Named
// "<name>-secrets" and referenced by the "secret-files" Volume that
// buildDeployment/buildResourceDeployment add when secretFiles is non-empty.
func buildSecret(name, namespace string, files map[string][]byte) *corev1.Secret {
	data := make(map[string][]byte, len(files))
	for path, content := range files {
		data[sanitizeSecretKey(path)] = content
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "-secrets",
			Namespace: namespace,
			Labels:    managedLabels(name),
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

// secretFileVolumeMounts returns one VolumeMount per secret file (sorted by
// path for deterministic re-applies), each referencing the shared
// "secret-files" Volume via SubPath so multiple files can share one Volume
// backed by buildSecret's Secret. Returns nil if secretFiles is empty.
func secretFileVolumeMounts(secretFiles map[string][]byte) []corev1.VolumeMount {
	if len(secretFiles) == 0 {
		return nil
	}
	paths := make([]string, 0, len(secretFiles))
	for path := range secretFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	mounts := make([]corev1.VolumeMount, 0, len(paths))
	for _, path := range paths {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "secret-files",
			MountPath: path,
			SubPath:   sanitizeSecretKey(path),
			ReadOnly:  true,
		})
	}
	return mounts
}

// secretFilesVolume returns the "secret-files" Volume sourced from
// buildSecret's "<name>-secrets" Secret, or nil if secretFiles is empty.
func secretFilesVolume(name string, secretFiles map[string][]byte) *corev1.Volume {
	if len(secretFiles) == 0 {
		return nil
	}
	return &corev1.Volume{
		Name: "secret-files",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: name + "-secrets"},
		},
	}
}

func buildDeployment(name, namespace, imageRef string, extraEnv map[string]string, secretFiles map[string][]byte) *appsv1.Deployment {
	replicas := int32(1)
	labels := managedLabels(name)

	appContainer := corev1.Container{
		Name:  "app",
		Image: imageRef,
		Ports: []corev1.ContainerPort{
			{ContainerPort: paketoPort, Protocol: corev1.ProtocolTCP},
		},
		Env: append([]corev1.EnvVar{
			{Name: "PORT", Value: "8080"},
		}, sortedEnvVars(extraEnv)...),
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
		VolumeMounts:    secretFileVolumeMounts(secretFiles),
	}

	var volumes []corev1.Volume
	if v := secretFilesVolume(name, secretFiles); v != nil {
		volumes = append(volumes, *v)
	}

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
						appContainer,
						buildHealthSidecar(name),
					},
					Volumes: volumes,
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
		Image:   "curlimages/curl:8.13.0",
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

// volumeName returns the deterministic name of a Phase-4 "image"-kind app's
// PVC and Volume — "<name>-data", matching internal/docker's
// "<container>-data" named-volume convention for the same feature.
func volumeName(name string) string {
	return name + "-data"
}

// buildResourcePVC returns a 1Gi ReadWriteOnce PersistentVolumeClaim against
// kind's default "standard" StorageClass (the local-path-provisioner running
// in the local-path-storage namespace — see isSystemNamespace in
// internal/k8s/k8s.go), used as the backing volume when a Phase-4
// "image"-kind app sets volume_path.
func buildResourcePVC(name, namespace string) *corev1.PersistentVolumeClaim {
	storageClass := "standard"
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      volumeName(name),
			Namespace: namespace,
			Labels:    managedLabels(name),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &storageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
}

// buildResourceDeployment is like buildDeployment but for standalone "local"
// Resources (provider='local', resource_type='k8s') and Phase-4 "image"-kind
// apps: the container port is caller-supplied (not hardcoded to the Paketo
// 8080 contract), no PORT env var is injected, and there is no health-checker
// sidecar — arbitrary public images don't follow either convention. env is
// applied as-is (sorted for deterministic re-applies) — e.g. POSTGRES_PASSWORD
// for a postgres:16 image app; pass nil for standalone Resources, which have
// no env-var concept. volumePath, if non-empty, mounts the PVC from
// buildResourcePVC at that path — pass "" for standalone Resources and image
// apps with no persistent storage configured. secretFiles, if non-empty,
// mounts buildSecret's Secret via the "secret-files" Volume — pass nil for
// standalone Resources.
func buildResourceDeployment(name, namespace, imageRef string, port int32, env map[string]string, volumePath string, secretFiles map[string][]byte) *appsv1.Deployment {
	replicas := int32(1)
	labels := managedLabels(name)

	container := corev1.Container{
		Name:  "app",
		Image: imageRef,
		Ports: []corev1.ContainerPort{
			{ContainerPort: port, Protocol: corev1.ProtocolTCP},
		},
		Env: sortedEnvVars(env),
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
		ImagePullPolicy: corev1.PullIfNotPresent,
	}

	var volumes []corev1.Volume
	if volumePath != "" {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: volumeName(name), MountPath: volumePath})
		volumes = append(volumes, corev1.Volume{
			Name: volumeName(name),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: volumeName(name),
				},
			},
		})
	}
	container.VolumeMounts = append(container.VolumeMounts, secretFileVolumeMounts(secretFiles)...)
	if v := secretFilesVolume(name, secretFiles); v != nil {
		volumes = append(volumes, *v)
	}

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
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}
}

// buildResourceService is like buildService but with a caller-supplied
// target port (the standalone Resource's container port, not paketoPort).
func buildResourceService(name, namespace string, port int32) *corev1.Service {
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
					TargetPort: intstr.FromInt32(port),
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
