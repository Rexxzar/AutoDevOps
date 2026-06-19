package deploy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rexxzar/autodevops/internal/sse"
)

// githubSignaturePrefix is the prefix GitHub puts on its X-Hub-Signature-256
// header value: "sha256=<hex-encoded HMAC-SHA256 of the raw request body>".
const githubSignaturePrefix = "sha256="

// VerifyWebhookSignature checks a GitHub webhook delivery's X-Hub-Signature-256
// header against the raw request body, HMAC-SHA256'd with the deployment's
// per-webhook secret. Uses hmac.Equal for constant-time comparison — the
// standard defence against timing attacks on signature checks. Pure function,
// independently testable with hand-computed vectors.
func VerifyWebhookSignature(payload []byte, signatureHeader string, secret []byte) bool {
	hexDigest, ok := strings.CutPrefix(signatureHeader, githubSignaturePrefix)
	if !ok {
		return false
	}

	want, err := hex.DecodeString(hexDigest)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	got := mac.Sum(nil)

	return hmac.Equal(want, got)
}

// namespaceName derives the deterministic K8s namespace for a stack:
// "autodevops-<name>" with underscores→hyphens and lower-cased, capped at 63
// characters to satisfy the DNS label format. Mirrors the same function in
// internal/k8s/manifests.go — kept in sync manually (the two packages cannot
// share it without violating the import boundary rules). Shared by every
// app in the stack that targets local K8s.
func namespaceName(stackName string) string {
	ns := "autodevops-" + strings.ToLower(strings.ReplaceAll(stackName, "_", "-"))
	if len(ns) > 63 {
		ns = ns[:63]
	}
	return ns
}

