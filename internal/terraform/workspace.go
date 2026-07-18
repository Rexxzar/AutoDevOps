package terraform

import (
	"fmt"
	"os"
	"path/filepath"
)

// Workspace files. One persistent directory per deployment: main.tf is
// overwritten on each Plan and re-applied against the SAME local-backend
// state file — matching the "regenerate the full desired-state config, re-
// apply to the same state" model locked for the combined-stack-per-deployment
// engine.
const (
	configFileName = "main.tf"
	planFileName   = "tfplan"
	stateFileName  = "terraform.tfstate"
)

// workspaceDir returns the absolute persistent directory for a deployment's
// terraform workspace, rooted at baseDir. It must be absolute: this same
// path is used both as the terraform process's working directory (via
// tfexec) and as the base for path arguments like -out=<dir>/tfplan — if dir
// were relative, terraform would resolve those arguments relative to its own
// (already-redirected) working directory and double the path.
func workspaceDir(baseDir, deploymentID string) (string, error) {
	dir := filepath.Join(baseDir, deploymentID)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("terraform: resolve workspace dir %s: %w", dir, err)
	}
	return abs, nil
}

// writeConfig ensures the workspace directory exists and (over)writes its
// rendered HCL config, returning the workspace directory path.
func writeConfig(baseDir, deploymentID string, config []byte) (string, error) {
	dir, err := workspaceDir(baseDir, deploymentID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("terraform: create workspace %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFileName), config, 0o644); err != nil {
		return "", fmt.Errorf("terraform: write config to %s: %w", dir, err)
	}
	return dir, nil
}

// planFilePath returns the saved binary plan's path within a workspace dir.
func planFilePath(dir string) string {
	return filepath.Join(dir, planFileName)
}

// readConfig returns the workspace's current main.tf — the exact HCL last
// written by writeConfig (from Engine.Render output, or a hand-edited
// version submitted via the resource detail page's "Plan changes" action).
// Returns a wrapped os.ErrNotExist if the workspace has never been planned.
func readConfig(baseDir, deploymentID string) ([]byte, error) {
	dir, err := workspaceDir(baseDir, deploymentID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, configFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("terraform: read config for %s: %w", deploymentID, os.ErrNotExist)
		}
		return nil, fmt.Errorf("terraform: read config for %s: %w", deploymentID, err)
	}
	return data, nil
}

// hasPlan reports whether a saved plan file exists for the workspace,
// distinguishing "no plan yet" from a real filesystem error.
func hasPlan(dir string) (bool, error) {
	_, err := os.Stat(planFilePath(dir))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("terraform: stat plan file in %s: %w", dir, err)
}

// hasWorkspace reports whether a workspace with a written config exists for
// the deployment, distinguishing "never planned" from a real filesystem error.
func hasWorkspace(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, configFileName))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("terraform: stat workspace config in %s: %w", dir, err)
}

// stateFilePath returns the absolute path to the workspace's local-backend
// state file — the value persisted as terraform_runs.state_file_path.
func stateFilePath(dir string) (string, error) {
	abs, err := filepath.Abs(filepath.Join(dir, stateFileName))
	if err != nil {
		return "", fmt.Errorf("terraform: resolve state file path for %s: %w", dir, err)
	}
	return abs, nil
}

// hasState reports whether a local-backend state file exists for the
// workspace, i.e. whether `terraform apply` has ever succeeded here —
// distinguishing "nothing was ever provisioned" from a real filesystem
// error. A workspace can have a written config (and even a failed plan)
// without ever reaching this point, e.g. if the rendered HCL failed
// provider validation before anything was created.
func hasState(dir string) (bool, error) {
	path, err := stateFilePath(dir)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("terraform: stat state file in %s: %w", dir, err)
}
