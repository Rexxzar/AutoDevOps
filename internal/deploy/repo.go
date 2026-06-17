package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/uuid"
)

// skipRepoDirs are directories excluded from ListRepoFiles traversal —
// build artefacts and dependency caches that have no business being edited.
var skipRepoDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".terraform": true, "dist": true, "build": true,
	"__pycache__": true, ".venv": true, "venv": true,
	"target": true, ".gradle": true,
}

// binaryRepoExts are file extensions refused by ReadRepoFile — content that
// can't be meaningfully edited as UTF-8 text in a browser textarea.
var binaryRepoExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".zip": true, ".tar": true, ".gz": true, ".bin": true, ".exe": true,
	".pdf": true, ".mp4": true, ".mp3": true, ".webm": true,
}

const maxRepoFileSize = 500 * 1024 // 500 KB
const maxRepoFiles = 300

// repoDir returns the on-disk workspace path for appID — identical to the
// path ensureClone writes to: workDir/<appID>.
func (o *Orchestrator) repoDir(appID string) string {
	return filepath.Join(o.workDir, appID)
}

// ListRepoFiles returns relative slash-separated paths of all editable files
// in the cloned workspace for appID, skipping binary extensions and dirs like
// node_modules / .git / dist.
func (o *Orchestrator) ListRepoFiles(appID string) ([]string, error) {
	root := o.repoDir(appID)
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("repo not cloned yet — trigger a deploy first")
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipRepoDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if binaryRepoExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= maxRepoFiles {
			return filepath.SkipAll
		}
		return nil
	})
	return files, err
}

// ReadRepoFile reads a file from the workspace. relPath must be relative and
// within the workspace root (path-traversal-safe).
func (o *Orchestrator) ReadRepoFile(appID, relPath string) ([]byte, error) {
	abs, err := o.safeRepoPath(appID, relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("file not found: %s", relPath)
	}
	if info.Size() > maxRepoFileSize {
		return nil, fmt.Errorf("file too large to edit in browser (>500 KB)")
	}
	return os.ReadFile(abs)
}

// WriteRepoFile writes content to a file in the workspace.
func (o *Orchestrator) WriteRepoFile(appID, relPath string, content []byte) error {
	abs, err := o.safeRepoPath(appID, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("repo: create parent dirs: %w", err)
	}
	return os.WriteFile(abs, content, 0o644)
}

// CommitAndPush stages all changes in the workspace, commits with message,
// and pushes to the configured remote using the app's stored git credentials.
func (o *Orchestrator) CommitAndPush(ctx context.Context, appID, message string) error {
	var repoURL, branch string
	var gitCredsSecretID *uuid.UUID
	if err := o.pool.QueryRow(ctx,
		`SELECT COALESCE(repo_url,''), branch, git_credentials_secret_id
		 FROM deployment_apps WHERE id = $1`, appID,
	).Scan(&repoURL, &branch, &gitCredsSecretID); err != nil {
		return fmt.Errorf("repo: load app: %w", err)
	}

	var creds *gitCredentials
	if gitCredsSecretID != nil {
		plaintext, err := o.vault.GetByID(ctx, (*gitCredsSecretID).String())
		if err != nil {
			return fmt.Errorf("repo: load git credentials: %w", err)
		}
		var c gitCredentials
		if err := json.Unmarshal(plaintext, &c); err != nil {
			return fmt.Errorf("repo: parse git credentials: %w", err)
		}
		creds = &c
	}

	auth, err := buildAuth(creds)
	if err != nil {
		return fmt.Errorf("repo: build git auth: %w", err)
	}

	repo, err := git.PlainOpen(o.repoDir(appID))
	if err != nil {
		return fmt.Errorf("repo: open repo: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("repo: open worktree: %w", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return fmt.Errorf("repo: git add: %w", err)
	}
	if _, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "AutoDevOps",
			Email: "autodevops@localhost",
			When:  time.Now(),
		},
	}); err != nil {
		return fmt.Errorf("repo: git commit: %w", err)
	}
	if err := repo.PushContext(ctx, &git.PushOptions{Auth: auth}); err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("repo: git push: %w", err)
	}
	return nil
}

// safeRepoPath joins workspace root + relPath and verifies the result stays
// within the root — prevents path traversal attacks.
func (o *Orchestrator) safeRepoPath(appID, relPath string) (string, error) {
	root := filepath.Clean(o.repoDir(appID))
	abs := filepath.Clean(filepath.Join(root, filepath.FromSlash(relPath)))
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid path: must be within the repository")
	}
	return abs, nil
}
