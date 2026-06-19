package k8s

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestNamespaceName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"my-app", "autodevops-my-app"},
		{"my_app", "autodevops-my-app"},
		{"MyApp", "autodevops-myapp"},
		{"MY_APP", "autodevops-my-app"},
	}
	for _, tc := range cases {
		got := NamespaceName(tc.in)
		if got != tc.want {
			t.Errorf("NamespaceName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNamespaceNameLengthCap(t *testing.T) {
	long := strings.Repeat("a", 60)
	got := NamespaceName(long)
	if len(got) > 63 {
		t.Errorf("NamespaceName returned %d chars, max is 63", len(got))
	}
}

func TestBuildDeployment(t *testing.T) {
	dep := buildDeployment("svc", "autodevops-svc", "registry.local:5000/svc:abc123", nil, nil, nil)

	if dep.Name != "svc" {
		t.Errorf("deployment name = %q, want %q", dep.Name, "svc")
	}
	if dep.Namespace != "autodevops-svc" {
		t.Errorf("deployment namespace = %q, want %q", dep.Namespace, "autodevops-svc")
	}

	containers := dep.Spec.Template.Spec.Containers
	if len(containers) != 2 {
		t.Fatalf("expected 2 containers (app + sidecar), got %d", len(containers))
	}

	app := containers[0]
	if app.Name != "app" {
		t.Errorf("first container name = %q, want %q", app.Name, "app")
	}
	if app.Image != "registry.local:5000/svc:abc123" {
		t.Errorf("app image = %q, want image ref", app.Image)
	}
	if len(app.Ports) == 0 || app.Ports[0].ContainerPort != paketoPort {
		t.Errorf("app container port = %d, want %d", app.Ports[0].ContainerPort, paketoPort)
	}

	sidecar := containers[1]
	if sidecar.Name != "health-checker" {
		t.Errorf("sidecar name = %q, want health-checker", sidecar.Name)
	}
	if sidecar.Image != "curlimages/curl:8.13.0" {
		t.Errorf("sidecar image = %q, want curlimages/curl:8.13.0", sidecar.Image)
	}
}

func TestBuildDeploymentExtraEnv(t *testing.T) {
	dep := buildDeployment("frontend", "autodevops-stack", "registry.local:5000/frontend:abc123", map[string]string{
		"AUTODEVOPS_BACKEND_URL":  "http://backend",
		"AUTODEVOPS_BACKEND_HOST": "backend",
	}, nil, nil)

	app := dep.Spec.Template.Spec.Containers[0]
	if app.Env[0].Name != "PORT" || app.Env[0].Value != "8080" {
		t.Fatalf("env[0] = %+v, want PORT=8080 first", app.Env[0])
	}

	want := map[string]string{
		"AUTODEVOPS_BACKEND_URL":  "http://backend",
		"AUTODEVOPS_BACKEND_HOST": "backend",
	}
	got := make(map[string]string)
	for _, e := range app.Env[1:] {
		got[e.Name] = e.Value
	}
	if len(got) != len(want) {
		t.Fatalf("env = %+v, want %+v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env[%q] = %q, want %q", k, got[k], v)
		}
	}

	// Sorted order: AUTODEVOPS_BACKEND_HOST < AUTODEVOPS_BACKEND_URL.
	if app.Env[1].Name != "AUTODEVOPS_BACKEND_HOST" || app.Env[2].Name != "AUTODEVOPS_BACKEND_URL" {
		t.Errorf("env order = [%s, %s], want sorted by key", app.Env[1].Name, app.Env[2].Name)
	}
}

func TestBuildService(t *testing.T) {
	svc := buildService("svc", "autodevops-svc")

	if svc.Name != "svc" || svc.Namespace != "autodevops-svc" {
		t.Errorf("service name/namespace = %q/%q", svc.Name, svc.Namespace)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 80 {
		t.Errorf("service port = %d, want 80", svc.Spec.Ports[0].Port)
	}
}

func TestResourceNamespace(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"my-app", "resource-my-app"},
		{"my_app", "resource-my-app"},
		{"MyApp", "resource-myapp"},
	}
	for _, tc := range cases {
		got := ResourceNamespace(tc.in)
		if got != tc.want {
			t.Errorf("ResourceNamespace(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	long := strings.Repeat("a", 60)
	if got := ResourceNamespace(long); len(got) > 63 {
		t.Errorf("ResourceNamespace returned %d chars, max is 63", len(got))
	}
}

func TestBuildResourceNamespace(t *testing.T) {
	ns := buildResourceNamespace("my-resource", "resource-my-resource")
	if ns.Name != "resource-my-resource" {
		t.Errorf("namespace name = %q, want %q", ns.Name, "resource-my-resource")
	}
	if ns.Labels["autodevops.io/managed-by"] != "autodevops" {
		t.Error("missing autodevops.io/managed-by label")
	}
}

func TestBuildResourceDeployment(t *testing.T) {
	dep := buildResourceDeployment("my-resource", "resource-my-resource", "nginx:latest", 80, nil, "", nil)

	if dep.Name != "my-resource" || dep.Namespace != "resource-my-resource" {
		t.Errorf("deployment name/namespace = %q/%q", dep.Name, dep.Namespace)
	}

	containers := dep.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container (no sidecar), got %d", len(containers))
	}

	app := containers[0]
	if app.Image != "nginx:latest" {
		t.Errorf("app image = %q, want nginx:latest", app.Image)
	}
	if len(app.Ports) != 1 || app.Ports[0].ContainerPort != 80 {
		t.Errorf("app container port = %v, want 80", app.Ports)
	}
	for _, e := range app.Env {
		if e.Name == "PORT" {
			t.Error("PORT env var should not be injected for resource deployments")
		}
	}
}

func TestBuildResourceDeploymentWithVolume(t *testing.T) {
	dep := buildResourceDeployment("pg", "autodevops-stack", "postgres:16", 5432, nil, "/var/lib/postgresql/data", nil)

	pod := dep.Spec.Template.Spec
	if len(pod.Volumes) != 1 || pod.Volumes[0].Name != "pg-data" {
		t.Fatalf("expected volume %q, got %v", "pg-data", pod.Volumes)
	}
	if pod.Volumes[0].PersistentVolumeClaim == nil || pod.Volumes[0].PersistentVolumeClaim.ClaimName != "pg-data" {
		t.Errorf("volume %v missing PVC source pg-data", pod.Volumes[0])
	}

	mounts := pod.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].Name != "pg-data" || mounts[0].MountPath != "/var/lib/postgresql/data" {
		t.Errorf("volume mounts = %v, want pg-data at /var/lib/postgresql/data", mounts)
	}
}

func TestSanitizeSecretKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/opt/secrets/firebase.json", "opt_secrets_firebase.json"},
		{"/etc/app.conf", "etc_app.conf"},
		{"relative/path.txt", "relative_path.txt"},
		{"single.txt", "single.txt"},
	}
	for _, tc := range cases {
		if got := sanitizeSecretKey(tc.in); got != tc.want {
			t.Errorf("sanitizeSecretKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildSecret(t *testing.T) {
	secret := buildSecret("app-plant", "autodevops-stack", map[string][]byte{
		"/opt/secrets/firebase.json": []byte(`{"type":"service_account"}`),
	})

	if secret.Name != "app-plant-secrets" || secret.Namespace != "autodevops-stack" {
		t.Errorf("secret name/namespace = %q/%q", secret.Name, secret.Namespace)
	}
	if secret.Type != corev1.SecretTypeOpaque {
		t.Errorf("secret type = %q, want Opaque", secret.Type)
	}
	got, ok := secret.Data["opt_secrets_firebase.json"]
	if !ok {
		t.Fatalf("secret data missing key %q, got %v", "opt_secrets_firebase.json", secret.Data)
	}
	if string(got) != `{"type":"service_account"}` {
		t.Errorf("secret data = %q, want firebase JSON", got)
	}
}

func TestBuildDeploymentWithSecretFiles(t *testing.T) {
	secretFiles := map[string][]byte{
		"/opt/secrets/firebase.json": []byte("{}"),
	}
	dep := buildDeployment("app-plant", "autodevops-stack", "registry.local:5000/app-plant:abc123", nil, secretFiles, nil)

	pod := dep.Spec.Template.Spec
	if len(pod.Volumes) != 1 || pod.Volumes[0].Name != "secret-files" {
		t.Fatalf("expected secret-files volume, got %v", pod.Volumes)
	}
	if pod.Volumes[0].Secret == nil || pod.Volumes[0].Secret.SecretName != "app-plant-secrets" {
		t.Errorf("volume secret source = %v, want app-plant-secrets", pod.Volumes[0].Secret)
	}

	mounts := pod.Containers[0].VolumeMounts
	if len(mounts) != 1 {
		t.Fatalf("expected 1 volume mount, got %v", mounts)
	}
	if mounts[0].Name != "secret-files" || mounts[0].MountPath != "/opt/secrets/firebase.json" || mounts[0].SubPath != "opt_secrets_firebase.json" || !mounts[0].ReadOnly {
		t.Errorf("volume mount = %+v, want secret-files at /opt/secrets/firebase.json (subpath opt_secrets_firebase.json, read-only)", mounts[0])
	}
}

func TestBuildResourceDeploymentWithSecretFiles(t *testing.T) {
	secretFiles := map[string][]byte{
		"/opt/secrets/firebase.json": []byte("{}"),
	}
	dep := buildResourceDeployment("app-plant", "autodevops-stack", "registry.local:5000/app-plant:abc123", 8080, nil, "/data", secretFiles)

	pod := dep.Spec.Template.Spec
	if len(pod.Volumes) != 2 {
		t.Fatalf("expected 2 volumes (pvc + secret-files), got %v", pod.Volumes)
	}
	var sawPVC, sawSecret bool
	for _, v := range pod.Volumes {
		switch v.Name {
		case "app-plant-data":
			sawPVC = true
		case "secret-files":
			sawSecret = true
			if v.Secret == nil || v.Secret.SecretName != "app-plant-secrets" {
				t.Errorf("secret-files volume source = %v, want app-plant-secrets", v.Secret)
			}
		}
	}
	if !sawPVC || !sawSecret {
		t.Errorf("volumes = %v, want both app-plant-data and secret-files", pod.Volumes)
	}

	mounts := pod.Containers[0].VolumeMounts
	if len(mounts) != 2 {
		t.Fatalf("expected 2 volume mounts (pvc + secret file), got %v", mounts)
	}
}

func TestBuildResourcePVC(t *testing.T) {
	pvc := buildResourcePVC("pg", "autodevops-stack")

	if pvc.Name != "pg-data" || pvc.Namespace != "autodevops-stack" {
		t.Errorf("pvc name/namespace = %q/%q", pvc.Name, pvc.Namespace)
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "standard" {
		t.Errorf("storage class = %v, want \"standard\"", pvc.Spec.StorageClassName)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("access modes = %v, want [ReadWriteOnce]", pvc.Spec.AccessModes)
	}
}

func TestBuildResourceService(t *testing.T) {
	svc := buildResourceService("my-resource", "resource-my-resource", 6379)

	if svc.Name != "my-resource" || svc.Namespace != "resource-my-resource" {
		t.Errorf("service name/namespace = %q/%q", svc.Name, svc.Namespace)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 80 {
		t.Errorf("service port = %v, want 80", svc.Spec.Ports)
	}
	if svc.Spec.Ports[0].TargetPort.IntVal != 6379 {
		t.Errorf("service target port = %v, want 6379", svc.Spec.Ports[0].TargetPort)
	}
}

func TestResourceName(t *testing.T) {
	cases := map[string]string{
		"ShopHup":        "shophup",
		"front":          "front",
		"my_app":         "my-app",
		"App.Plant 2":    "app-plant-2",
		"-weird-":        "weird",
		"":               "app",
		"ALLCAPS":        "allcaps",
	}
	for in, want := range cases {
		if got := resourceName(in); got != want {
			t.Errorf("resourceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildIngress(t *testing.T) {
	ing := buildIngress("svc", "autodevops-svc", "svc.autodevops.local", false)

	if ing.Name != "svc" || ing.Namespace != "autodevops-svc" {
		t.Errorf("ingress name/namespace = %q/%q", ing.Name, ing.Namespace)
	}
	if *ing.Spec.IngressClassName != ingressClassName {
		t.Errorf("ingressClassName = %q, want %q", *ing.Spec.IngressClassName, ingressClassName)
	}
	if len(ing.Spec.TLS) == 0 || ing.Spec.TLS[0].Hosts[0] != "svc.autodevops.local" {
		t.Error("TLS host not set correctly")
	}
	if ing.Annotations["cert-manager.io/cluster-issuer"] != clusterIssuerName {
		t.Error("cert-manager cluster-issuer annotation missing")
	}
	if ing.Annotations["nginx.ingress.kubernetes.io/ssl-redirect"] != "true" {
		t.Error("dev ingress should force ssl-redirect")
	}
}

func TestBuildIngressPublicDomain(t *testing.T) {
	ing := buildIngress("svc", "autodevops-svc", "k8s.example.com", true)

	if ing.Spec.Rules[0].Host != "k8s.example.com" {
		t.Errorf("ingress host = %q, want public domain", ing.Spec.Rules[0].Host)
	}
	// nginx terminates TLS and proxies plain HTTP — the in-cluster ingress
	// must not have a TLS block or force ssl-redirect (would 308-loop).
	if len(ing.Spec.TLS) != 0 {
		t.Errorf("public-domain ingress should have no TLS block, got %v", ing.Spec.TLS)
	}
	if ing.Annotations["nginx.ingress.kubernetes.io/ssl-redirect"] != "false" {
		t.Errorf("public-domain ingress must disable ssl-redirect, got %q", ing.Annotations["nginx.ingress.kubernetes.io/ssl-redirect"])
	}
	if _, ok := ing.Annotations["cert-manager.io/cluster-issuer"]; ok {
		t.Error("public-domain ingress should not carry a cert-manager annotation")
	}
}

func TestBuildDeploymentK8sConfig(t *testing.T) {
	replicas := int32(3)
	dep := buildDeployment("api", "autodevops-stack", "registry.local:5000/api:abc123", nil, nil, &K8sConfig{
		Replicas:      &replicas,
		CPURequest:    "250m",
		MemLimit:      "1Gi",
		Liveness:      &ProbeConfig{Path: "/healthz", Port: 8080},
		Readiness:     &ProbeConfig{Path: "/ready"},
		RollingUpdate: &RollingUpdateConfig{MaxSurge: "2", MaxUnavailable: "0"},
	})

	if *dep.Spec.Replicas != 3 {
		t.Errorf("replicas = %d, want 3", *dep.Spec.Replicas)
	}
	app := dep.Spec.Template.Spec.Containers[0]
	if got := app.Resources.Requests.Cpu().String(); got != "250m" {
		t.Errorf("cpu request = %q, want 250m", got)
	}
	if got := app.Resources.Limits.Memory().String(); got != "1Gi" {
		t.Errorf("mem limit = %q, want 1Gi", got)
	}
	if app.LivenessProbe == nil || app.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Error("liveness probe not set to /healthz")
	}
	if app.ReadinessProbe == nil || app.ReadinessProbe.HTTPGet.Path != "/ready" {
		t.Error("readiness probe not set to /ready")
	}
	if dep.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("strategy type = %q, want RollingUpdate", dep.Spec.Strategy.Type)
	}
	if dep.Spec.Strategy.RollingUpdate.MaxSurge.String() != "2" {
		t.Errorf("maxSurge = %q, want 2", dep.Spec.Strategy.RollingUpdate.MaxSurge.String())
	}
}

func TestBuildDeploymentHPASeedsMinReplicas(t *testing.T) {
	dep := buildDeployment("api", "autodevops-stack", "registry.local:5000/api:abc123", nil, nil, &K8sConfig{
		HPA: &HPAConfig{Min: 2, Max: 5, TargetCPU: 70},
	})
	if *dep.Spec.Replicas != 2 {
		t.Errorf("HPA-managed deployment should seed replicas at HPA.Min (2), got %d", *dep.Spec.Replicas)
	}
}

func TestBuildHPA(t *testing.T) {
	hpa := buildHPA("api", "autodevops-stack", HPAConfig{Min: 2, Max: 6, TargetCPU: 75})
	if hpa.Name != "api" || hpa.Namespace != "autodevops-stack" {
		t.Errorf("hpa name/namespace = %q/%q", hpa.Name, hpa.Namespace)
	}
	if *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 6 {
		t.Errorf("hpa min/max = %d/%d, want 2/6", *hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas)
	}
	if hpa.Spec.ScaleTargetRef.Name != "api" || hpa.Spec.ScaleTargetRef.Kind != "Deployment" {
		t.Errorf("hpa scaleTargetRef = %+v", hpa.Spec.ScaleTargetRef)
	}
	if len(hpa.Spec.Metrics) != 1 || *hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 75 {
		t.Error("hpa CPU target utilisation not set to 75")
	}
}

func TestBuildHPAClamps(t *testing.T) {
	hpa := buildHPA("api", "ns", HPAConfig{Min: 0, Max: 0, TargetCPU: 0})
	if *hpa.Spec.MinReplicas != 1 || hpa.Spec.MaxReplicas != 1 {
		t.Errorf("clamped min/max = %d/%d, want 1/1", *hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas)
	}
	if *hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 80 {
		t.Error("default target CPU should be 80")
	}
}

func TestBuildCertificate(t *testing.T) {
	cert := buildCertificate("svc", "autodevops-svc", "svc.autodevops.local")

	if cert.GetName() != "svc-tls" {
		t.Errorf("certificate name = %q, want svc-tls", cert.GetName())
	}
	if cert.GetNamespace() != "autodevops-svc" {
		t.Errorf("certificate namespace = %q, want autodevops-svc", cert.GetNamespace())
	}
	spec, ok := cert.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("cert spec not a map")
	}
	if spec["secretName"] != "svc-tls" {
		t.Errorf("certificate secretName = %q, want svc-tls", spec["secretName"])
	}
}

func TestBuildClusterIssuer(t *testing.T) {
	issuer := buildClusterIssuer()
	if issuer.GetName() != clusterIssuerName {
		t.Errorf("issuer name = %q, want %q", issuer.GetName(), clusterIssuerName)
	}
	spec, ok := issuer.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("issuer spec not a map")
	}
	if _, hasSelfSigned := spec["selfSigned"]; !hasSelfSigned {
		t.Error("selfSigned key missing from issuer spec")
	}
}
