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

// ProcessPush is the orchestration entrypoint for a verified GitHub push
// delivery, run in a goroutine by the webhook handler — GitHub times out
// webhook responses after ~10s, so cloning must never block the HTTP response.
// It looks up the deployment itself (mirrors Poller.pollAccount's
// self-contained "look up by ID, decrypt creds, do the work" shape): decrypts
// any stored git credentials, converges the clone workspace to the target
// branch, detects the framework, persists it, and publishes the updated row
// as a single "deploy.status" SSE event.
func (o *Orchestrator) ProcessPush(ctx context.Context, deploymentID string) error {
	var (
		repoURL          string
		branch           string
		gitCredsSecretID *string
	)
	err := o.pool.QueryRow(ctx, `
		SELECT repo_url, branch, git_credentials_secret_id::text
		FROM deployments WHERE id = $1
	`, deploymentID).Scan(&repoURL, &branch, &gitCredsSecretID)
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

	dep, err := o.persistFramework(ctx, deploymentID, framework)
	if err != nil {
		return err
	}

	o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
	return nil
}

func (o *Orchestrator) persistFramework(ctx context.Context, deploymentID, framework string) (Deployment, error) {
	var d Deployment
	err := o.pool.QueryRow(ctx, `
		UPDATE deployments
		SET framework = $1, updated_at = now()
		WHERE id = $2
		RETURNING id::text, name, repo_url, branch, framework, target_type, status, updated_at
	`, framework, deploymentID).Scan(
		&d.ID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework, &d.TargetType, &d.Status, &d.UpdatedAt,
	)
	if err != nil {
		return Deployment{}, fmt.Errorf("deploy: persist framework for %s: %w", deploymentID, err)
	}
	return d, nil
}
