package deploy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// namespaceName derives the deterministic K8s namespace for a deployment:
// "autodevops-<name>" with underscores→hyphens and lower-cased, capped at 63
// characters to satisfy the DNS label format. Mirrors the same function in
// internal/k8s/manifests.go — kept in sync manually (the two packages cannot
// share it without violating the import boundary rules).
func namespaceName(deploymentName string) string {
	ns := "autodevops-" + strings.ToLower(strings.ReplaceAll(deploymentName, "_", "-"))
	if len(ns) > 63 {
		ns = ns[:63]
	}
	return ns
}

// ProcessPush is the orchestration entrypoint for a verified GitHub push
// delivery, run in a goroutine by the webhook handler — GitHub times out
// webhook responses after ~10s, so cloning must never block the HTTP response.
// Pipeline: look up deployment → decrypt git creds → converge clone workspace →
// detect framework → persist (framework + status=building) → build OCI image via
// Paketo (→ status=deploying) → deploy to the target:
//   - target_type="k8s":        KubeDeployer  → status=running|failed
//   - target_type="container":  DockerDeployer → status=running|failed
//   - other / no deployer wired: stays at status=deploying
func (o *Orchestrator) ProcessPush(ctx context.Context, deploymentID string) error {
	var (
		name             string
		repoURL          string
		branch           string
		targetType       string
		gitCredsSecretID *string
	)
	err := o.pool.QueryRow(ctx, `
		SELECT name, repo_url, branch, target_type, git_credentials_secret_id::text
		FROM deployments WHERE id = $1
	`, deploymentID).Scan(&name, &repoURL, &branch, &targetType, &gitCredsSecretID)
	if err != nil {
		return fmt.Errorf("deploy: look up deployment %s: %w", deploymentID, err)
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

	repoPath, err := o.ensureClone(ctx, deploymentID, repoURL, branch, creds)
	if err != nil {
		return fmt.Errorf("deploy: clone %s: %w", repoURL, err)
	}

	framework, err := DetectFramework(repoPath)
	if err != nil {
		return fmt.Errorf("deploy: detect framework for %s: %w", repoPath, err)
	}

	// Persist framework + transition to building in a single UPDATE so the
	// dashboard sees the framework and the building status simultaneously.
	dep, err := o.persistFramework(ctx, deploymentID, framework)
	if err != nil {
		return err
	}
	o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})

	if err := o.buildImage(ctx, deploymentID, name, repoPath); err != nil {
		return err // buildImage already persisted status=failed and published SSE
	}

	// Fetch the image_ref that buildImage persisted — both deploy branches need it.
	var imageRef string
	if err := o.pool.QueryRow(ctx,
		"SELECT COALESCE(image_ref,'') FROM deployments WHERE id = $1", deploymentID,
	).Scan(&imageRef); err != nil {
		return fmt.Errorf("deploy: fetch image_ref: %w", err)
	}

	switch {
	case targetType == "k8s" && o.kubeDeployer != nil:
		namespace := namespaceName(name)
		if err := o.kubeDeployer.KubeDeploy(ctx, name, namespace, imageRef); err != nil {
			dep, _ := o.persistStatus(ctx, deploymentID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: k8s deploy %s: %w", name, err)
		}
		dep, err := o.persistRunning(ctx, deploymentID, namespace)
		if err != nil {
			return err
		}
		o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})

	case targetType == "container" && o.dockerDeployer != nil:
		if err := o.dockerDeployer.DockerDeploy(ctx, name, imageRef); err != nil {
			dep, _ := o.persistStatus(ctx, deploymentID, "failed")
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			return fmt.Errorf("deploy: docker deploy %s: %w", name, err)
		}
		dep, err := o.persistStatus(ctx, deploymentID, "running")
		if err != nil {
			return err
		}
		o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
	}

	return nil
}

// persistFramework updates the framework column and transitions status to
// "building" in one UPDATE — combining the two into a single round-trip
// avoids a momentary state where framework is set but status is still pending.
func (o *Orchestrator) persistFramework(ctx context.Context, deploymentID, framework string) (Deployment, error) {
	var d Deployment
	err := o.pool.QueryRow(ctx, `
		UPDATE deployments
		SET framework = $1, status = 'building', updated_at = now()
		WHERE id = $2
		RETURNING id::text, name, repo_url, branch, framework,
		          COALESCE(image_ref, ''), COALESCE(namespace, ''), target_type, status, updated_at
	`, framework, deploymentID).Scan(
		&d.ID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
		&d.ImageRef, &d.Namespace, &d.TargetType, &d.Status, &d.UpdatedAt,
	)
	if err != nil {
		return Deployment{}, fmt.Errorf("deploy: persist framework for %s: %w", deploymentID, err)
	}
	return d, nil
}

// persistStatus updates only the status column — used by buildImage to record
// terminal states (failed) without touching framework or image_ref.
func (o *Orchestrator) persistStatus(ctx context.Context, deploymentID, status string) (Deployment, error) {
	var d Deployment
	err := o.pool.QueryRow(ctx, `
		UPDATE deployments
		SET status = $1, updated_at = now()
		WHERE id = $2
		RETURNING id::text, name, repo_url, branch, framework,
		          COALESCE(image_ref, ''), COALESCE(namespace, ''), target_type, status, updated_at
	`, status, deploymentID).Scan(
		&d.ID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
		&d.ImageRef, &d.Namespace, &d.TargetType, &d.Status, &d.UpdatedAt,
	)
	if err != nil {
		return Deployment{}, fmt.Errorf("deploy: persist status %q for %s: %w", status, deploymentID, err)
	}
	return d, nil
}

// persistImageRef records the k8s-resolvable image reference and transitions
// status to "deploying" — called by buildImage after a successful pack build.
func (o *Orchestrator) persistImageRef(ctx context.Context, deploymentID, imageRef string) (Deployment, error) {
	var d Deployment
	err := o.pool.QueryRow(ctx, `
		UPDATE deployments
		SET image_ref = $1, status = 'deploying', updated_at = now()
		WHERE id = $2
		RETURNING id::text, name, repo_url, branch, framework,
		          COALESCE(image_ref, ''), COALESCE(namespace, ''), target_type, status, updated_at
	`, imageRef, deploymentID).Scan(
		&d.ID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
		&d.ImageRef, &d.Namespace, &d.TargetType, &d.Status, &d.UpdatedAt,
	)
	if err != nil {
		return Deployment{}, fmt.Errorf("deploy: persist image_ref for %s: %w", deploymentID, err)
	}
	return d, nil
}

// persistRunning sets namespace + status=running after K8s manifests are
// applied and at least one pod is Available.
func (o *Orchestrator) persistRunning(ctx context.Context, deploymentID, namespace string) (Deployment, error) {
	var d Deployment
	err := o.pool.QueryRow(ctx, `
		UPDATE deployments
		SET namespace = $1, status = 'running', updated_at = now()
		WHERE id = $2
		RETURNING id::text, name, repo_url, branch, framework,
		          COALESCE(image_ref, ''), COALESCE(namespace, ''), target_type, status, updated_at
	`, namespace, deploymentID).Scan(
		&d.ID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
		&d.ImageRef, &d.Namespace, &d.TargetType, &d.Status, &d.UpdatedAt,
	)
	if err != nil {
		return Deployment{}, fmt.Errorf("deploy: persist running for %s: %w", deploymentID, err)
	}
	return d, nil
}
