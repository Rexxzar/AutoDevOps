package terraform

import (
	"fmt"
	"os"
)

// HasState reports whether the workspace for deploymentID/resourceID has a
// terraform state file — i.e. `apply` has succeeded at least once and cloud
// resources may exist. Exactly one of deploymentID/resourceID is non-empty.
func (r *Runner) HasState(deploymentID, resourceID string) (bool, error) {
	dir, err := workspaceDir(r.baseDir, workspaceKey(deploymentID, resourceID))
	if err != nil {
		return false, err
	}
	return hasState(dir)
}

// HasConfig reports whether a main.tf exists for the workspace (Plan has run
// at least once, or config was imported).
func (r *Runner) HasConfig(deploymentID, resourceID string) (bool, error) {
	dir, err := workspaceDir(r.baseDir, workspaceKey(deploymentID, resourceID))
	if err != nil {
		return false, err
	}
	return hasWorkspace(dir)
}

// RemoveWorkspace deletes the entire on-disk workspace (main.tf, state, saved
// plan, .terraform/) for deploymentID/resourceID. Irreversible — callers MUST
// ensure the resources were destroyed first (deleting state while cloud
// resources still exist orphans them). No-op if the directory doesn't exist.
func (r *Runner) RemoveWorkspace(deploymentID, resourceID string) error {
	dir, err := workspaceDir(r.baseDir, workspaceKey(deploymentID, resourceID))
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("terraform: remove workspace %s: %w", dir, err)
	}
	return nil
}

// WriteConfig writes raw HCL into the workspace for resourceID without running
// terraform — used by the "import your own Terraform" flow to seed a workspace
// before the first Plan. The on-disk main.tf becomes the source of truth for
// imported workspaces (they are never regenerated from params).
func (r *Runner) WriteConfig(deploymentID, resourceID string, config []byte) error {
	_, err := writeConfig(r.baseDir, workspaceKey(deploymentID, resourceID), config)
	return err
}

// WorkspaceDir returns the absolute on-disk path of a workspace — used by the
// compliance scanner to run tfsec against the rendered .tf files.
func (r *Runner) WorkspaceDir(deploymentID, resourceID string) (string, error) {
	return workspaceDir(r.baseDir, workspaceKey(deploymentID, resourceID))
}
