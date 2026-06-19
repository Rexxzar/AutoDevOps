package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

// ManifestParams contains everything needed to generate K8s manifests for an
// app without actually applying them — mirrors the parameters KubeDeploy and
// KubeDeployImageApp receive.
type ManifestParams struct {
	Name          string
	Namespace     string
	ImageRef      string
	Domain        string
	Kind          string // "git" or "image"
	Port          int32
	Env           map[string]string
	VolumePath    string
	SecretFiles   map[string][]byte
	K8sConfigJSON []byte
	IngressDomain string
}

// GenerateManifests builds the same K8s objects that KubeDeploy /
// KubeDeployImageApp would apply, and serialises them as a multi-document YAML
// string. Secret data values are redacted ("***") so the output is safe to
// display/download without leaking credentials.
func GenerateManifests(p ManifestParams) (string, error) {
	name := resourceName(p.Name)
	var objects []runtime.Object

	ns := buildResourceNamespace(name, p.Namespace)
	objects = append(objects, ns)

	if p.Kind == "image" {
		if p.VolumePath != "" {
			objects = append(objects, buildResourcePVC(name, p.Namespace))
		}
		if len(p.SecretFiles) > 0 {
			sec := buildSecret(name, p.Namespace, p.SecretFiles)
			redactSecret(sec)
			objects = append(objects, sec)
		}
		objects = append(objects, buildResourceDeployment(name, p.Namespace, p.ImageRef, p.Port, p.Env, p.VolumePath, p.SecretFiles))
		objects = append(objects, buildResourceService(name, p.Namespace, p.Port))
	} else {
		cfg := parseK8sConfig(p.K8sConfigJSON)

		if len(p.SecretFiles) > 0 {
			sec := buildSecret(name, p.Namespace, p.SecretFiles)
			redactSecret(sec)
			objects = append(objects, sec)
		}
		objects = append(objects, buildDeployment(name, p.Namespace, p.ImageRef, p.Env, p.SecretFiles, cfg))
		objects = append(objects, buildService(name, p.Namespace))

		if cfg != nil && cfg.HPA != nil {
			objects = append(objects, buildHPA(name, p.Namespace, *cfg.HPA))
		}

		publicDomain := p.Domain != ""
		host := name + "." + p.IngressDomain
		if publicDomain {
			host = p.Domain
		}
		objects = append(objects, buildIngress(name, p.Namespace, host, publicDomain))
	}

	return marshalMultiDocYAML(objects)
}

// redactSecret replaces all Secret data values with "***" so generated YAML
// never contains actual credentials.
func redactSecret(s *corev1.Secret) {
	for k := range s.Data {
		s.Data[k] = []byte("***")
	}
}

// marshalMultiDocYAML serialises a slice of runtime.Object into a single
// multi-document YAML string separated by "---\n".
func marshalMultiDocYAML(objects []runtime.Object) (string, error) {
	var buf bytes.Buffer
	for i, obj := range objects {
		if i > 0 {
			buf.WriteString("---\n")
		}
		j, err := json.Marshal(obj)
		if err != nil {
			return "", fmt.Errorf("marshal object to json: %w", err)
		}
		y, err := sigsyaml.JSONToYAML(j)
		if err != nil {
			return "", fmt.Errorf("convert json to yaml: %w", err)
		}
		buf.Write(y)
	}
	return buf.String(), nil
}