// ProcessPush is the orchestration entrypoint for a verified GitHub push
// delivery, run in a goroutine by the webhook handler — GitHub times out
// webhook responses after ~10s, so cloning must never block the HTTP response.
// Pipeline: look up app (+ its stack) → decrypt git creds → converge clone
// workspace → detect framework → persist (framework + status=building) →
// build OCI image via Paketo (→ status=deploying) → deploy to the target:
//   - target_kind="local_k8s":    KubeDeployer   → status=running|failed
//   - target_kind="local_docker": DockerDeployer → status=running|failed
//   - target_kind="cloud_vm":     SSH deployer   → status=running|failed
//   - target_kind="cloud_ecs":    ECR push + ECS task redeploy → status=running|failed
//   - target_kind="cloud_aci":    ACR push + ACI terraform redeploy → status=running|failed
//   - other / no deployer wired:  stays at status=deploying
func (o *Orchestrator) ProcessPush(ctx context.Context, appID string) error {
	var (
		stackName         string
		appName           string
		repoURL           string
		branch            string
		targetKind        string
		targetResourceID  *string
		gitCredsSecretID  *string
		buildContext      string
		secretFilesID     *string
		domain            string
		declaredFramework string
		namespaceOverride string
		k8sConfigJSON     []byte
	)
	err := o.pool.QueryRow(ctx, `
		SELECT d.name, da.name, COALESCE(da.repo_url, ''), da.branch, da.target_kind, da.target_resource_id::text, da.git_credentials_secret_id::text, COALESCE(da.build_context, ''), da.secret_files_id::text, COALESCE(da.domain, ''), COALESCE(da.declared_framework, ''), COALESCE(d.namespace_override, ''), da.k8s_config
		FROM deployment_apps da
		JOIN deployments d ON d.id = da.deployment_id
		WHERE da.id = $1
	`, appID).Scan(&stackName, &appName, &repoURL, &branch, &targetKind, &targetResourceID, &gitCredsSecretID, &buildContext, &secretFilesID, &domain, &declaredFramework, &namespaceOverride, &k8sConfigJSON)
	if err != nil {
		return fmt.Errorf("deploy: look up app %s: %w", appID, err)
	}

	var creds *gitCredentials
	if gitCredsSecretID != nil {
		plaintext, err := o.vault.GetByID(ctx, *gitCredsSecretID)
		if err != nil {
			return fmt.Errorf("deploy: decrypt git credentials: %w", err)
		}
		var c gitCredentials
		if err := json.Unmarshal(plaintext, &c); err != nil {
			return fmt.Errorf("deploy: decode git credentials: %w", err)
		}
		creds = &c
	}

	o.publishStep(appID, StepClone, StepActive)
	repoPath, err := o.ensureClone(ctx, appID, repoURL, branch, creds)
	if err != nil {
		o.publishStep(appID, StepClone, StepFailed)
		return fmt.Errorf("deploy: clone %s: %w", repoURL, err)
	}
	o.publishStep(appID, StepClone, StepDone)

	// Auto-write a Procfile based on the declared framework when the repo
	// doesn't already have one — saves the user from having to create it manually.
	if declaredFramework != "" {
		procfilePath := filepath.Join(repoPath, "Procfile")
		if _, statErr := os.Stat(procfilePath); os.IsNotExist(statErr) {
			if content, ok := ProcfileForFramework(declaredFramework); ok {
				if writeErr := os.WriteFile(procfilePath, []byte(content+"\n"), 0o644); writeErr != nil {
					slog.Warn("deploy: write auto-procfile", "framework", declaredFramework, "error", writeErr)
				} else {
					slog.Info("deploy: wrote auto-procfile", "framework", declaredFramework, "app", appName)
				}
			}
		}
	}

	o.publishStep(appID, StepDetect, StepActive)
	framework, err := DetectFramework(repoPath)
	if err != nil {
		o.publishStep(appID, StepDetect, StepFailed)
		return fmt.Errorf("deploy: detect framework for %s: %w", repoPath, err)
	}
	o.publishStep(appID, StepDetect, StepDone)

	// Persist framework + transition to building in a single UPDATE so the
	// dashboard sees the framework and the building status simultaneously.
	dep, err := o.persistFramework(ctx, appID, framework)
	if err != nil {
		return err
	}
	o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})

	extraEnv, err := o.resolveLinks(ctx, appID, stackName, targetKind)
	if err != nil {
		o.publishStep(appID, StepBuild, StepFailed)
		return fmt.Errorf("deploy: resolve links for %s: %w", appID, err)
	}
	// Map generic AUTODEVOPS_* link vars to framework-specific vars (e.g.
	// DATABASE_URL for NestJS/Django, DB_HOST/DB_USERNAME for Laravel).
	extraEnv = applyFrameworkEnv(declaredFramework, extraEnv)

	secretFiles, err := o.loadSecretFiles(ctx, secretFilesID)
	if err != nil {
		o.publishStep(appID, StepBuild, StepFailed)
		return fmt.Errorf("deploy: load secret files for %s: %w", appID, err)
	}

	contextDir := repoPath
	if buildContext != "" {
		contextDir = filepath.Join(repoPath, buildContext)
	}
	if err := o.buildImage(ctx, appID, appName, repoPath, contextDir, declaredFramework, extraEnv); err != nil {
		return err // buildImage already persisted status=failed, published SSE, and marked its step failed
	}

	// Fetch the image_ref that buildImage persisted — both deploy branches need it.
	var imageRef string
	if err := o.pool.QueryRow(ctx,
		"SELECT COALESCE(image_ref,'') FROM deployment_apps WHERE id = $1", appID,
	).Scan(&imageRef); err != nil {
		return fmt.Errorf("deploy: fetch image_ref: %w", err)
	}

	if o.complianceGate != nil {
		o.publishStep(appID, StepCompliance, StepActive)
		if err := o.complianceGate.RunAll(ctx, appID, repoPath, imageRef); err != nil {
			o.publishStep(appID, StepCompliance, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: %w", err)
		}
		o.publishStep(appID, StepCompliance, StepDone)
	} else {
		o.publishStep(appID, StepCompliance, StepSkipped)
	}

	o.publishStep(appID, StepDeploy, StepActive)
	switch {
	case targetKind == "local_k8s" && o.kubeDeployer != nil:
		namespace := namespaceName(stackName)
		if namespaceOverride != "" {
			namespace = namespaceOverride
		}
		if err := o.kubeDeployer.KubeDeploy(ctx, appName, namespace, imageRef, domain, extraEnv, secretFiles, k8sConfigJSON); err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: k8s deploy %s: %w", appName, err)
		}
		dep, err := o.persistRunning(ctx, appID, namespace)
		if err != nil {
			return err
		}
		o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
		o.publishStep(appID, StepDeploy, StepDone)

		// TLS step — central nginx terminates real TLS and proxies to the kind
		// ingress (ProvisionK8s). Same non-fatal semantics as the local_docker
		// branch: a TLS failure leaves the app running but marks tls_status.
		if domain != "" && o.tlsProvisioner != nil {
			slog.Info("tls: provisioning (k8s)", "app_id", appID, "domain", domain)
			if tlsErr := o.tlsProvisioner.ProvisionK8s(ctx, appID, domain, o.publishStep); tlsErr != nil {
				slog.Error("tls: provision failed (k8s)", "app_id", appID, "domain", domain, "error", tlsErr)
				o.hub.Publish(sse.Event{Type: "deploy.log", Payload: LogEvent{AppID: appID, Line: "TLS: " + tlsErr.Error()}})
			}
		} else if domain != "" {
			o.publishStep(appID, StepTLS, StepSkipped)
		}

	case targetKind == "local_docker" && o.dockerDeployer != nil:
		if err := o.dockerDeployer.DockerDeploy(ctx, stackName, appName, imageRef, extraEnv, secretFiles); err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: docker deploy %s: %w", appName, err)
		}
		dep, err := o.persistStatus(ctx, appID, "running")
		if err != nil {
			return err
		}
		o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
		o.publishStep(appID, StepDeploy, StepDone)

		// TLS step — only when a domain is configured and the provisioner is wired.
		// Non-fatal: a TLS failure leaves the container running (already "running"
		// status above) but marks tls_status="cert_failed"|"dns_failed" so the
		// dashboard can surface a "retry TLS" action.
		if domain != "" && o.tlsProvisioner != nil {
			slog.Info("tls: provisioning", "app_id", appID, "domain", domain)
			if tlsErr := o.tlsProvisioner.Provision(ctx, appID, domain, stackName, appName, o.publishStep); tlsErr != nil {
				slog.Error("tls: provision failed", "app_id", appID, "domain", domain, "error", tlsErr)
				o.hub.Publish(sse.Event{Type: "deploy.log", Payload: LogEvent{AppID: appID, Line: "TLS: " + tlsErr.Error()}})
			}
		} else if domain != "" {
			o.publishStep(appID, StepTLS, StepSkipped)
		}

	case targetKind == "cloud_vm":
		if targetResourceID == nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: app %s has target_kind cloud_vm but no target_resource_id", appID)
		}
		res, err := o.loadCloudVMResource(ctx, *targetResourceID)
		if err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: %w", err)
		}
		sha12, err := headSHA12(repoPath)
		if err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: %w", err)
		}
		pushRef, _ := o.imageRefs(appName, sha12)
		if err := o.deployToVM(ctx, stackName, appName, pushRef, extraEnv, res); err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: vm deploy %s: %w", appName, err)
		}
		dep, err := o.persistStatus(ctx, appID, "running")
		if err != nil {
			return err
		}
		o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
		o.publishStep(appID, StepDeploy, StepDone)

	case targetKind == "cloud_ecs":
		if targetResourceID == nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: app %s has target_kind cloud_ecs but no target_resource_id", appID)
		}
		res, err := o.loadECSResource(ctx, *targetResourceID)
		if err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: %w", err)
		}
		sha12, err := headSHA12(repoPath)
		if err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: %w", err)
		}
		pushRef, _ := o.imageRefs(appName, sha12)
		if err := o.deployToECS(ctx, pushRef, extraEnv, res); err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: ecs deploy %s: %w", appName, err)
		}
		dep, err := o.persistStatus(ctx, appID, "running")
		if err != nil {
			return err
		}
		o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
		o.publishStep(appID, StepDeploy, StepDone)

	case targetKind == "cloud_aci":
		if targetResourceID == nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: app %s has target_kind cloud_aci but no target_resource_id", appID)
		}
		res, err := o.loadACIResource(ctx, *targetResourceID)
		if err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: %w", err)
		}
		sha12, err := headSHA12(repoPath)
		if err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: %w", err)
		}
		pushRef, _ := o.imageRefs(appName, sha12)
		if err := o.deployToACI(ctx, appName, pushRef, extraEnv, res); err != nil {
			o.publishStep(appID, StepDeploy, StepFailed)
			dep, _ := o.persistStatus(ctx, appID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: aci deploy %s: %w", appName, err)
		}
		dep, err := o.persistStatus(ctx, appID, "running")
		if err != nil {
			return err
		}
		o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
		o.publishStep(appID, StepDeploy, StepDone)

	default:
		o.publishStep(appID, StepDeploy, StepSkipped)
	}

	return nil
}

