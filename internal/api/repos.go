package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Rexxzar/autodevops/internal/deploy"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/go-chi/chi/v5"
)

// repoListItem is the Git Repo Store list/summary shape.
type repoListItem struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	RepoURL        string         `json:"repo_url"`
	Branch         string         `json:"branch"`
	AuthType       string         `json:"auth_type"` // "", "token", "ssh_key"
	HasCredentials bool           `json:"has_credentials"`
	LastScannedAt  *time.Time     `json:"last_scanned_at"`
	UsedBy         int            `json:"used_by"`
	Findings       map[string]int `json:"findings"` // severity -> count
	CreatedAt      time.Time      `json:"created_at"`
}

// handleListRepos returns every stored repo with its usage count and a
// per-severity finding breakdown from its last manual scan.
func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// severity breakdown per repo, in one pass.
	bySeverity := map[string]map[string]int{}
	if rows, err := s.db.Query(ctx,
		`SELECT repo_id::text, severity, COUNT(*) FROM compliance_findings
		 WHERE repo_id IS NOT NULL GROUP BY repo_id, severity`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, sev string
			var n int
			if err := rows.Scan(&id, &sev, &n); err != nil {
				continue
			}
			if bySeverity[id] == nil {
				bySeverity[id] = map[string]int{}
			}
			bySeverity[id][sev] = n
		}
	}

	rows, err := s.db.Query(ctx, `
		SELECT r.id::text, r.name, r.repo_url, r.branch, COALESCE(r.git_auth_type, ''),
		       r.git_credentials_secret_id IS NOT NULL, r.last_scanned_at, r.created_at,
		       (SELECT COUNT(*) FROM deployment_apps da WHERE da.repo_id = r.id AND da.status != 'destroyed')
		FROM git_repos r
		ORDER BY r.created_at DESC`)
	if err != nil {
		http.Error(w, fmt.Sprintf("list repos: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []repoListItem{}
	for rows.Next() {
		var it repoListItem
		if err := rows.Scan(&it.ID, &it.Name, &it.RepoURL, &it.Branch, &it.AuthType,
			&it.HasCredentials, &it.LastScannedAt, &it.CreatedAt, &it.UsedBy); err != nil {
			http.Error(w, fmt.Sprintf("scan repo: %v", err), http.StatusInternalServerError)
			return
		}
		it.Findings = bySeverity[it.ID]
		if it.Findings == nil {
			it.Findings = map[string]int{}
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateRepo registers a new repo in the store. Body:
// {name?, repo_url, branch?, git_auth?}. name defaults to the repo URL's last
// path segment (de-duplicated). git credentials are encrypted in the vault.
func (s *Server) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Name    string          `json:"name"`
		RepoURL string          `json:"repo_url"`
		Branch  string          `json:"branch"`
		GitAuth *gitAuthRequest `json:"git_auth,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.RepoURL = strings.TrimSpace(req.RepoURL)
	if req.RepoURL == "" {
		http.Error(w, "repo_url is required", http.StatusBadRequest)
		return
	}
	if req.GitAuth != nil && req.GitAuth.Type != deploy.GitAuthToken && req.GitAuth.Type != deploy.GitAuthSSHKey {
		http.Error(w, "git_auth.type must be 'token' or 'ssh_key'", http.StatusBadRequest)
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = "main"
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = deriveRepoName(req.RepoURL)
	}
	name, err := s.uniqueRepoName(ctx, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	repoID, err := s.insertRepo(ctx, name, req.RepoURL, branch, req.GitAuth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.hub.Publish(sse.Event{Type: "repo", Payload: map[string]string{"id": repoID, "name": name}})
	writeJSON(w, http.StatusCreated, map[string]string{"id": repoID, "name": name})
}

// insertRepo creates a git_repos row, encrypting any git credentials into the
// vault under "git-repo:<id>" and linking them back. Returns the new repo id.
func (s *Server) insertRepo(ctx context.Context, name, repoURL, branch string, auth *gitAuthRequest) (string, error) {
	var repoID string
	if err := s.db.QueryRow(ctx,
		`INSERT INTO git_repos (name, repo_url, branch) VALUES ($1, $2, $3) RETURNING id::text`,
		name, repoURL, branch,
	).Scan(&repoID); err != nil {
		return "", fmt.Errorf("insert repo: %w", err)
	}
	if auth != nil {
		if err := s.storeRepoCredentials(ctx, repoID, auth); err != nil {
			return "", err
		}
	}
	return repoID, nil
}

// storeRepoCredentials encrypts git credentials for a repo and links them onto
// the git_repos row.
func (s *Server) storeRepoCredentials(ctx context.Context, repoID string, auth *gitAuthRequest) error {
	raw, err := json.Marshal(map[string]string{
		"type":    auth.Type,
		"token":   auth.Token,
		"ssh_key": auth.SSHKey,
	})
	if err != nil {
		return fmt.Errorf("encode git credentials: %w", err)
	}
	name := "git-repo:" + repoID
	if err := s.vault.Put(ctx, name, raw); err != nil {
		return fmt.Errorf("store git credentials: %w", err)
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE git_repos
		 SET git_auth_type = $2, git_credentials_secret_id = (SELECT id FROM secrets WHERE name = $3), updated_at = now()
		 WHERE id = $1`, repoID, auth.Type, name); err != nil {
		return fmt.Errorf("link git credentials: %w", err)
	}
	return nil
}

// handleGetRepo returns a single repo plus the apps backed by it and its latest
// compliance findings.
func (s *Server) handleGetRepo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	var it repoListItem
	err := s.db.QueryRow(ctx, `
		SELECT r.id::text, r.name, r.repo_url, r.branch, COALESCE(r.git_auth_type, ''),
		       r.git_credentials_secret_id IS NOT NULL, r.last_scanned_at, r.created_at
		FROM git_repos r WHERE r.id = $1`, id,
	).Scan(&it.ID, &it.Name, &it.RepoURL, &it.Branch, &it.AuthType,
		&it.HasCredentials, &it.LastScannedAt, &it.CreatedAt)
	if err != nil {
		http.Error(w, "repo not found", http.StatusNotFound)
		return
	}
	it.Findings = map[string]int{}

	type appUsage struct {
		AppID          string `json:"app_id"`
		AppName        string `json:"app_name"`
		Status         string `json:"status"`
		DeploymentID   string `json:"deployment_id"`
		DeploymentName string `json:"deployment_name"`
	}
	usedBy := []appUsage{}
	if rows, qErr := s.db.Query(ctx, `
		SELECT da.id::text, da.name, da.status, d.id::text, d.name
		FROM deployment_apps da JOIN deployments d ON d.id = da.deployment_id
		WHERE da.repo_id = $1 AND da.status != 'destroyed'
		ORDER BY d.name, da.name`, id); qErr == nil {
		defer rows.Close()
		for rows.Next() {
			var a appUsage
			if err := rows.Scan(&a.AppID, &a.AppName, &a.Status, &a.DeploymentID, &a.DeploymentName); err == nil {
				usedBy = append(usedBy, a)
			}
		}
	}
	it.UsedBy = len(usedBy)

	type finding struct {
		Scanner    string `json:"scanner"`
		Severity   string `json:"severity"`
		RuleID     string `json:"rule_id"`
		Message    string `json:"message"`
		FilePath   string `json:"file_path"`
		LineNumber int    `json:"line_number"`
	}
	findings := []finding{}
	if rows, qErr := s.db.Query(ctx, `
		SELECT scanner, severity, rule_id, message, COALESCE(file_path, ''), COALESCE(line_number, 0)
		FROM compliance_findings WHERE repo_id = $1
		ORDER BY CASE severity
		  WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2
		  WHEN 'low' THEN 3 ELSE 4 END, scanner`, id); qErr == nil {
		defer rows.Close()
		for rows.Next() {
			var f finding
			if err := rows.Scan(&f.Scanner, &f.Severity, &f.RuleID, &f.Message, &f.FilePath, &f.LineNumber); err == nil {
				findings = append(findings, f)
				it.Findings[f.Severity]++
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"repo":     it,
		"used_by":  usedBy,
		"findings": findings,
	})
}

// handleDeleteRepo removes a repo from the store. Apps that referenced it have
// their repo_id set to NULL (the FK is ON DELETE SET NULL) — never destroyed.
// The on-disk workspace and the encrypted credentials secret are cleaned up.
func (s *Server) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")

	tag, err := s.db.Exec(ctx, `DELETE FROM git_repos WHERE id = $1`, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("delete repo: %v", err), http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "repo not found", http.StatusNotFound)
		return
	}
	// Best-effort cleanup — failures here must not fail the delete.
	_ = s.vault.Delete(ctx, "git-repo:"+id)
	s.deploy.RemoveRepoStoreWorkspace(id)
	s.hub.Publish(sse.Event{Type: "repo", Payload: map[string]string{"id": id, "deleted": "true"}})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ── File editor (repo store) ────────────────────────────────────────────────

func (s *Server) handleListStoreFiles(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	files, err := s.deploy.ListStoreFiles(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"files": files})
}

func (s *Server) handleGetStoreFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	path := chi.URLParam(r, "*")
	content, err := s.deploy.ReadStoreFile(path, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "content": string(content)})
}

