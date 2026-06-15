package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// composeFileNames are the docker-compose filenames checked, in order, at
// the repo root — matches the Compose CLI's own default search order.
var composeFileNames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

// PreviewCompose clones repoURL@branch into a throwaway workspace, looks for
// a docker-compose file at the repo root, and parses it into a ComposePlan
// for the "Import from docker-compose" UI. No deployment_apps rows are
// created — the frontend re-POSTs the user-confirmed plan through the
// existing create-deployment/add-app/create-link endpoints. gitAuthType is ""
// for public repos, else GitAuthToken/GitAuthSSHKey with the matching
// gitToken/gitSSHKey populated — mirrors gitCredentials' JSON shape without
// exposing that unexported type across the package boundary.
func (o *Orchestrator) PreviewCompose(ctx context.Context, repoURL, branch, gitAuthType, gitToken, gitSSHKey string) (*ComposePlan, error) {
	var creds *gitCredentials
	if gitAuthType != "" {
		creds = &gitCredentials{Type: gitAuthType, Token: gitToken, SSHKey: gitSSHKey}
	}
	auth, err := buildAuth(creds)
	if err != nil {
		return nil, fmt.Errorf("deploy: build git auth: %w", err)
	}

	if err := os.MkdirAll(o.workDir, 0o755); err != nil {
		return nil, fmt.Errorf("deploy: create workspace dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(o.workDir, "compose-preview-*")
	if err != nil {
		return nil, fmt.Errorf("deploy: create temp clone dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if branch == "" {
		branch = "main"
	}
	if _, err := git.PlainCloneContext(ctx, tmpDir, false, &git.CloneOptions{
		URL:           repoURL,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         1,
	}); err != nil {
		return nil, fmt.Errorf("deploy: clone %s (branch %s): %w", repoURL, branch, err)
	}

	var data []byte
	found := false
	for _, name := range composeFileNames {
		b, readErr := os.ReadFile(filepath.Join(tmpDir, name))
		if readErr == nil {
			data = b
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("deploy: no docker-compose file found in repo root (looked for %s)", strings.Join(composeFileNames, ", "))
	}

	plan, err := ParseCompose(data, envFileReaderFor(tmpDir))
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// envFileReaderFor reads env_file paths relative to repoRoot, rejecting any
// that resolve outside it.
func envFileReaderFor(repoRoot string) envFileReader {
	return func(p string) ([]byte, error) {
		full := filepath.Join(repoRoot, p)
		rel, err := filepath.Rel(repoRoot, full)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("path escapes repo root")
		}
		return os.ReadFile(full)
	}
}
