package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/Rexxzar/autodevops/internal/cicd"
)

// CICDInstallParams carries everything needed to install a GitHub Actions
// workflow into a repo. GitHubToken must be a PAT with both `repo` and
// `workflow` scope (the latter is required by GitHub to push a file under
// .github/workflows/, and the API call needs `repo` to write secrets).
type CICDInstallParams struct {
	AppID         string // deployment_apps.id — embedded in the deploy webhook URL
	Name          string // app name — used in the workflow filename + job labels
	Branch        string
	Framework     string
	RepoURL       string
	GitHubToken   string
	WebhookSecret string // plaintext — set as the AUTODEVOPS_WEBHOOK_SECRET repo secret
	ServerURL     string // set as the AUTODEVOPS_URL repo secret (no trailing slash)
}

// CICDInstallResult reports what the install did, for the UI.
type CICDInstallResult struct {
	WorkflowPath    string   `json:"workflow_path"`
	CommitSHA       string   `json:"commit_sha,omitempty"`
	AlreadyUpToDate bool     `json:"already_up_to_date"`
	SecretsSet      []string `json:"secrets_set"`
}

// InstallCICD clones the repo into a throwaway workspace, commits the generated
// workflow to .github/workflows/, pushes it, then sets the two repository
// secrets the workflow references. It is idempotent: re-running with an
// unchanged workflow skips the commit but still re-applies the secrets.
func (o *Orchestrator) InstallCICD(ctx context.Context, p CICDInstallParams) (*CICDInstallResult, error) {
	owner, repo, ok := parseGitHubRepo(p.RepoURL)
	if !ok {
		return nil, fmt.Errorf("deploy: CI/CD auto-install only supports GitHub repos (got %q)", p.RepoURL)
	}
	if p.GitHubToken == "" {
		return nil, fmt.Errorf("deploy: a GitHub token with repo+workflow scope is required")
	}

	creds := &gitCredentials{Type: GitAuthToken, Token: p.GitHubToken}
	auth, err := buildAuth(creds)
	if err != nil {
		return nil, fmt.Errorf("deploy: build git auth: %w", err)
	}

	if mkErr := os.MkdirAll(o.workDir, 0o755); mkErr != nil {
		return nil, fmt.Errorf("deploy: create workspace dir: %w", mkErr)
	}
	tmp, err := os.MkdirTemp(o.workDir, "cicd-install-*")
	if err != nil {
		return nil, fmt.Errorf("deploy: create temp workspace: %w", err)
	}
	defer os.RemoveAll(tmp)

	gitRepo, err := git.PlainCloneContext(ctx, tmp, false, &git.CloneOptions{
		URL:           p.RepoURL,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(p.Branch),
		SingleBranch:  true,
		Depth:         1,
	})
	if err != nil {
		return nil, fmt.Errorf("deploy: clone %s (branch %s): %w", p.RepoURL, p.Branch, err)
	}

	relPath := ".github/workflows/autodevops-" + sanitizeWorkflowName(p.Name) + ".yml"
	yaml := cicd.Workflow(cicd.WorkflowParams{
		AppID:     p.AppID,
		Name:      p.Name,
		Branch:    p.Branch,
		Framework: p.Framework,
	})
	absPath := filepath.Join(tmp, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, fmt.Errorf("deploy: create workflow dir: %w", err)
	}
	if err := os.WriteFile(absPath, []byte(yaml), 0o644); err != nil {
		return nil, fmt.Errorf("deploy: write workflow file: %w", err)
	}

	result := &CICDInstallResult{WorkflowPath: relPath}

	wt, err := gitRepo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("deploy: open worktree: %w", err)
	}
	if _, err := wt.Add(filepath.FromSlash(relPath)); err != nil {
		return nil, fmt.Errorf("deploy: git add workflow: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("deploy: git status: %w", err)
	}
	if status.IsClean() {
		// Workflow already present and identical — nothing to commit/push.
		result.AlreadyUpToDate = true
	} else {
		commit, err := wt.Commit("ci: add AutoDevOps CI/CD workflow", &git.CommitOptions{
			Author: &object.Signature{Name: "AutoDevOps", Email: "autodevops@localhost", When: time.Now()},
		})
		if err != nil {
			return nil, fmt.Errorf("deploy: git commit workflow: %w", err)
		}
		if err := gitRepo.PushContext(ctx, &git.PushOptions{Auth: auth}); err != nil && err != git.NoErrAlreadyUpToDate {
			return nil, workflowPushError(err)
		}
		result.CommitSHA = commit.String()[:12]
	}

	// Set the two repository secrets the workflow references.
	client := githubClient()
	secrets := []struct{ name, value string }{
		{"AUTODEVOPS_WEBHOOK_SECRET", p.WebhookSecret},
		{"AUTODEVOPS_URL", strings.TrimRight(p.ServerURL, "/")},
	}
	for _, s := range secrets {
		if err := githubSetSecret(ctx, client, p.GitHubToken, owner, repo, s.name, s.value); err != nil {
			return nil, err
		}
		result.SecretsSet = append(result.SecretsSet, s.name)
	}

	return result, nil
}

