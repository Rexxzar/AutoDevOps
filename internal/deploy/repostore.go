package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// repoStoreDir is the on-disk workspace for a stored repo (Git Repo Store),
// kept under workDir/repos/<repoID> so it never collides with the per-app
// deploy clones at workDir/<appID>.
func (o *Orchestrator) repoStoreDir(repoID string) string {
	return filepath.Join(o.workDir, "repos", repoID)
}

// RemoveRepoStoreWorkspace deletes a stored repo's on-disk clone. Best-effort —
// called when a repo is removed from the store.
func (o *Orchestrator) RemoveRepoStoreWorkspace(repoID string) {
	_ = os.RemoveAll(o.repoStoreDir(repoID))
}

// repoStoreMeta loads a stored repo's url, branch and (optional) git
// credentials from git_repos. creds is nil for public repos.
func (o *Orchestrator) repoStoreMeta(ctx context.Context, repoID string) (repoURL, branch string, creds *gitCredentials, err error) {
	var credsSecretID *string
	if err = o.pool.QueryRow(ctx,
		`SELECT repo_url, branch, git_credentials_secret_id::text
		 FROM git_repos WHERE id = $1`, repoID,
	).Scan(&repoURL, &branch, &credsSecretID); err != nil {
		return "", "", nil, fmt.Errorf("repo store: load repo %s: %w", repoID, err)
	}
	if credsSecretID != nil {
		plaintext, gErr := o.vault.GetByID(ctx, *credsSecretID)
		if gErr != nil {
			return "", "", nil, fmt.Errorf("repo store: load git credentials: %w", gErr)
		}
		var c gitCredentials
		if jErr := json.Unmarshal(plaintext, &c); jErr != nil {
			return "", "", nil, fmt.Errorf("repo store: parse git credentials: %w", jErr)
		}
		creds = &c
	}
	return repoURL, branch, creds, nil
}

// EnsureRepoStoreClone makes sure workDir/repos/<repoID> exists as a clone of
// the stored repo's branch, cloning it on first access. Unlike the deploy
// path's ensureClone, it does NOT hard-reset an existing clone — so unsaved
// edits made through the repo-store editor survive across page loads. Use
// RefreshRepoStoreClone to explicitly pull the latest remote state.
func (o *Orchestrator) EnsureRepoStoreClone(ctx context.Context, repoID string) (string, error) {
	path := o.repoStoreDir(repoID)
	if _, err := git.PlainOpen(path); err == nil {
		return path, nil
	}

	repoURL, branch, creds, err := o.repoStoreMeta(ctx, repoID)
	if err != nil {
		return "", err
	}
	auth, err := buildAuth(creds)
	if err != nil {
		return "", fmt.Errorf("repo store: build git auth: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("repo store: create workspace dir: %w", err)
	}
	if _, err := git.PlainCloneContext(ctx, path, false, &git.CloneOptions{
		URL:           repoURL,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         1,
	}); err != nil {
		_ = os.RemoveAll(path) // don't leave a half-clone that masks future retries
		return "", fmt.Errorf("repo store: clone %s (branch %s): %w", repoURL, branch, err)
	}
	return path, nil
}

// RefreshResult describes the outcome of a RefreshRepoStoreClone ("pull latest").
type RefreshResult struct {
	Updated bool        `json:"updated"`           // HEAD moved (or freshly cloned)
	OldSHA  string      `json:"old_sha,omitempty"` // HEAD before the pull (short)
	NewSHA  string      `json:"new_sha,omitempty"` // HEAD after the pull (short)
	Commit  *CommitInfo `json:"commit,omitempty"`  // latest commit now checked out
}

// CommitInfo is a compact view of a git commit for the dashboard.
type CommitInfo struct {
	SHA     string `json:"sha"`     // short hash
	Message string `json:"message"` // first line
	Author  string `json:"author"`
	Date    string `json:"date"` // RFC3339
}

// RefreshRepoStoreClone fetches origin/<branch> and hard-resets the workspace
// to it, discarding any local edits — the "pull latest" action. It reports
// whether HEAD actually moved and the latest commit now checked out (the clone
// is shallow, so only the tip commit is available, not a full "commits since"
// list).
func (o *Orchestrator) RefreshRepoStoreClone(ctx context.Context, repoID string) (*RefreshResult, error) {
	repoURL, branch, creds, err := o.repoStoreMeta(ctx, repoID)
	if err != nil {
		return nil, err
	}
	auth, err := buildAuth(creds)
	if err != nil {
		return nil, fmt.Errorf("repo store: build git auth: %w", err)
	}
	path := o.repoStoreDir(repoID)
	repo, err := git.PlainOpen(path)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		// First access: clone it. That IS the update.
		if _, e := o.EnsureRepoStoreClone(ctx, repoID); e != nil {
			return nil, e
		}
		repo, err = git.PlainOpen(path)
		if err != nil {
			return nil, fmt.Errorf("repo store: open workspace %s: %w", path, err)
		}
		ci := headCommitInfo(repo)
		res := &RefreshResult{Updated: true, Commit: ci}
		if ci != nil {
			res.NewSHA = ci.SHA
		}
		return res, nil
	}
	if err != nil {
		return nil, fmt.Errorf("repo store: open workspace %s: %w", path, err)
	}

	oldSHA := ""
	if h, hErr := repo.Head(); hErr == nil {
		oldSHA = shortSHA(h.Hash().String())
	}

	remoteRefSpec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch))
	if fErr := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{remoteRefSpec},
		Auth:       auth,
		Depth:      1,
		Force:      true,
	}); fErr != nil && !errors.Is(fErr, git.NoErrAlreadyUpToDate) {
		return nil, fmt.Errorf("repo store: fetch %s (branch %s): %w", repoURL, branch, fErr)
	}
	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		return nil, fmt.Errorf("repo store: resolve origin/%s: %w", branch, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("repo store: open worktree: %w", err)
	}
	if err := wt.Reset(&git.ResetOptions{Commit: remoteRef.Hash(), Mode: git.HardReset}); err != nil {
		return nil, fmt.Errorf("repo store: reset to origin/%s: %w", branch, err)
	}

	newSHA := shortSHA(remoteRef.Hash().String())
	return &RefreshResult{
		Updated: oldSHA != newSHA,
		OldSHA:  oldSHA,
		NewSHA:  newSHA,
		Commit:  headCommitInfo(repo),
	}, nil
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// headCommitInfo returns a compact view of the repo's current HEAD commit, or
// nil if it cannot be resolved.
func headCommitInfo(repo *git.Repository) *CommitInfo {
	ref, err := repo.Head()
	if err != nil {
		return nil
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil
	}
	msg := strings.TrimSpace(c.Message)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return &CommitInfo{
		SHA:     shortSHA(c.Hash.String()),
		Message: msg,
		Author:  c.Author.Name,
		Date:    c.Author.When.Format(time.RFC3339),
	}
}