// ApplyManifestYAML parses a multi-document YAML string and applies each
// recognised K8s object using the same typed create-or-update methods
// KubeDeploy uses. Unrecognised kinds are silently skipped. Returns the count
// of objects applied.
func (c *Controller) ApplyManifestYAML(ctx context.Context, yamlData string) (int, error) {
	decoder := yaml.NewYAMLOrJSONDecoder(io.NopCloser(strings.NewReader(yamlData)), 4096)
	applied := 0

	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return applied, fmt.Errorf("decode yaml document: %w", err)
		}
		if len(raw) == 0 {
			continue
		}

		var meta struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return applied, fmt.Errorf("read object kind: %w", err)
		}

		var applyErr error
		switch meta.Kind {
		case "Namespace":
			var ns corev1.Namespace
			if err := json.Unmarshal(raw, &ns); err != nil {
				return applied, fmt.Errorf("unmarshal Namespace: %w", err)
			}
			applyErr = c.applyNamespace(ctx, &ns)

		case "Deployment":
			var dep appsv1.Deployment
			if err := json.Unmarshal(raw, &dep); err != nil {
				return applied, fmt.Errorf("unmarshal Deployment: %w", err)
			}
			applyErr = c.applyDeployment(ctx, &dep)

		case "Service":
			var svc corev1.Service
			if err := json.Unmarshal(raw, &svc); err != nil {
				return applied, fmt.Errorf("unmarshal Service: %w", err)
			}
			applyErr = c.applyService(ctx, &svc)

		case "Ingress":
			var ing networkingv1.Ingress
			if err := json.Unmarshal(raw, &ing); err != nil {
				return applied, fmt.Errorf("unmarshal Ingress: %w", err)
			}
			applyErr = c.applyIngress(ctx, &ing)

		case "HorizontalPodAutoscaler":
			var hpa autoscalingv2.HorizontalPodAutoscaler
			if err := json.Unmarshal(raw, &hpa); err != nil {
				return applied, fmt.Errorf("unmarshal HPA: %w", err)
			}
			applyErr = c.applyHPA(ctx, &hpa)

		case "Secret":
			var sec corev1.Secret
			if err := json.Unmarshal(raw, &sec); err != nil {
				return applied, fmt.Errorf("unmarshal Secret: %w", err)
			}
			applyErr = c.applySecret(ctx, &sec)

		case "PersistentVolumeClaim":
			var pvc corev1.PersistentVolumeClaim
			if err := json.Unmarshal(raw, &pvc); err != nil {
				return applied, fmt.Errorf("unmarshal PVC: %w", err)
			}
			applyErr = c.applyPVC(ctx, &pvc)

		default:
			continue
		}
		if applyErr != nil {
			return applied, fmt.Errorf("apply %s: %w", meta.Kind, applyErr)
		}
		applied++
	}
	return applied, nil
}

// GetLiveManifests fetches the currently-running K8s objects for an app from
// the cluster and serialises them as multi-document YAML. This shows the
// actual live state (including runtime fields like status, resourceVersion)
// rather than the generated template.
func (c *Controller) GetLiveManifests(ctx context.Context, name, namespace string) (string, error) {
	name = resourceName(name)
	var objects []runtime.Object

	ns, err := c.clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return "", fmt.Errorf("get namespace: %w", err)
	}
	if ns != nil && err == nil {
		ns.ManagedFields = nil
		objects = append(objects, ns)
	}

	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return "", fmt.Errorf("get deployment: %w", err)
	}
	if dep != nil && err == nil {
		dep.ManagedFields = nil
		objects = append(objects, dep)
	}

	svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return "", fmt.Errorf("get service: %w", err)
	}
	if svc != nil && err == nil {
		svc.ManagedFields = nil
		objects = append(objects, svc)
	}

	hpa, err := c.clientset.AutoscalingV2().HorizontalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return "", fmt.Errorf("get hpa: %w", err)
	}
	if hpa != nil && err == nil {
		hpa.ManagedFields = nil
		objects = append(objects, hpa)
	}

	ing, err := c.clientset.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return "", fmt.Errorf("get ingress: %w", err)
	}
	if ing != nil && err == nil {
		ing.ManagedFields = nil
		objects = append(objects, ing)
	}

	sec, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, name+"-secrets", metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return "", fmt.Errorf("get secret: %w", err)
	}
	if sec != nil && err == nil {
		sec.ManagedFields = nil
		redactSecret(sec)
		objects = append(objects, sec)
	}

	pvc, err := c.clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, volumeName(name), metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return "", fmt.Errorf("get pvc: %w", err)
	}
	if pvc != nil && err == nil {
		pvc.ManagedFields = nil
		objects = append(objects, pvc)
	}

	if len(objects) == 0 {
		return "", fmt.Errorf("no K8s resources found for %s in namespace %s", name, namespace)
	}
	return marshalMultiDocYAML(objects)
}
