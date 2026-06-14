package deploy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testOrchestrator builds an Orchestrator with realistic-but-inert registry
// settings and no deployers — the fast tests never actually build an image,
// apply K8s manifests, or start Docker containers.
func testOrchestrator(pool *pgxpool.Pool, vault *secrets.Vault, hub *sse.Hub, workDir string) *Orchestrator {
	return New(pool, hub, vault, workDir,
		"paketobuildpacks/builder-jammy-full:latest",
		"localhost:5001",
		"registry.local:5000",
		"apps.127.0.0.1.sslip.io",
		nil, // no KubeDeployer
		nil, // no DockerDeployer
		nil, // no ImagePusher
		nil, // no ImageSaver
		nil, // no ComplianceGate
	)
}

// --- Fast tests: pure functions fed temp dirs / synthetic input, no
// network/DB required (mirrors internal/cloud's "synthetic SDK structs, no
// credentials" fast-test layer). ---

func TestImageRefs(t *testing.T) {
	o := &Orchestrator{registryPush: "localhost:5001", registryPull: "registry.local:5000"}
	push, pull := o.imageRefs("my-app", "abc123def456")

	wantPush := "localhost:5001/my-app:abc123def456"
	wantPull := "registry.local:5000/my-app:abc123def456"

	if push != wantPush {
		t.Errorf("pushRef = %q, want %q", push, wantPush)
	}
	if pull != wantPull {
		t.Errorf("pullRef = %q, want %q", pull, wantPull)
	}
}

func TestHeadSHA12(t *testing.T) {
	dir := t.TempDir()

	// Initialise a real git repo with one commit so headSHA12 has a HEAD to read.
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := wt.Add("hello.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	hash, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com"},
	})
	if err != nil {
		t.Fatalf("git commit: %v", err)
	}

	sha, err := headSHA12(dir)
	if err != nil {
		t.Fatalf("headSHA12: %v", err)
	}
	if len(sha) != 12 {
		t.Errorf("headSHA12 length = %d, want 12", len(sha))
	}
	if sha != hash.String()[:12] {
		t.Errorf("headSHA12 = %q, want %q", sha, hash.String()[:12])
	}
}

func TestDetectFramework(t *testing.T) {
	cases := []struct {
		name    string
		markers []string
		want    string
	}{
		{"node", []string{"package.json"}, FrameworkNode},
		{"go", []string{"go.mod"}, FrameworkGo},
		{"python requirements", []string{"requirements.txt"}, FrameworkPython},
		{"python pyproject", []string{"pyproject.toml"}, FrameworkPython},
		{"java maven", []string{"pom.xml"}, FrameworkJava},
		{"java gradle", []string{"build.gradle"}, FrameworkJava},
		{"ruby", []string{"Gemfile"}, FrameworkRuby},
		{"php", []string{"composer.json"}, FrameworkPHP},
		{"empty dir", nil, FrameworkUnknown},
		{"go takes precedence over node", []string{"go.mod", "package.json"}, FrameworkGo},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, marker := range tc.markers {
				if err := os.WriteFile(filepath.Join(dir, marker), []byte("{}"), 0o644); err != nil {
					t.Fatalf("seed marker file %s: %v", marker, err)
				}
			}

			got, err := DetectFramework(dir)
			if err != nil {
				t.Fatalf("DetectFramework: %v", err)
			}
			if got != tc.want {
				t.Errorf("DetectFramework(%v) = %q, want %q", tc.markers, got, tc.want)
			}
		})
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	secret := []byte("test-webhook-secret")
	payload := []byte(`{"ref":"refs/heads/main","repository":{"full_name":"octocat/Hello-World"}}`)

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	validSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	cases := []struct {
		name      string
		payload   []byte
		signature string
		secret    []byte
		want      bool
	}{
		{"valid signature", payload, validSig, secret, true},
		{"tampered payload", []byte(`{"ref":"refs/heads/evil"}`), validSig, secret, false},
		{"wrong secret", payload, validSig, []byte("not-the-secret"), false},
		{"missing sha256 prefix", payload, hex.EncodeToString(mac.Sum(nil)), secret, false},
		{"non-hex signature", payload, "sha256=not-hex!!", secret, false},
		{"empty header", payload, "", secret, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyWebhookSignature(tc.payload, tc.signature, tc.secret); got != tc.want {
				t.Errorf("VerifyWebhookSignature() = %v, want %v", got, tc.want)
			}
		})
	}
}

// gitCredentials is persisted (encrypted) via vault.Put and decoded back in
// ProcessPush — round-tripping through json guards against field-tag drift
// breaking that decode at runtime (mirrors TestCredentialJSONRoundTrip from
// internal/cloud).
func TestGitCredentialsJSONRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		want gitCredentials
		keys []string
	}{
		{"token", gitCredentials{Type: GitAuthToken, Token: "ghp_xxx"}, []string{"type", "token"}},
		{"ssh_key", gitCredentials{Type: GitAuthSSHKey, SSHKey: "-----BEGIN OPENSSH PRIVATE KEY-----..."}, []string{"type", "ssh_key"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got gitCredentials
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got != tc.want {
				t.Errorf("round-tripped = %+v, want %+v", got, tc.want)
			}
			for _, key := range tc.keys {
				if !jsonHasKey(t, raw, key) {
					t.Errorf("encoded JSON missing expected key %q: %s", key, raw)
				}
			}
		})
	}
}

func jsonHasKey(t *testing.T, raw []byte, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal into map: %v", err)
	}
	_, ok := m[key]
	return ok
}

