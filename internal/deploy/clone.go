package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// ensureClone makes workDir/<deploymentID> match origin/<branch>'s latest
// commit: cloning fresh if the workspace doesn't exist yet, or fetching +
// hard-resetting if it does. This idempotent "converge to desired state"
// approach avoids accumulating stale clones across repeated pushes — mirrors
// Runner.Plan's "always regenerate the full desired state" philosophy. Returns
// the workspace path on success.
func (o *Orchestrator) ensureClone(ctx context.Context, deploymentID, repoURL, branch string, creds *gitCredentials) (string, error) {
	auth, err := buildAuth(creds)
	if err != nil {
		return "", fmt.Errorf("deploy: build git auth: %w", err)
	}

	path := filepath.Join(o.workDir, deploymentID)
	refName := plumbing.NewBranchReferenceName(branch)

	repo, err := git.PlainOpen(path)
	switch {
	case errors.Is(err, git.ErrRepositoryNotExists):
		if mkErr := os.MkdirAll(o.workDir, 0o755); mkErr != nil {
			return "", fmt.Errorf("deploy: create workspace dir: %w", mkErr)
		}
		if _, cloneErr := git.PlainCloneContext(ctx, path, false, &git.CloneOptions{
			URL:           repoURL,
			Auth:          auth,
			ReferenceName: refName,
			SingleBranch:  true,
			Depth:         1,
		}); cloneErr != nil {
			return "", fmt.Errorf("deploy: clone %s (branch %s): %w", repoURL, branch, cloneErr)
		}
		return path, nil
	case err != nil:
		return "", fmt.Errorf("deploy: open existing workspace %s: %w", path, err)
	}

	remoteRefSpec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch))
	if fetchErr := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{remoteRefSpec},
		Auth:       auth,
		Depth:      1,
		Force:      true,
	}); fetchErr != nil && !errors.Is(fetchErr, git.NoErrAlreadyUpToDate) {
		return "", fmt.Errorf("deploy: fetch %s (branch %s): %w", repoURL, branch, fetchErr)
	}

	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		return "", fmt.Errorf("deploy: resolve origin/%s: %w", branch, err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("deploy: open worktree: %w", err)
	}
	if err := worktree.Reset(&git.ResetOptions{Commit: remoteRef.Hash(), Mode: git.HardReset}); err != nil {
		return "", fmt.Errorf("deploy: reset to origin/%s (%s): %w", branch, remoteRef.Hash(), err)
	}

	return path, nil
}

// buildAuth translates a deployment's stored gitCredentials into a go-git
// transport.AuthMethod. Public repos (creds == nil) need none — matching the
// "ask the user only if the repo is private" framing.
func buildAuth(creds *gitCredentials) (transport.AuthMethod, error) {
	if creds == nil {
		return nil, nil
	}

	switch creds.Type {
	case GitAuthToken:
		if creds.Token == "" {
			return nil, fmt.Errorf("deploy: token auth requires a token")
		}
		// GitHub accepts any non-empty username with a PAT as the password
		// over HTTPS — "x-access-token" is GitHub's own documented convention.
		return &githttp.BasicAuth{Username: "x-access-token", Password: creds.Token}, nil
	case GitAuthSSHKey:
		if creds.SSHKey == "" {
			return nil, fmt.Errorf("deploy: ssh_key auth requires a private key")
		}
		keys, err := ssh.NewPublicKeys("git", []byte(creds.SSHKey), "")
		if err != nil {
			return nil, fmt.Errorf("deploy: parse ssh private key: %w", err)
		}
		return keys, nil
	default:
		return nil, fmt.Errorf("deploy: unknown git auth type %q", creds.Type)
	}
}