// ListStoreFiles ensures the stored repo is cloned, then returns its editable
// file paths.
func (o *Orchestrator) ListStoreFiles(ctx context.Context, repoID string) ([]string, error) {
	root, err := o.EnsureRepoStoreClone(ctx, repoID)
	if err != nil {
		return nil, err
	}
	return listEditableFiles(root)
}

// ReadStoreFile reads a file from the stored repo's workspace.
func (o *Orchestrator) ReadStoreFile(relPath, repoID string) ([]byte, error) {
	return readEditableFile(o.repoStoreDir(repoID), relPath)
}

// WriteStoreFile writes content to a file in the stored repo's workspace
// (saved locally; not committed until CommitAndPushStore).
func (o *Orchestrator) WriteStoreFile(repoID, relPath string, content []byte) error {
	return writeEditableFile(o.repoStoreDir(repoID), relPath, content)
}

// CommitAndPushStore stages all workspace changes for the stored repo, commits
// with message, and pushes to the remote using the repo's stored credentials.
func (o *Orchestrator) CommitAndPushStore(ctx context.Context, repoID, message string) error {
	_, _, creds, err := o.repoStoreMeta(ctx, repoID)
	if err != nil {
		return err
	}
	auth, err := buildAuth(creds)
	if err != nil {
		return fmt.Errorf("repo store: build git auth: %w", err)
	}

	repo, err := git.PlainOpen(o.repoStoreDir(repoID))
	if err != nil {
		return fmt.Errorf("repo store: open repo: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("repo store: open worktree: %w", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return fmt.Errorf("repo store: git add: %w", err)
	}
	if _, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "AutoDevOps",
			Email: "autodevops@localhost",
			When:  time.Now(),
		},
	}); err != nil {
		return fmt.Errorf("repo store: git commit: %w", err)
	}
	if err := repo.PushContext(ctx, &git.PushOptions{Auth: auth}); err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("repo store: git push: %w", err)
	}
	return nil
}