func (s *Server) handlePutStoreFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	path := chi.URLParam(r, "*")
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.deploy.WriteStoreFile(id, path, []byte(body.Content)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleCommitStoreRepo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message == "" {
		body.Message = "edit via AutoDevOps dashboard"
	}
	if err := s.deploy.CommitAndPushStore(r.Context(), id, body.Message); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pushed"})
}

func (s *Server) handleRefreshStoreRepo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	res, err := s.deploy.RefreshRepoStoreClone(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleScanRepo runs the source-level compliance scanners against the repo's
// current workspace and returns the findings plus whether the gate would block.
// A blocking gate is informational here — it is NOT an HTTP error.
func (s *Server) handleScanRepo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if s.compliance == nil {
		http.Error(w, "compliance scanner not configured", http.StatusServiceUnavailable)
		return
	}
	path, err := s.deploy.EnsureRepoStoreClone(ctx, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	gateErr := s.compliance.ScanSource(ctx, id, path)

	type finding struct {
		Scanner    string `json:"scanner"`
		Severity   string `json:"severity"`
		RuleID     string `json:"rule_id"`
		Message    string `json:"message"`
		FilePath   string `json:"file_path"`
		LineNumber int    `json:"line_number"`
	}
	findings := []finding{}
	if rows, qErr := s.db.Query(ctx, `
		SELECT scanner, severity, rule_id, message, COALESCE(file_path, ''), COALESCE(line_number, 0)
		FROM compliance_findings WHERE repo_id = $1
		ORDER BY CASE severity
		  WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2
		  WHEN 'low' THEN 3 ELSE 4 END, scanner`, id); qErr == nil {
		defer rows.Close()
		for rows.Next() {
			var f finding
			if err := rows.Scan(&f.Scanner, &f.Severity, &f.RuleID, &f.Message, &f.FilePath, &f.LineNumber); err == nil {
				findings = append(findings, f)
			}
		}
	}
	resp := map[string]any{"findings": findings, "blocked": gateErr != nil}
	if gateErr != nil {
		resp["gate_message"] = gateErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── helpers ─────────────────────────────────────────────────────────────────

// deriveRepoName extracts a human name from a git URL's last path segment,
// stripping a trailing ".git". Falls back to "repo" if nothing usable remains.
func deriveRepoName(repoURL string) string {
	u := strings.TrimSuffix(strings.TrimRight(repoURL, "/"), ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.TrimSpace(u)
	if u == "" {
		return "repo"
	}
	return u
}

// uniqueRepoName returns base, or base-2, base-3, … until it does not collide
// with an existing git_repos.name.
func (s *Server) uniqueRepoName(ctx context.Context, base string) (string, error) {
	name := base
	for i := 2; ; i++ {
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM git_repos WHERE name = $1)`, name).Scan(&exists); err != nil {
			return "", fmt.Errorf("check repo name: %w", err)
		}
		if !exists {
			return name, nil
		}
		name = fmt.Sprintf("%s-%d", base, i)
		if i > 1000 {
			return "", fmt.Errorf("could not find a free name for %q", base)
		}
	}
}

// findOrCreateRepoByURL returns the id of the existing store repo for repoURL
// (matching url + branch), or creates one, copying the given git auth onto it
// when newly created. Used to auto-register a deployment app's repo into the
// store. Best-effort: callers treat errors as non-fatal.
func (s *Server) findOrCreateRepoByURL(ctx context.Context, repoURL, branch string, auth *gitAuthRequest) (string, error) {
	if branch == "" {
		branch = "main"
	}
	var existing string
	err := s.db.QueryRow(ctx,
		`SELECT id::text FROM git_repos WHERE repo_url = $1 AND branch = $2 LIMIT 1`,
		repoURL, branch,
	).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	name, nErr := s.uniqueRepoName(ctx, deriveRepoName(repoURL))
	if nErr != nil {
		return "", nErr
	}
	return s.insertRepo(ctx, name, repoURL, branch, auth)
}

// repoGitAuth returns the stored git_auth for a repo (decrypted), or nil if the
// repo has no credentials. Used to copy a picked repo's credentials onto a new
// app created from it.
func (s *Server) repoGitAuth(ctx context.Context, repoID string) (*gitAuthRequest, string, string, error) {
	var repoURL, branch string
	var credsSecretID *string
	if err := s.db.QueryRow(ctx,
		`SELECT repo_url, branch, git_credentials_secret_id::text FROM git_repos WHERE id = $1`, repoID,
	).Scan(&repoURL, &branch, &credsSecretID); err != nil {
		return nil, "", "", fmt.Errorf("load repo %s: %w", repoID, err)
	}
	if credsSecretID == nil {
		return nil, repoURL, branch, nil
	}
	plaintext, err := s.vault.GetByID(ctx, *credsSecretID)
	if err != nil {
		return nil, "", "", fmt.Errorf("decrypt repo credentials: %w", err)
	}
	var m struct {
		Type   string `json:"type"`
		Token  string `json:"token"`
		SSHKey string `json:"ssh_key"`
	}
	if err := json.Unmarshal(plaintext, &m); err != nil {
		return nil, "", "", fmt.Errorf("parse repo credentials: %w", err)
	}
	if m.Type == "" {
		return nil, repoURL, branch, nil
	}
	return &gitAuthRequest{Type: m.Type, Token: m.Token, SSHKey: m.SSHKey}, repoURL, branch, nil
}

// linkAppRepo sets deployment_apps.repo_id. Best-effort; callers log on error.
func (s *Server) linkAppRepo(ctx context.Context, appID, repoID string) error {
	_, err := s.db.Exec(ctx, `UPDATE deployment_apps SET repo_id = $2 WHERE id = $1`, appID, repoID)
	return err
}