// persistFramework updates the framework column and transitions status to
// "building" in one UPDATE — combining the two into a single round-trip
// avoids a momentary state where framework is set but status is still pending.
func (o *Orchestrator) persistFramework(ctx context.Context, appID, framework string) (Deployment, error) {
	var d Deployment
	err := o.pool.QueryRow(ctx, `
		UPDATE deployment_apps
		SET framework = $1, status = 'building', updated_at = now()
		WHERE id = $2
		RETURNING id::text, deployment_id::text, name, COALESCE(repo_url, ''), branch, COALESCE(framework, ''),
		          COALESCE(image_ref, ''), COALESCE(namespace, ''), target_kind, status, updated_at
	`, framework, appID).Scan(
		&d.ID, &d.DeploymentID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
		&d.ImageRef, &d.Namespace, &d.TargetKind, &d.Status, &d.UpdatedAt,
	)
	if err != nil {
		return Deployment{}, fmt.Errorf("deploy: persist framework for %s: %w", appID, err)
	}
	return d, nil
}

// persistStatus updates only the status column — used by buildImage to record
// terminal states (failed) without touching framework or image_ref.
func (o *Orchestrator) persistStatus(ctx context.Context, appID, status string) (Deployment, error) {
	var d Deployment
	err := o.pool.QueryRow(ctx, `
		UPDATE deployment_apps
		SET status = $1, updated_at = now()
		WHERE id = $2
		RETURNING id::text, deployment_id::text, name, COALESCE(repo_url, ''), branch, COALESCE(framework, ''),
		          COALESCE(image_ref, ''), COALESCE(namespace, ''), target_kind, status, updated_at
	`, status, appID).Scan(
		&d.ID, &d.DeploymentID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
		&d.ImageRef, &d.Namespace, &d.TargetKind, &d.Status, &d.UpdatedAt,
	)
	if err != nil {
		return Deployment{}, fmt.Errorf("deploy: persist status %q for %s: %w", status, appID, err)
	}
	return d, nil
}

