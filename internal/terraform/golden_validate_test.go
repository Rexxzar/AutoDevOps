package terraform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestGoldenTerraformValidate runs the real `terraform fmt -check` and
// `terraform validate` against each rendered golden template — the deep
// semantic check the T07 spec calls for, on top of the fast byte-compare in
// TestGolden.
//
// Gated behind AUTODEVOPS_TF_VALIDATE=1: `terraform init` downloads provider
// plugins, and the AWS provider alone is ~686MB. CI always sets the env var
// and caches TF_PLUGIN_CACHE_DIR so the download happens once, not per run.
// Locally: AUTODEVOPS_TF_VALIDATE=1 go test ./internal/terraform/... -run TestGoldenTerraformValidate
func TestGoldenTerraformValidate(t *testing.T) {
	if os.Getenv("AUTODEVOPS_TF_VALIDATE") == "" {
		t.Skip("set AUTODEVOPS_TF_VALIDATE=1 to run terraform init/fmt/validate against rendered templates (downloads provider plugins on first run, slow)")
	}

	tfPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skipf("terraform binary not found in PATH: %v", err)
	}

	cacheDir := os.Getenv("TF_PLUGIN_CACHE_DIR")
	if cacheDir == "" {
		cacheDir = filepath.Join("testdata", ".terraform-plugin-cache")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("create plugin cache dir %s: %v", cacheDir, err)
	}
	absCacheDir, err := filepath.Abs(cacheDir)
	if err != nil {
		t.Fatalf("resolve plugin cache dir: %v", err)
	}

	e := testEngine(t)

	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := e.Render(tc.id, tc.params)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "main.tf"), out, 0o644); err != nil {
				t.Fatalf("write rendered config: %v", err)
			}

			env := append(os.Environ(),
				"TF_PLUGIN_CACHE_DIR="+absCacheDir,
				"TF_IN_AUTOMATION=1",
			)

			runTerraform(t, tfPath, dir, env, "init", "-input=false", "-backend=false")
			runTerraform(t, tfPath, dir, env, "fmt", "-check", "-diff")
			runTerraform(t, tfPath, dir, env, "validate")
		})
	}
}

func runTerraform(t *testing.T, tfPath, dir string, env []string, args ...string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, tfPath, args...)
	cmd.Dir = dir
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("terraform %v failed: %v\n%s", args, err, out)
	}
}
