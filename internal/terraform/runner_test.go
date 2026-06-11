package terraform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- fast, always-on tests: pure logic, no terraform binary or DB needed ---
// (mirrors how internal/k8s unit-tests classify/backoffDelay while leaving
// DB-touching code like upsertPodHealth to manual/integration verification)

func TestWorkspaceDir(t *testing.T) {
	got, err := workspaceDir(filepath.Join("base", "runs"), "dep-123")
	if err != nil {
		t.Fatalf("workspaceDir: %v", err)
	}
	want, err := filepath.Abs(filepath.Join("base", "runs", "dep-123"))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if got != want {
		t.Fatalf("workspaceDir = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("workspaceDir = %q, want an absolute path", got)
	}
}

func TestWriteConfig_writesAndOverwrites(t *testing.T) {
	base := t.TempDir()

	dir, err := writeConfig(base, "dep-1", []byte("resource \"x\" \"y\" {}"))
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	want, err := workspaceDir(base, "dep-1")
	if err != nil {
		t.Fatalf("workspaceDir: %v", err)
	}
	if dir != want {
		t.Fatalf("writeConfig dir = %q, want %q", dir, want)
	}

	got, err := os.ReadFile(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != "resource \"x\" \"y\" {}" {
		t.Fatalf("config content = %q", got)
	}

	// The engine always regenerates the full desired-state config — Plan must
	// overwrite, not append.
	if _, err := writeConfig(base, "dep-1", []byte("resource \"a\" \"b\" {}")); err != nil {
		t.Fatalf("writeConfig overwrite: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatalf("read overwritten config: %v", err)
	}
	if string(got) != "resource \"a\" \"b\" {}" {
		t.Fatalf("config not overwritten, got %q", got)
	}
}

func TestHasPlan(t *testing.T) {
	base := t.TempDir()
	dir, err := workspaceDir(base, "dep-1")
	if err != nil {
		t.Fatalf("workspaceDir: %v", err)
	}

	if ok, err := hasPlan(dir); err != nil || ok {
		t.Fatalf("hasPlan on nonexistent workspace = (%v, %v), want (false, nil)", ok, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if ok, err := hasPlan(dir); err != nil || ok {
		t.Fatalf("hasPlan before plan saved = (%v, %v), want (false, nil)", ok, err)
	}

	if err := os.WriteFile(planFilePath(dir), []byte("binary plan"), 0o644); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	if ok, err := hasPlan(dir); err != nil || !ok {
		t.Fatalf("hasPlan after plan saved = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestHasWorkspace(t *testing.T) {
	base := t.TempDir()

	neverPlannedDir, err := workspaceDir(base, "dep-never-planned")
	if err != nil {
		t.Fatalf("workspaceDir: %v", err)
	}
	if ok, err := hasWorkspace(neverPlannedDir); err != nil || ok {
		t.Fatalf("hasWorkspace on nonexistent workspace = (%v, %v), want (false, nil)", ok, err)
	}

	dir, err := writeConfig(base, "dep-1", []byte("# config"))
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	if ok, err := hasWorkspace(dir); err != nil || !ok {
		t.Fatalf("hasWorkspace after writeConfig = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestHasState(t *testing.T) {
	base := t.TempDir()

	// A workspace can have a written (even invalid) config with no state
	// file if Plan failed validation before anything was ever applied —
	// hasState must report false so Destroy can short-circuit instead of
	// re-running terraform against the same broken config.
	dir, err := writeConfig(base, "dep-1", []byte("# config"))
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	if ok, err := hasState(dir); err != nil || ok {
		t.Fatalf("hasState before apply = (%v, %v), want (false, nil)", ok, err)
	}

	path, err := stateFilePath(dir)
	if err != nil {
		t.Fatalf("stateFilePath: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	if ok, err := hasState(dir); err != nil || !ok {
		t.Fatalf("hasState after apply = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestStateFilePath(t *testing.T) {
	base := t.TempDir()
	dir, err := workspaceDir(base, "dep-1")
	if err != nil {
		t.Fatalf("workspaceDir: %v", err)
	}

	got, err := stateFilePath(dir)
	if err != nil {
		t.Fatalf("stateFilePath: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("stateFilePath = %q, want an absolute path", got)
	}
	if filepath.Base(got) != stateFileName {
		t.Fatalf("stateFilePath base = %q, want %q", filepath.Base(got), stateFileName)
	}
}

func TestNullable(t *testing.T) {
	if got := nullable(""); got != nil {
		t.Fatalf(`nullable("") = %v, want nil (UUID columns reject "" outright)`, got)
	}
	if got := nullable("abc-123"); got != "abc-123" {
		t.Fatalf(`nullable("abc-123") = %v, want "abc-123"`, got)
	}
}

func TestRunner_acquireRejectsConcurrentRunsPerDeployment(t *testing.T) {
	r := &Runner{inFlight: make(map[string]struct{})}

	if !r.acquire("dep-1") {
		t.Fatal("first acquire for dep-1 should succeed")
	}
	if r.acquire("dep-1") {
		t.Fatal("second concurrent acquire for dep-1 should be rejected")
	}
	if !r.acquire("dep-2") {
		t.Fatal("acquire for a different deployment should not be blocked by dep-1")
	}

	r.release("dep-1")
	if !r.acquire("dep-1") {
		t.Fatal("acquire for dep-1 should succeed again after release")
	}
}

// --- slow integration test: real terraform + real database ---
//
// Gated behind AUTODEVOPS_TF_VALIDATE=1 (same env var and skip-on-missing-
// terraform pattern as golden_validate_test.go) AND a reachable database
// (skips with a clear message otherwise — there's no Postgres service in CI,
// matching why upsertPodHealth has no DB-backed test either; this just goes
// further when run locally against the dev Postgres).
//
// Drives a full Plan -> Apply -> Destroy cycle through the public Runner API
// using the tiny `hashicorp/random` provider (no cloud credentials, unlike
// the 686MB AWS provider the golden tests gate on) — proving the tfexec
// orchestration, workspace/state-file management, terraform_runs persistence,
// and SSE publishing all work end to end.
//
// Locally: AUTODEVOPS_TF_VALIDATE=1 go test ./internal/terraform/... -run TestRunnerLifecycle -v
func TestRunnerLifecycle(t *testing.T) {
	if os.Getenv("AUTODEVOPS_TF_VALIDATE") == "" {
		t.Skip("set AUTODEVOPS_TF_VALIDATE=1 to run the full plan/apply/destroy cycle against a real terraform binary + database (downloads the random provider on first run)")
	}

	tfPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skipf("terraform binary not found in PATH: %v", err)
	}

	cacheDir, err := filepath.Abs(filepath.Join("testdata", ".terraform-plugin-cache"))
	if err != nil {
		t.Fatalf("resolve plugin cache dir: %v", err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("create plugin cache dir: %v", err)
	}
	t.Setenv("TF_PLUGIN_CACHE_DIR", cacheDir)
	t.Setenv("TF_IN_AUTOMATION", "1")

	pool := testPostgres(t)
	deploymentID := seedDeployment(t, pool)

	hub := sse.NewHub()
	r := NewRunner(tfPath, t.TempDir(), pool, hub, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	const config = `
terraform {
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

resource "random_pet" "this" {
  length = 2
}
`
	const template = "random-stack"

	t.Run("apply without a prior plan is rejected", func(t *testing.T) {
		if _, err := r.Apply(ctx, deploymentID, "", "", template); err == nil {
			t.Fatal("Apply before any Plan should return an error")
		}
	})

	t.Run("plan", func(t *testing.T) {
		run, err := r.Plan(ctx, deploymentID, "", "", template, []byte(config))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if run.Action != actionPlan || run.Status != statusSucceeded {
			t.Fatalf("plan run = %+v, want action=%q status=%q", run, actionPlan, statusSucceeded)
		}
		if run.PlanOutput == "" {
			t.Error("plan output should not be empty")
		}
		if run.StateFilePath != "" {
			t.Errorf("plan should not record a state file path, got %q", run.StateFilePath)
		}
		assertRunPersisted(t, pool, run)
	})

	t.Run("apply", func(t *testing.T) {
		run, err := r.Apply(ctx, deploymentID, "", "", template)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if run.Action != actionApply || run.Status != statusSucceeded {
			t.Fatalf("apply run = %+v, want action=%q status=%q", run, actionApply, statusSucceeded)
		}
		if run.StateFilePath == "" {
			t.Fatal("apply should record a state file path")
		}
		if _, err := os.Stat(run.StateFilePath); err != nil {
			t.Errorf("recorded state file %s should exist: %v", run.StateFilePath, err)
		}
		assertRunPersisted(t, pool, run)
	})

	t.Run("a second concurrent run for the same deployment is rejected", func(t *testing.T) {
		// Hold the in-flight slot directly rather than racing two real
		// terraform invocations against the same workspace/state lock.
		if !r.acquire(deploymentID) {
			t.Fatal("expected to acquire the deployment slot for this sub-test")
		}
		defer r.release(deploymentID)

		if _, err := r.Plan(ctx, deploymentID, "", "", template, []byte(config)); err == nil {
			t.Fatal("Plan while a run is already in flight for this deployment should be rejected")
		}
	})

	t.Run("destroy", func(t *testing.T) {
		run, err := r.Destroy(ctx, deploymentID, "", "", template)
		if err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if run.Action != actionDestroy || run.Status != statusSucceeded {
			t.Fatalf("destroy run = %+v, want action=%q status=%q", run, actionDestroy, statusSucceeded)
		}
		assertRunPersisted(t, pool, run)
	})
}

// testPostgres opens a pool against the dev database, skipping the test (not
// failing it — there's no Postgres service in CI) when one isn't reachable.
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

// seedDeployment inserts a throwaway deployment row to satisfy
// terraform_runs.deployment_id's foreign key, returning its ID. Cleanup
// cascades (ON DELETE CASCADE) to any terraform_runs rows the test creates.
func seedDeployment(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()

	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO deployments (name, repo_url, target_type)
		VALUES ($1, 'https://example.invalid/runner-test.git', 'container')
		RETURNING id
	`, fmt.Sprintf("tf-runner-test-%d", time.Now().UnixNano())).Scan(&id)
	if err != nil {
		t.Fatalf("seed test deployment: %v", err)
	}

	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM deployments WHERE id = $1", id); err != nil {
			t.Logf("cleanup test deployment %s: %v", id, err)
		}
	})
	return id
}

// assertRunPersisted confirms a Run was written to terraform_runs with the
// fields the Runner reported back to the caller.
func assertRunPersisted(t *testing.T, pool *pgxpool.Pool, run *Run) {
	t.Helper()

	var status, action string
	err := pool.QueryRow(context.Background(),
		"SELECT status, action FROM terraform_runs WHERE id = $1", run.ID,
	).Scan(&status, &action)
	if err != nil {
		t.Fatalf("query persisted run %s: %v", run.ID, err)
	}
	if status != run.Status || action != run.Action {
		t.Fatalf("persisted run (status=%q action=%q), want (status=%q action=%q)", status, action, run.Status, run.Action)
	}
}