// persistImageRef records the k8s-resolvable image reference and transitions
// status to "deploying" — called by buildImage after a successful pack build.
func (o *Orchestrator) persistImageRef(ctx context.Context, appID, imageRef string) (Deployment, error) {
	var d Deployment
	err := o.pool.QueryRow(ctx, `
		UPDATE deployment_apps
		SET image_ref = $1, status = 'deploying', updated_at = now()
		WHERE id = $2
		RETURNING id::text, deployment_id::text, name, COALESCE(repo_url, ''), branch, COALESCE(framework, ''),
		          COALESCE(image_ref, ''), COALESCE(namespace, ''), target_kind, status, updated_at
	`, imageRef, appID).Scan(
		&d.ID, &d.DeploymentID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
		&d.ImageRef, &d.Namespace, &d.TargetKind, &d.Status, &d.UpdatedAt,
	)
	if err != nil {
		return Deployment{}, fmt.Errorf("deploy: persist image_ref for %s: %w", appID, err)
	}
	return d, nil
}

// persistRunning sets namespace + status=running after K8s manifests are
// applied and at least one pod is Available.
func (o *Orchestrator) persistRunning(ctx context.Context, appID, namespace string) (Deployment, error) {
	var d Deployment
	err := o.pool.QueryRow(ctx, `
		UPDATE deployment_apps
		SET namespace = $1, status = 'running', updated_at = now()
		WHERE id = $2
		RETURNING id::text, deployment_id::text, name, COALESCE(repo_url, ''), branch, COALESCE(framework, ''),
		          COALESCE(image_ref, ''), COALESCE(namespace, ''), target_kind, status, updated_at
	`, namespace, appID).Scan(
		&d.ID, &d.DeploymentID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
		&d.ImageRef, &d.Namespace, &d.TargetKind, &d.Status, &d.UpdatedAt,
	)
	if err != nil {
		return Deployment{}, fmt.Errorf("deploy: persist running for %s: %w", appID, err)
	}
	return d, nil
}