// workflowPushError adds a hint when a push is rejected for lacking the
// `workflow` token scope (GitHub blocks PAT pushes that touch .github/workflows
// unless the token carries that scope).
func workflowPushError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "workflow") || strings.Contains(strings.ToLower(msg), "refusing to allow") {
		return fmt.Errorf("deploy: push rejected — the GitHub token needs the `workflow` scope to commit .github/workflows files: %w", err)
	}
	return fmt.Errorf("deploy: push workflow: %w", err)
}

// CICDUninstallParams carries what's needed to remove an app's generated
// workflow from its repo. Credentials are passed as plain strings (not the
// unexported gitCredentials type) so internal/api can populate them from the
// vault without crossing the package boundary — same pattern as PreviewCompose.
type CICDUninstallParams struct {
	Name        string // app name — used to locate the workflow filename
	Branch      string
	RepoURL     string
	GitAuthType string // "token" | "ssh_key" | "" (none stored)
	GitToken    string
	GitSSHKey   string
}

// CICDUninstallResult reports what the uninstall did.
type CICDUninstallResult struct {
	WorkflowPath string `json:"workflow_path"`
	Removed      bool   `json:"removed"`
	CommitSHA    string `json:"commit_sha,omitempty"`
}

// UninstallCICD clones the repo, deletes .github/workflows/autodevops-<name>.yml
// if present, and pushes the removal. It is the counterpart to InstallCICD,
// called on app/deployment delete so a later redeploy under a different name
// never leaves a stale workflow firing at a dead webhook. Repository secrets
// are left in place — they're inert once no workflow references them, and
// deleting the shared AUTODEVOPS_* pair could break another live deployment of
// the same repo. Best-effort by design: callers log failures, they don't block
// the delete.
func (o *Orchestrator) UninstallCICD(ctx context.Context, p CICDUninstallParams) (*CICDUninstallResult, error) {
	relPath := ".github/workflows/autodevops-" + sanitizeWorkflowName(p.Name) + ".yml"
	result := &CICDUninstallResult{WorkflowPath: relPath}

	if _, _, ok := parseGitHubRepo(p.RepoURL); !ok {
		// Not a GitHub repo — nothing was ever installed.
		return result, nil
	}
	if p.GitAuthType == "" {
		return nil, fmt.Errorf("deploy: removing the CI/CD workflow needs git credentials, but the repo has none stored")
	}

	auth, err := buildAuth(&gitCredentials{Type: p.GitAuthType, Token: p.GitToken, SSHKey: p.GitSSHKey})
	if err != nil {
		return nil, fmt.Errorf("deploy: build git auth: %w", err)
	}

	if mkErr := os.MkdirAll(o.workDir, 0o755); mkErr != nil {
		return nil, fmt.Errorf("deploy: create workspace dir: %w", mkErr)
	}
	tmp, err := os.MkdirTemp(o.workDir, "cicd-uninstall-*")
	if err != nil {
		return nil, fmt.Errorf("deploy: create temp workspace: %w", err)
	}
	defer os.RemoveAll(tmp)

	gitRepo, err := git.PlainCloneContext(ctx, tmp, false, &git.CloneOptions{
		URL:           p.RepoURL,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(p.Branch),
		SingleBranch:  true,
		Depth:         1,
	})
	if err != nil {
		return nil, fmt.Errorf("deploy: clone %s (branch %s): %w", p.RepoURL, p.Branch, err)
	}

	absPath := filepath.Join(tmp, filepath.FromSlash(relPath))
	if _, statErr := os.Stat(absPath); statErr != nil {
		// No workflow file to remove — nothing to do.
		return result, nil
	}

	wt, err := gitRepo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("deploy: open worktree: %w", err)
	}
	if _, err := wt.Remove(filepath.FromSlash(relPath)); err != nil {
		return nil, fmt.Errorf("deploy: git remove workflow: %w", err)
	}
	commit, err := wt.Commit("ci: remove AutoDevOps CI/CD workflow", &git.CommitOptions{
		Author: &object.Signature{Name: "AutoDevOps", Email: "autodevops@localhost", When: time.Now()},
	})
	if err != nil {
		return nil, fmt.Errorf("deploy: git commit workflow removal: %w", err)
	}
	if err := gitRepo.PushContext(ctx, &git.PushOptions{Auth: auth}); err != nil && err != git.NoErrAlreadyUpToDate {
		return nil, workflowPushError(err)
	}
	result.Removed = true
	result.CommitSHA = commit.String()[:12]
	return result, nil
}

// sanitizeWorkflowName lowercases and replaces non-alphanumeric runs with a
// single dash so the app name is safe in a filename.
func sanitizeWorkflowName(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "app"
	}
	return out
}
