package k8s

import (
	"strings"
	"testing"
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
		got := namespaceName(tc.in)
		if got != tc.want {
			t.Errorf("namespaceName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNamespaceNameLengthCap(t *testing.T) {
	long := strings.Repeat("a", 60)
	got := namespaceName(long)
	if len(got) > 63 {
		t.Errorf("namespaceName returned %d chars, max is 63", len(got))
	}
}

func TestBuildNamespace(t *testing.T) {
	ns := buildNamespace("my-app")
	if ns.Name != "autodevops-my-app" {
		t.Errorf("namespace name = %q, want %q", ns.Name, "autodevops-my-app")
	}
	if ns.Labels["autodevops.io/managed-by"] != "autodevops" {
		t.Error("missing autodevops.io/managed-by label")
	}
}

func TestBuildDeployment(t *testing.T) {
	dep := buildDeployment("svc", "autodevops-svc", "registry.local:5000/svc:abc123")

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
	if sidecar.Image != "curlimages/curl:8" {
		t.Errorf("sidecar image = %q, want curlimages/curl:8", sidecar.Image)
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
	dep := buildResourceDeployment("my-resource", "resource-my-resource", "nginx:latest", 80)

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

func TestBuildIngress(t *testing.T) {
	ing := buildIngress("svc", "autodevops-svc", "svc.autodevops.local")

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