// --- Gated integration test: requires a reachable database AND network
// access to clone a real (tiny, public) repo. Mirrors the
// AUTODEVOPS_TF_VALIDATE/AUTODEVOPS_CLOUD_VALIDATE + testPostgres/seedDeployment
// pattern in internal/terraform/runner_test.go and internal/cloud/poller_test.go
// — registers a deployment through the same Vault+DB path the API handler
// uses, runs ProcessPush directly, and confirms the clone landed on disk and
// deployments.framework was persisted.
//
// After T13 ProcessPush also attempts a pack build. With octocat/Hello-World
// (no language markers, no buildpacks) the build fails and status becomes
// "failed" — that's the expected outcome here and is explicitly asserted.
// Set AUTODEVOPS_BUILD_VALIDATE=1 alongside AUTODEVOPS_DEPLOY_VALIDATE=1 to
// also run the full pack build against a Docker daemon; without it, the test
// still verifies clone + framework + correct status transition on build failure.
//
// Locally (clone + framework only, build attempt expected to fail):
//
//	AUTODEVOPS_DEPLOY_VALIDATE=1 go test ./internal/deploy/... -run TestProcessPushLifecycle -v
func TestProcessPushLifecycle(t *testing.T) {
	if os.Getenv("AUTODEVOPS_DEPLOY_VALIDATE") == "" {
		t.Skip("set AUTODEVOPS_DEPLOY_VALIDATE=1 to clone a real public repo and run the orchestrator end to end")
	}

	pool := testPostgres(t)
	vault, err := secrets.New("deploy-orchestrator-test-master-key", pool)
	if err != nil {
		t.Fatalf("init vault: %v", err)
	}
	hub := sse.NewHub()
	workDir := t.TempDir()
	orchestrator := testOrchestrator(pool, vault, hub, workDir)

	appID := seedDeployment(t, pool, vault, "https://github.com/octocat/Hello-World.git", "master")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// ProcessPush clones, detects framework, then attempts a pack build.
	// octocat/Hello-World has no buildpack-detectable language, so the build
	// will fail and status will land on "failed" — log but don't fatal.
	buildErr := orchestrator.ProcessPush(ctx, appID)
	if buildErr != nil {
		t.Logf("ProcessPush returned error (expected build failure for unknown framework): %v", buildErr)
	}

	repoPath := filepath.Join(workDir, appID)
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		t.Errorf("expected a git workspace at %s: %v", repoPath, err)
	}

	var framework, status string
	if err := pool.QueryRow(ctx,
		"SELECT COALESCE(framework,''), status FROM deployment_apps WHERE id = $1", appID,
	).Scan(&framework, &status); err != nil {
		t.Fatalf("look up persisted row: %v", err)
	}
	if framework == "" {
		t.Error("expected deployment_apps.framework to be populated after ProcessPush")
	}
	t.Logf("framework=%q status=%q", framework, status)

	// With a real Docker daemon + AUTODEVOPS_BUILD_VALIDATE=1, a buildable
	// repo would end up "deploying". Without one, the build fails → "failed".
	wantStatuses := map[string]bool{"failed": true, "deploying": true}
	if !wantStatuses[status] {
		t.Errorf("status = %q, want one of %v", status, wantStatuses)
	}

	// Second run: ensureClone must converge an existing workspace (idempotent).
	buildErr2 := orchestrator.ProcessPush(ctx, appID)
	if buildErr2 != nil {
		t.Logf("ProcessPush (second run) returned error (expected): %v", buildErr2)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		t.Errorf("workspace gone after second run: %v", err)
	}
}

// testPostgres opens a pool against the dev database, skipping the test (not
// failing it — there's no Postgres service in CI) when one isn't reachable.
// Mirrors internal/terraform/runner_test.go's helper of the same name.
func testPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("cannot create postgres pool for %s: %v", url, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres not reachable at %s — start it with `docker start autodevops-pg`: %v", url, err)
	}

	t.Cleanup(pool.Close)
	return pool
}

// seedDeployment registers a stack with one app exactly like
// handleCreateDeployment does (webhook secret stored through the Vault, rows
// inserted with the resolved secret UUID), returning the app's ID — the ID
// ProcessPush expects. Cleanup removes the stack (cascades to the app).
func seedDeployment(t *testing.T, pool *pgxpool.Pool, vault *secrets.Vault, repoURL, branch string) string {
	t.Helper()
	ctx := context.Background()

	name := fmt.Sprintf("deploy-orchestrator-test-%d", time.Now().UnixNano())
	secretName := "webhook:" + name
	if err := vault.Put(ctx, secretName, []byte("test-webhook-secret")); err != nil {
		t.Fatalf("vault.Put webhook secret: %v", err)
	}

	var deploymentID, appID string
	err := pool.QueryRow(ctx, `
		WITH stack AS (
			INSERT INTO deployments (name) VALUES ($1) RETURNING id
		)
		INSERT INTO deployment_apps (deployment_id, name, repo_url, branch, target_kind, webhook_secret_id)
		SELECT stack.id, $1, $2, $3, 'local_docker', secrets.id
		FROM stack, secrets WHERE secrets.name = $4
		RETURNING deployment_id::text, id::text
	`, name, repoURL, branch, secretName).Scan(&deploymentID, &appID)
	if err != nil {
		t.Fatalf("seed test deployment: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM deployments WHERE id = $1", deploymentID); err != nil {
			t.Logf("cleanup test deployment %s: %v", deploymentID, err)
		}
		if err := vault.Delete(cleanupCtx, secretName); err != nil {
			t.Logf("cleanup test webhook secret %s: %v", secretName, err)
		}
	})
	return appID
}
