package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Rexxzar/autodevops/internal/auth"
	"github.com/Rexxzar/autodevops/internal/cicd"
	"github.com/Rexxzar/autodevops/internal/cloud"
	"github.com/Rexxzar/autodevops/internal/deploy"
	"github.com/Rexxzar/autodevops/internal/docker"
	"github.com/Rexxzar/autodevops/internal/k8s"
	"github.com/Rexxzar/autodevops/internal/registry"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/Rexxzar/autodevops/internal/terraform"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleHealth doubles as a tiny unauthenticated bootstrap endpoint: the
// frontend reads ingress_domain here so generated app URLs
// (web/src/pages/deployment-detail.tsx) match the server's actual
// INGRESS_DOMAIN instead of a hardcoded guess, and public_host so
// container-target app links point at this server's real address instead
// of a hardcoded "localhost".
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "ingress_domain": s.ingressDomain, "public_host": s.serverIP(r)})
}

func (s *Server) handleDNSCheck(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		writeError(w, http.StatusBadRequest, "domain query param required")
		return
	}

	// Resolve the domain
	addrs, err := net.LookupHost(domain)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"domain":    domain,
			"ok":        false,
			"server_ip": s.serverIP(r),
			"error":     "domain does not resolve — check the name is correct",
		})
		return
	}

	targetIP := r.URL.Query().Get("target_ip")
	if targetIP == "" {
		targetIP = s.serverIP(r)
	}
	ok := false
	for _, a := range addrs {
		if a == targetIP {
			ok = true
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"domain":       domain,
		"resolved_ips": addrs,
		"server_ip":    targetIP,
		"ok":           ok,
	})
}

// serverIP returns the public IP to tell users to point their DNS at.
// Priority: PUBLIC_IP env var → X-Forwarded-For → remote addr.
func (s *Server) serverIP(r *http.Request) string {
	if s.publicIP != "" {
		return s.publicIP
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required")
		return
	}

	var userID, passwordHash string
	err := s.db.QueryRow(r.Context(),
		"SELECT id::text, password_hash FROM users WHERE email = $1", req.Email,
	).Scan(&userID, &passwordHash)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if !auth.VerifyPassword(req.Password, passwordHash) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, err := s.auth.Issue(userID, req.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

// appTargetType maps a deployment_apps.target_kind value back to the legacy
// "k8s"/"container"/"vm" target_type vocabulary the frontend still expects.
func appTargetType(targetKind string) string {
	switch targetKind {
	case "local_k8s":
		return "k8s"
	case "local_docker":
		return "container"
	case "cloud_vm":
		return "vm"
	case "cloud_ecs":
		return "ecs"
	case "cloud_aci":
		return "aci"
	default:
		return targetKind
	}
}

// normalizeTargetType maps the legacy "k8s"/"container"/"vm"/"ecs"/"aci"
// target_type vocabulary (still used by the frontend) to a
// deployment_apps.target_kind value, rejecting anything else.
func normalizeTargetType(targetType string) (string, error) {
	switch targetType {
	case "k8s":
		return "local_k8s", nil
	case "container":
		return "local_docker", nil
	case "vm":
		return "cloud_vm", nil
	case "ecs":
		return "cloud_ecs", nil
	case "aci":
		return "cloud_aci", nil
	default:
		return "", fmt.Errorf(`target_type must be "k8s", "container", "vm", "ecs", or "aci"`)
	}
}

// containerTargetType maps a deployment_apps.target_kind to the
// provisioned_resources.resource_type validateContainerTarget should require,
// and reports whether targetKind needs that validation at all (local_k8s and
// local_docker don't reference a provisioned resource).
func containerTargetType(targetKind string) (string, bool) {
	switch targetKind {
	case "cloud_vm":
		return "vm", true
	case "cloud_ecs":
		return "ecs", true
	case "cloud_aci":
		return "aci", true
	default:
		return "", false
	}
}

// validateContainerTarget checks that resourceID refers to an applied
// provisioned_resources row of type wantType ("vm", "ecs", or "aci") —
// required when targetKind is cloud_vm/cloud_ecs/cloud_aci (the corresponding
// connector in internal/deploy dials/redeploys this resource at deploy time
// and fails if it's missing or not yet provisioned).
func (s *Server) validateContainerTarget(ctx context.Context, resourceID, wantType string) error {
	if resourceID == "" {
		return fmt.Errorf("target_resource_id required for target_type %q", wantType)
	}
	var resourceType, status string
	if err := s.db.QueryRow(ctx,
		`SELECT resource_type, status FROM provisioned_resources WHERE id = $1`, resourceID,
	).Scan(&resourceType, &status); err != nil {
		return fmt.Errorf("target_resource_id must reference an existing resource")
	}
	if resourceType != wantType {
		return fmt.Errorf("target_resource_id must reference a %s resource (got %q)", wantType, resourceType)
	}
	if status != "applied" {
		return fmt.Errorf("target_resource_id must reference an applied resource (status=%q)", status)
	}
	if wantType == "aci" || wantType == "ecs" {
		var existingApp string
		if err := s.db.QueryRow(ctx,
			`SELECT name FROM deployment_apps WHERE target_resource_id = $1 LIMIT 1`, resourceID,
		).Scan(&existingApp); err == nil {
			return fmt.Errorf("resource is already used by app %q — each ACI/ECS resource can only serve one app", existingApp)
		}
	}
	return nil
}

// gitAuthRequest is the request-body shape for optional git credentials,
// shared by handleCreateDeployment and handleAddApp.
type gitAuthRequest struct {
	Type   string `json:"type"`
	Token  string `json:"token,omitempty"`
	SSHKey string `json:"ssh_key,omitempty"`
}

// appCreateRequest is createApp's input — already-validated fields shared by
// "create stack + first app" (handleCreateDeployment) and "add app to
// existing stack" (handleAddApp). Kind selects between the two: "git"
// (default, zero value) uses RepoURL/Branch/GitAuth; "image" (Phase 4, only
// reachable via handleAddApp) uses ImageRef/Port/Env instead.
type appCreateRequest struct {
	Name              string
	Kind              string // "git" (default) | "image"
	RepoID            string // kind == "git", optional — back this app with a stored repo (Git Repo Store)
	RepoURL           string
	Branch            string
	TargetKind        string // "local_k8s" | "local_docker" | "cloud_vm"
	TargetResourceID  string // cloud_vm: provisioned_resources.id (a "vm" resource)
	Domain            string
	GitAuth           *gitAuthRequest
	BuildContext      string            // kind == "git", optional — see Phase 9 build_context
	DeclaredFramework string            // kind == "git", optional — user-declared framework for Procfile/DB env mapping
	ImageRef          string            // kind == "image"
	Port              int               // kind == "image"
	Env               map[string]string // kind == "image", optional
	VolumePath        string            // kind == "image", optional — see Phase 4 volume_path
	SecretFiles       map[string]string // container path -> base64 content, optional — see secret_files
	K8sConfig         json.RawMessage   // kind == "git", optional — local_k8s per-app tuning (replicas/HPA/probes/...)
	ComplianceEnforce bool              // kind == "git", optional — block the deploy when the gate finds ≥threshold issues (default false = report-only)
}

// storeSecretFiles encrypts files (container path -> base64 content) into the
// vault under "secret-files:<deploymentID>:<appName>" for createApp/
// createImageApp's INSERT — secretFilesName feeds the secret_files_id
// subquery, paths becomes secret_file_paths (cleartext, for dashboard
// display without decrypting). Returns (nil, nil, nil) if files is empty.
func (s *Server) storeSecretFiles(ctx context.Context, deploymentID, appName string, files map[string]string) (secretFilesName *string, paths []string, err error) {
	if len(files) == 0 {
		return nil, nil, nil
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return nil, nil, fmt.Errorf("encode secret files: %w", err)
	}
	name := "secret-files:" + deploymentID + ":" + appName
	if err := s.vault.Put(ctx, name, raw); err != nil {
		return nil, nil, fmt.Errorf("store secret files: %w", err)
	}
	paths = make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	return &name, paths, nil
}

// createApp generates a per-app webhook secret and (optionally) stores git
// credentials — both encrypted in the vault — then inserts the
// deployment_apps row under the given stack, mirroring
// handleCreateCloudAccount's "encrypt before it touches the database" shape.
// Returns the new app's id and its plaintext webhook secret (returned to the
// caller exactly once, for pasting into GitHub's repo → Settings → Webhooks).
// req.Kind == "image" delegates to createImageApp instead — those apps have
// no webhook/git credentials, so webhookSecret is always "" for them.
func (s *Server) createApp(ctx context.Context, deploymentID string, req appCreateRequest) (appID, webhookSecret string, err error) {
	if req.Kind == "image" {
		appID, err = s.createImageApp(ctx, deploymentID, req)
		return appID, "", err
	}

	// Resolve repo backing (Git Repo Store). An explicit RepoID fills in the
	// repo URL/branch and copies the stored credentials into this app's own
	// per-app secret (apps stay independent — deleting the repo never breaks
	// them). Without a RepoID, the repo is auto-registered into the store after
	// insert so every deployed git repo shows up on the Repositories page.
	var backingRepoID string
	if req.RepoID != "" {
		auth, repoURL, repoBranch, rErr := s.repoGitAuth(ctx, req.RepoID)
		if rErr != nil {
			return "", "", fmt.Errorf("resolve stored repo: %w", rErr)
		}
		backingRepoID = req.RepoID
		if req.RepoURL == "" {
			req.RepoURL = repoURL
		}
		if req.Branch == "" {
			req.Branch = repoBranch
		}
		if req.GitAuth == nil && auth != nil {
			req.GitAuth = auth
		}
	}

	webhookSecretRaw := make([]byte, 32)
	if _, err := rand.Read(webhookSecretRaw); err != nil {
		return "", "", fmt.Errorf("generate webhook secret: %w", err)
	}
	webhookSecret = hex.EncodeToString(webhookSecretRaw)

	webhookSecretName := "webhook:" + deploymentID + ":" + req.Name
	if err := s.vault.Put(ctx, webhookSecretName, []byte(webhookSecret)); err != nil {
		return "", "", fmt.Errorf("store webhook secret: %w", err)
	}

	var gitCredsSecretName *string
	if req.GitAuth != nil {
		raw, err := json.Marshal(map[string]string{
			"type":    req.GitAuth.Type,
			"token":   req.GitAuth.Token,
			"ssh_key": req.GitAuth.SSHKey,
		})
		if err != nil {
			return "", "", fmt.Errorf("encode git credentials: %w", err)
		}
		name := "git-credentials:" + deploymentID + ":" + req.Name
		if err := s.vault.Put(ctx, name, raw); err != nil {
			return "", "", fmt.Errorf("store git credentials: %w", err)
		}
		gitCredsSecretName = &name
	}

	branch := req.Branch
	if branch == "" {
		branch = "main"
	}

	secretFilesName, secretFilePaths, err := s.storeSecretFiles(ctx, deploymentID, req.Name, req.SecretFiles)
	if err != nil {
		return "", "", err
	}

	var k8sConfigArg interface{}
	if len(req.K8sConfig) > 0 {
		k8sConfigArg = []byte(req.K8sConfig)
	}
	err = s.db.QueryRow(ctx, `
		INSERT INTO deployment_apps (deployment_id, name, repo_url, branch, target_kind, target_resource_id, domain, webhook_secret_id, git_credentials_secret_id, build_context, secret_files_id, secret_file_paths, declared_framework, k8s_config, compliance_enforce)
		SELECT $1, $2, $3, $4, $5, NULLIF($6, '')::uuid, NULLIF($7, ''),
		       (SELECT id FROM secrets WHERE name = $8),
		       (SELECT id FROM secrets WHERE name = $9),
		       NULLIF($10, ''),
		       (SELECT id FROM secrets WHERE name = $11),
		       $12,
		       NULLIF($13, ''),
		       $14,
		       $15
		RETURNING id::text
	`, deploymentID, req.Name, req.RepoURL, branch, req.TargetKind, req.TargetResourceID, req.Domain, webhookSecretName, gitCredsSecretName, req.BuildContext, secretFilesName, secretFilePaths, req.DeclaredFramework, k8sConfigArg, req.ComplianceEnforce).Scan(&appID)
	if err != nil {
		return "", "", fmt.Errorf("insert app: %w", err)
	}

	// Auto-register a brand-new repo into the store, then link the app to its
	// backing repo. Best-effort — store bookkeeping must never fail a deploy.
	if backingRepoID == "" && req.RepoURL != "" {
		if rid, regErr := s.findOrCreateRepoByURL(ctx, req.RepoURL, branch, req.GitAuth); regErr != nil {
			slog.Warn("repo store: auto-register failed", "repo_url", req.RepoURL, "error", regErr)
		} else {
			backingRepoID = rid
		}
	}
	if backingRepoID != "" {
		if linkErr := s.linkAppRepo(ctx, appID, backingRepoID); linkErr != nil {
			slog.Warn("repo store: link app to repo failed", "app_id", appID, "repo_id", backingRepoID, "error", linkErr)
		}
	}

	return appID, webhookSecret, nil
}

// createImageApp inserts a "image"-kind app (Phase 4, e.g. a postgres:16 db
// container) — no webhook secret or git credentials, just an image ref and
// container port, plus an optional vault-encrypted env-vars blob (e.g.
// POSTGRES_PASSWORD) referenced by image_env_secret_id, mirroring
// createApp's git-credentials encryption shape.
func (s *Server) createImageApp(ctx context.Context, deploymentID string, req appCreateRequest) (appID string, err error) {
	var envSecretName *string
	if len(req.Env) > 0 {
		raw, err := json.Marshal(req.Env)
		if err != nil {
			return "", fmt.Errorf("encode image env: %w", err)
		}
		name := "image-env:" + deploymentID + ":" + req.Name
		if err := s.vault.Put(ctx, name, raw); err != nil {
			return "", fmt.Errorf("store image env: %w", err)
		}
		envSecretName = &name
	}

	secretFilesName, secretFilePaths, err := s.storeSecretFiles(ctx, deploymentID, req.Name, req.SecretFiles)
	if err != nil {
		return "", err
	}

	err = s.db.QueryRow(ctx, `
		INSERT INTO deployment_apps (deployment_id, name, kind, image_ref, port, target_kind, target_resource_id, image_env_secret_id, volume_path, secret_files_id, secret_file_paths)
		SELECT $1, $2, 'image', $3, $4, $5, NULLIF($6, '')::uuid, (SELECT id FROM secrets WHERE name = $7), NULLIF($8, ''), (SELECT id FROM secrets WHERE name = $9), $10
		RETURNING id::text
	`, deploymentID, req.Name, req.ImageRef, req.Port, req.TargetKind, req.TargetResourceID, envSecretName, req.VolumePath, secretFilesName, secretFilePaths).Scan(&appID)
	if err != nil {
		return "", fmt.Errorf("insert image app: %w", err)
	}
	return appID, nil
}

// handleListDeployments lists stacks (deployments) with their apps. Each
// stack also carries flat fields mirroring its first app — kept for
// `web/src/pages/deployments.tsx` and friends, which predate multi-app
// stacks and only render a single app per row.
func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT d.id::text, d.name, d.created_at, d.updated_at,
		       da.id::text, da.name, COALESCE(da.repo_url,''), da.branch,
		       COALESCE(da.framework,''), da.target_kind, da.status,
		       COALESCE(da.image_ref,'')
		FROM deployments d
		JOIN deployment_apps da ON da.deployment_id = d.id
		ORDER BY d.created_at DESC, da.created_at ASC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deployments")
		return
	}
	defer rows.Close()

	type appRow struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		RepoURL    string `json:"repo_url"`
		Branch     string `json:"branch"`
		Framework  string `json:"framework,omitempty"`
		TargetKind string `json:"target_kind"`
		Status     string `json:"status"`
		ImageRef   string `json:"image_ref,omitempty"`
	}
	type row struct {
		ID         string    `json:"id"`
		Name       string    `json:"name"`
		RepoURL    string    `json:"repo_url"`
		Branch     string    `json:"branch"`
		Framework  string    `json:"framework,omitempty"`
		TargetType string    `json:"target_type"`
		Status     string    `json:"status"`
		ImageRef   string    `json:"image_ref,omitempty"`
		CreatedAt  time.Time `json:"created_at"`
		UpdatedAt  time.Time `json:"updated_at"`
		Apps       []appRow  `json:"apps"`
	}
	var results []row
	index := make(map[string]int)
	for rows.Next() {
		var stackID, stackName string
		var createdAt, updatedAt time.Time
		var a appRow
		if err := rows.Scan(&stackID, &stackName, &createdAt, &updatedAt,
			&a.ID, &a.Name, &a.RepoURL, &a.Branch, &a.Framework, &a.TargetKind, &a.Status, &a.ImageRef); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan deployment")
			return
		}
		i, ok := index[stackID]
		if !ok {
			results = append(results, row{ID: stackID, Name: stackName, CreatedAt: createdAt, UpdatedAt: updatedAt})
			i = len(results) - 1
			index[stackID] = i
		}
		results[i].Apps = append(results[i].Apps, a)
		if len(results[i].Apps) == 1 {
			results[i].RepoURL = a.RepoURL
			results[i].Branch = a.Branch
			results[i].Framework = a.Framework
			results[i].TargetType = appTargetType(a.TargetKind)
			results[i].Status = a.Status
			results[i].ImageRef = a.ImageRef
		}
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

// handleCreateDeployment registers a deployment for the Git-to-deploy
// orchestrator (T12). It generates a per-deployment webhook secret and
// (optionally) stores the user's chosen git auth — both encrypted in the
// vault, with deployments.webhook_secret_id/git_credentials_secret_id
// resolved to their UUIDs in the same INSERT — mirroring
// handleCreateCloudAccount's "encrypt before it touches the database" shape.
// The response carries the webhook URL + plaintext secret exactly once, for
// the user to paste into GitHub's repo → Settings → Webhooks.
func (s *Server) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string            `json:"name"`
		RepoID            string            `json:"repo_id,omitempty"` // back this app with a stored repo (Git Repo Store)
		RepoURL           string            `json:"repo_url"`
		Branch            string            `json:"branch"`
		TargetType        string            `json:"target_type"`
		TargetResourceID  string            `json:"target_resource_id,omitempty"`
		Domain            string            `json:"domain,omitempty"`
		GitAuth           *gitAuthRequest   `json:"git_auth,omitempty"`
		BuildContext      string            `json:"build_context,omitempty"`
		DeclaredFramework string            `json:"declared_framework,omitempty"`
		SecretFiles       map[string]string `json:"secret_files,omitempty"`
		NamespaceOverride string            `json:"namespace_override,omitempty"` // local_k8s: per-stack namespace override
		K8sConfig         json.RawMessage   `json:"k8s_config,omitempty"`         // local_k8s: per-app tuning (replicas/HPA/probes/...)
		ComplianceEnforce bool              `json:"compliance_enforce,omitempty"` // block deploy on gate findings (default false = report-only)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || (req.RepoURL == "" && req.RepoID == "") {
		writeError(w, http.StatusBadRequest, "name and repo_url (or repo_id) required")
		return
	}
	targetKind, err := normalizeTargetType(req.TargetType)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.GitAuth != nil && req.GitAuth.Type != deploy.GitAuthToken && req.GitAuth.Type != deploy.GitAuthSSHKey {
		writeError(w, http.StatusBadRequest, `git_auth.type must be "token" or "ssh_key"`)
		return
	}

	ctx := r.Context()

	if wantType, ok := containerTargetType(targetKind); ok {
		if err := s.validateContainerTarget(ctx, req.TargetResourceID, wantType); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	var deploymentID string
	if err := s.db.QueryRow(ctx, `INSERT INTO deployments (name, namespace_override) VALUES ($1, NULLIF($2, '')) RETURNING id::text`, req.Name, req.NamespaceOverride).Scan(&deploymentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create deployment")
		return
	}

	appID, webhookSecret, err := s.createApp(ctx, deploymentID, appCreateRequest{
		Name:              req.Name,
		RepoID:            req.RepoID,
		RepoURL:           req.RepoURL,
		Branch:            req.Branch,
		TargetKind:        targetKind,
		TargetResourceID:  req.TargetResourceID,
		Domain:            req.Domain,
		GitAuth:           req.GitAuth,
		BuildContext:      req.BuildContext,
		DeclaredFramework: req.DeclaredFramework,
		SecretFiles:       req.SecretFiles,
		K8sConfig:         req.K8sConfig,
		ComplianceEnforce: req.ComplianceEnforce,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create deployment")
		return
	}

	s.triggerAppIfGit(appID, "git")

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":             deploymentID,
		"webhook_url":    webhookPath(req.RepoURL, appID),
		"webhook_secret": webhookSecret,
	})
}

// handleImportComposePreview clones repoURL@branch and parses any
// docker-compose file at its root into a deploy.ComposePlan (Phase 9) — a
// read-only preview for the "Import from docker-compose" flow. No
// deployment_apps rows are created here; the frontend re-POSTs the
// user-confirmed plan through handleCreateDeployment/handleAddApp/
// handleCreateAppLink.
func (s *Server) handleImportComposePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoURL string          `json:"repo_url"`
		Branch  string          `json:"branch"`
		GitAuth *gitAuthRequest `json:"git_auth,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RepoURL == "" {
		writeError(w, http.StatusBadRequest, "repo_url required")
		return
	}
	if req.GitAuth != nil && req.GitAuth.Type != deploy.GitAuthToken && req.GitAuth.Type != deploy.GitAuthSSHKey {
		writeError(w, http.StatusBadRequest, `git_auth.type must be "token" or "ssh_key"`)
		return
	}

	var authType, token, sshKey string
	if req.GitAuth != nil {
		authType, token, sshKey = req.GitAuth.Type, req.GitAuth.Token, req.GitAuth.SSHKey
	}

	plan, err := s.deploy.PreviewCompose(r.Context(), req.RepoURL, req.Branch, authType, token, sshKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, plan)
}

// handleAddApp adds a new app to an existing stack, reusing createApp scoped
// to the stack's existing deployment id — the new app gets its own clone
// workspace (workDir/<appID>), webhook, and (once triggered) build/deploy/
// compliance run, alongside any apps already in the stack.
func (s *Server) handleAddApp(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")

	var exists bool
	if err := s.db.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM deployments WHERE id = $1)`, deploymentID,
	).Scan(&exists); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up deployment")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}

	var req struct {
		Name              string            `json:"name"`
		Kind              string            `json:"kind,omitempty"` // "git" (default) | "image"
		RepoID            string            `json:"repo_id,omitempty"`
		RepoURL           string            `json:"repo_url,omitempty"`
		Branch            string            `json:"branch,omitempty"`
		TargetType        string            `json:"target_type"`
		TargetResourceID  string            `json:"target_resource_id,omitempty"`
		Domain            string            `json:"domain,omitempty"`
		GitAuth           *gitAuthRequest   `json:"git_auth,omitempty"`
		BuildContext      string            `json:"build_context,omitempty"`
		DeclaredFramework string            `json:"declared_framework,omitempty"`
		ImageRef          string            `json:"image_ref,omitempty"`
		Port              int               `json:"port,omitempty"`
		Env               map[string]string `json:"env,omitempty"`
		VolumePath        string            `json:"volume_path,omitempty"`
		SecretFiles       map[string]string `json:"secret_files,omitempty"`
		K8sConfig         json.RawMessage   `json:"k8s_config,omitempty"` // kind == "git", local_k8s per-app tuning
		ComplianceEnforce bool              `json:"compliance_enforce,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	if req.Kind == "" {
		req.Kind = "git"
	}
	targetKind, err := normalizeTargetType(req.TargetType)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if wantType, ok := containerTargetType(targetKind); ok {
		if err := s.validateContainerTarget(r.Context(), req.TargetResourceID, wantType); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	switch req.Kind {
	case "git":
		if req.RepoURL == "" && req.RepoID == "" {
			writeError(w, http.StatusBadRequest, "repo_url (or repo_id) required")
			return
		}
		if req.GitAuth != nil && req.GitAuth.Type != deploy.GitAuthToken && req.GitAuth.Type != deploy.GitAuthSSHKey {
			writeError(w, http.StatusBadRequest, `git_auth.type must be "token" or "ssh_key"`)
			return
		}
	case "image":
		if req.ImageRef == "" {
			writeError(w, http.StatusBadRequest, "image_ref required")
			return
		}
		if req.Port < 1 || req.Port > 65535 {
			writeError(w, http.StatusBadRequest, "port must be between 1 and 65535")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, `kind must be "git" or "image"`)
		return
	}

	appID, webhookSecret, err := s.createApp(r.Context(), deploymentID, appCreateRequest{
		Name:              req.Name,
		Kind:              req.Kind,
		RepoID:            req.RepoID,
		RepoURL:           req.RepoURL,
		Branch:            req.Branch,
		TargetKind:        targetKind,
		TargetResourceID:  req.TargetResourceID,
		Domain:            req.Domain,
		GitAuth:           req.GitAuth,
		BuildContext:      req.BuildContext,
		DeclaredFramework: req.DeclaredFramework,
		ImageRef:          req.ImageRef,
		Port:              req.Port,
		Env:               req.Env,
		VolumePath:        req.VolumePath,
		SecretFiles:       req.SecretFiles,
		K8sConfig:         req.K8sConfig,
		ComplianceEnforce: req.ComplianceEnforce,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, "an app with this name already exists in this deployment")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to add app")
		return
	}

	if req.Kind == "image" {
		go func() {
			if err := s.deploy.DeployImageApp(context.Background(), appID); err != nil {
				slog.Error("deploy: image app create", "app_id", appID, "error", err)
			}
		}()
		writeJSON(w, http.StatusCreated, map[string]string{"id": appID})
		return
	}

	s.triggerAppIfGit(appID, "git")

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":             appID,
		"webhook_url":    webhookPath(req.RepoURL, appID),
		"webhook_secret": webhookSecret,
	})
}

// handleGetDeployment returns a single stack with its full apps[] — powers
// the deployment-detail page (status, target, domain, namespace, image_ref
// per app).
func (s *Server) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var stackName, namespaceOverride string
	var createdAt, updatedAt time.Time
	if err := s.db.QueryRow(r.Context(),
		`SELECT name, COALESCE(namespace_override, ''), created_at, updated_at FROM deployments WHERE id = $1`, id,
	).Scan(&stackName, &namespaceOverride, &createdAt, &updatedAt); err != nil {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, name, kind, COALESCE(role, ''), COALESCE(repo_url, ''), branch,
		       COALESCE(framework, ''), target_kind, COALESCE(namespace, ''),
		       COALESCE(image_ref, ''), COALESCE(port, 0), COALESCE(domain, ''), status, created_at, updated_at,
		       COALESCE(volume_path, ''), COALESCE(secret_file_paths, '{}'), tls_status, COALESCE(tls_error, ''),
		       git_credentials_secret_id IS NOT NULL,
		       image_env_secret_id IS NOT NULL,
		       COALESCE(declared_framework, ''),
		       k8s_config,
		       COALESCE(target_resource_id::text, ''),
		       compliance_enforce
		FROM deployment_apps WHERE deployment_id = $1 ORDER BY created_at ASC
	`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list apps")
		return
	}
	defer rows.Close()

	type appDetail struct {
		ID                string    `json:"id"`
		Name              string    `json:"name"`
		Kind              string    `json:"kind"`
		Role              string    `json:"role,omitempty"`
		RepoURL           string    `json:"repo_url,omitempty"`
		Branch            string    `json:"branch"`
		Framework         string    `json:"framework,omitempty"`
		DeclaredFramework string    `json:"declared_framework,omitempty"`
		TargetType        string    `json:"target_type"`
		Namespace         string    `json:"namespace,omitempty"`
		ImageRef          string    `json:"image_ref,omitempty"`
		Port              int       `json:"port,omitempty"`
		Domain            string    `json:"domain,omitempty"`
		HostPort          uint32    `json:"host_port,omitempty"`
		Status            string    `json:"status"`
		CreatedAt         time.Time `json:"created_at"`
		UpdatedAt         time.Time `json:"updated_at"`
		VolumePath        string    `json:"volume_path,omitempty"`
		SecretFilePaths   []string  `json:"secret_file_paths,omitempty"`
		TLSStatus            string    `json:"tls_status,omitempty"`
		TLSError             string    `json:"tls_error,omitempty"`
		HasGitCredentials    bool            `json:"has_git_credentials"`
		HasImageEnv          bool            `json:"has_image_env"`
		K8sConfig            json.RawMessage `json:"k8s_config,omitempty"`
		Links                []appLink       `json:"links"`
		// Steps hydrates the deploy-progress pipeline for pages that load (or
		// reconnect their SSE stream) mid-deploy — deploy.step events aren't
		// replayed on reconnect, so the frontend seeds stepsByApp from here.
		Steps             []deploy.StepEvent `json:"steps,omitempty"`
		TargetResourceID  string             `json:"target_resource_id,omitempty"`
		ComplianceEnforce bool               `json:"compliance_enforce"`
	}
	var apps []appDetail
	index := make(map[string]int)
	for rows.Next() {
		var a appDetail
		var targetKind string
		if err := rows.Scan(&a.ID, &a.Name, &a.Kind, &a.Role, &a.RepoURL, &a.Branch,
			&a.Framework, &targetKind, &a.Namespace, &a.ImageRef, &a.Port, &a.Domain, &a.Status, &a.CreatedAt, &a.UpdatedAt, &a.VolumePath, &a.SecretFilePaths, &a.TLSStatus, &a.TLSError, &a.HasGitCredentials, &a.HasImageEnv, &a.DeclaredFramework, &a.K8sConfig, &a.TargetResourceID, &a.ComplianceEnforce); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan app")
			return
		}
		a.TargetType = appTargetType(targetKind)
		if targetKind == "local_docker" {
			a.HostPort = docker.AppHostPort(stackName, a.Name)
		}
		a.Steps = s.deploy.CurrentSteps(a.ID)
		a.Links = []appLink{}
		index[a.ID] = len(apps)
		apps = append(apps, a)
	}
	if apps == nil {
		apps = []appDetail{}
	}

	linkRows, err := s.db.Query(r.Context(), `
		SELECT l.id::text, l.app_id::text, l.alias,
		       COALESCE(l.depends_on_app_id::text, ''), COALESCE(da.name, ''),
		       COALESCE(l.depends_on_resource_id::text, ''), COALESCE(pr.name, ''),
		       COALESCE(l.port, 0)
		FROM deployment_app_links l
		JOIN deployment_apps owner ON owner.id = l.app_id
		LEFT JOIN deployment_apps da ON da.id = l.depends_on_app_id
		LEFT JOIN provisioned_resources pr ON pr.id = l.depends_on_resource_id
		WHERE owner.deployment_id = $1
	`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list app links")
		return
	}
	defer linkRows.Close()
	for linkRows.Next() {
		var l appLink
		var appID string
		if err := linkRows.Scan(&l.ID, &appID, &l.Alias,
			&l.DependsOnAppID, &l.DependsOnAppName,
			&l.DependsOnResourceID, &l.DependsOnResourceName, &l.Port); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan app link")
			return
		}
		if i, ok := index[appID]; ok {
			apps[i].Links = append(apps[i].Links, l)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":                 id,
		"name":               stackName,
		"namespace_override": namespaceOverride,
		"created_at":         createdAt,
		"updated_at":         updatedAt,
		"apps":               apps,
	})
}

// appLink is one resolved dependency edge from deployment_app_links, joined
// to the depended-on app's or provisioned resource's name — returned as part
// of an app's []Links in handleGetDeployment and by the create/delete link
// handlers below. Exactly one of DependsOn{App,Resource}ID is set, per the
// table's CHECK constraint; Port is only meaningful for resource links.
type appLink struct {
	ID                    string `json:"id"`
	Alias                 string `json:"alias"`
	DependsOnAppID        string `json:"depends_on_app_id,omitempty"`
	DependsOnAppName      string `json:"depends_on_app_name,omitempty"`
	DependsOnResourceID   string `json:"depends_on_resource_id,omitempty"`
	DependsOnResourceName string `json:"depends_on_resource_name,omitempty"`
	Port                  int    `json:"port,omitempty"`
}

// aliasPattern restricts link aliases to valid AUTODEVOPS_<ALIAS>_* env var
// name fragments: must start with a letter, then letters/digits/underscores.
var aliasPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// triggerAppIfGit re-runs ProcessPush for a "git" kind app in the background —
// the same async path handleTriggerApp uses for manual rebuilds. Called after
// a link is created or deleted so the app's container picks up the updated
// AUTODEVOPS_* environment (resolveLinks, T-Phase3). A no-op for "image" kind
// apps, which have no build/deploy pipeline to re-run.
func (s *Server) triggerAppIfGit(appID, kind string) {
	if kind != "git" {
		return
	}
	go func() {
		if err := s.deploy.ProcessPush(context.Background(), appID); err != nil {
			slog.Error("deploy: re-trigger after link change", "app_id", appID, "error", err)
		}
	}()
}

// handleCreateAppLink adds a dependency link from one app to either another
// app in the same stack (deployment_app_links.depends_on_app_id) or an
// applied provisioned_resource (depends_on_resource_id, e.g. an RDS instance
// or cloud VM — Phase 5a), with an optional Port for resource links that
// expose a non-default port. resolveLinks (T-Phase3/5a) turns this into
// AUTODEVOPS_<ALIAS>_* env vars for appId's container on its next
// build/deploy, triggered here.
func (s *Server) handleCreateAppLink(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var appKind string
	if err := s.db.QueryRow(r.Context(),
		`SELECT kind FROM deployment_apps WHERE id = $1 AND deployment_id = $2`, appID, deploymentID,
	).Scan(&appKind); err != nil {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}

	var req struct {
		Alias               string `json:"alias"`
		DependsOnAppID      string `json:"depends_on_app_id"`
		DependsOnResourceID string `json:"depends_on_resource_id"`
		Port                int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !aliasPattern.MatchString(req.Alias) {
		writeError(w, http.StatusBadRequest, "alias must start with a letter and contain only letters, digits, and underscores")
		return
	}
	if (req.DependsOnAppID == "") == (req.DependsOnResourceID == "") {
		writeError(w, http.StatusBadRequest, "exactly one of depends_on_app_id or depends_on_resource_id is required")
		return
	}
	if req.Port != 0 && (req.Port < 1 || req.Port > 65535) {
		writeError(w, http.StatusBadRequest, "port must be between 1 and 65535")
		return
	}

	var dependsOnName, dependsOnResourceName string
	if req.DependsOnAppID != "" {
		if req.DependsOnAppID == appID {
			writeError(w, http.StatusBadRequest, "an app cannot depend on itself")
			return
		}
		if req.Port != 0 {
			writeError(w, http.StatusBadRequest, "port is only valid for resource links")
			return
		}
		if err := s.db.QueryRow(r.Context(),
			`SELECT name FROM deployment_apps WHERE id = $1 AND deployment_id = $2`, req.DependsOnAppID, deploymentID,
		).Scan(&dependsOnName); err != nil {
			writeError(w, http.StatusBadRequest, "depends_on_app_id must reference another app in this stack")
			return
		}
	} else {
		if err := s.db.QueryRow(r.Context(),
			`SELECT name FROM provisioned_resources WHERE id = $1 AND status = 'applied'`, req.DependsOnResourceID,
		).Scan(&dependsOnResourceName); err != nil {
			writeError(w, http.StatusBadRequest, "depends_on_resource_id must reference an applied resource")
			return
		}
	}

	var linkID string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO deployment_app_links (app_id, depends_on_app_id, depends_on_resource_id, alias, port)
		VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, NULLIF($5, 0))
		RETURNING id::text
	`, appID, req.DependsOnAppID, req.DependsOnResourceID, req.Alias, req.Port).Scan(&linkID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create link")
		return
	}

	s.triggerAppIfGit(appID, appKind)

	writeJSON(w, http.StatusCreated, appLink{
		ID:                    linkID,
		Alias:                 req.Alias,
		DependsOnAppID:        req.DependsOnAppID,
		DependsOnAppName:      dependsOnName,
		DependsOnResourceID:   req.DependsOnResourceID,
		DependsOnResourceName: dependsOnResourceName,
		Port:                  req.Port,
	})
}

// handleDeleteAppLink removes a dependency link and re-triggers the app's
// build/deploy so its container env reflects the change (resolveLinks,
// T-Phase3).
func (s *Server) handleDeleteAppLink(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")
	linkID := chi.URLParam(r, "linkId")

	var appKind string
	if err := s.db.QueryRow(r.Context(),
		`SELECT kind FROM deployment_apps WHERE id = $1 AND deployment_id = $2`, appID, deploymentID,
	).Scan(&appKind); err != nil {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}

	tag, err := s.db.Exec(r.Context(),
		`DELETE FROM deployment_app_links WHERE id = $1 AND app_id = $2`, linkID, appID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete link")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "link not found")
		return
	}

	s.triggerAppIfGit(appID, appKind)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUpdateAppDomain(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var appKind string
	if err := s.db.QueryRow(r.Context(),
		`SELECT kind FROM deployment_apps WHERE id = $1 AND deployment_id = $2`, appID, deploymentID,
	).Scan(&appKind); err != nil {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}

	if _, err := s.db.Exec(r.Context(),
		`UPDATE deployment_apps SET domain = NULLIF($2, ''), updated_at = now() WHERE id = $1`,
		appID, req.Domain,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update domain")
		return
	}

	s.triggerAppIfGit(appID, appKind)

	writeJSON(w, http.StatusOK, map[string]string{"domain": req.Domain})
}

// handleUpdateAppCompliance toggles whether the compliance gate BLOCKS this
// app's deploys (true) or just records findings (false, report-only default).
// It does not re-trigger a deploy — the flag takes effect on the next one.
func (s *Server) handleUpdateAppCompliance(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var req struct {
		Enforce bool `json:"enforce"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tag, err := s.db.Exec(r.Context(),
		`UPDATE deployment_apps SET compliance_enforce = $3, updated_at = now() WHERE id = $1 AND deployment_id = $2`,
		appID, deploymentID, req.Enforce,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update compliance setting")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"compliance_enforce": req.Enforce})
}

// handleUpdateSecretFiles adds and/or removes per-app secret files (container
// path -> file content), re-encrypting the merged set back into the vault
// under "secret-files:<deploymentID>:<appName>" (see loadSecretFiles,
// internal/deploy/secretfiles.go) and re-triggering a redeploy so the new
// mount takes effect on local_k8s/local_docker targets.
func (s *Server) handleUpdateSecretFiles(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var appName, appKind string
	var secretFilesID *string
	if err := s.db.QueryRow(r.Context(),
		`SELECT name, kind, secret_files_id::text FROM deployment_apps WHERE id = $1 AND deployment_id = $2`,
		appID, deploymentID,
	).Scan(&appName, &appKind, &secretFilesID); err != nil {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}

	var req struct {
		Add    map[string]string `json:"add,omitempty"`    // container path -> base64 content
		Remove []string          `json:"remove,omitempty"` // container paths to drop
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	files := make(map[string]string)
	if secretFilesID != nil {
		raw, err := s.vault.GetByID(r.Context(), *secretFilesID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load secret files")
			return
		}
		if err := json.Unmarshal(raw, &files); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to decode secret files")
			return
		}
	}
	for _, path := range req.Remove {
		delete(files, path)
	}
	for path, content := range req.Add {
		files[path] = content
	}

	raw, err := json.Marshal(files)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode secret files")
		return
	}
	name := "secret-files:" + deploymentID + ":" + appName
	if err := s.vault.Put(r.Context(), name, raw); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store secret files")
		return
	}

	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	if _, err := s.db.Exec(r.Context(),
		`UPDATE deployment_apps SET secret_files_id = (SELECT id FROM secrets WHERE name = $1), secret_file_paths = $2 WHERE id = $3`,
		name, paths, appID,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update app")
		return
	}

	if appKind == "image" {
		go func() {
			if err := s.deploy.DeployImageApp(context.Background(), appID); err != nil {
				slog.Error("deploy: redeploy after secret files update", "app_id", appID, "error", err)
			}
		}()
	} else {
		s.triggerAppIfGit(appID, appKind)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"secret_file_paths": paths})
}

// handleGitHubWebhook receives GitHub push deliveries for a deployment.
// Outside the JWT-authenticated group — GitHub can't present a bearer token,
// so the per-deployment HMAC secret (X-Hub-Signature-256) is the auth
// boundary instead, exactly GitHub's documented webhook security model.
// Cloning happens asynchronously: GitHub times out webhook responses after
// ~10s, so this handler verifies, decides, and returns 202 immediately,
// leaving deploy.Orchestrator.ProcessPush to run in the background.
func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var branch, webhookSecretID string
	err = s.db.QueryRow(r.Context(), `
		SELECT branch, webhook_secret_id::text FROM deployment_apps WHERE id = $1
	`, appID).Scan(&branch, &webhookSecretID)
	if err != nil {
		writeError(w, http.StatusNotFound, "deployment app not found")
		return
	}

	secret, err := s.vault.GetByID(r.Context(), webhookSecretID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify signature")
		return
	}
	if !deploy.VerifyWebhookSignature(body, r.Header.Get("X-Hub-Signature-256"), secret) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
	case "push":
		var payload struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid push payload")
			return
		}
		if payload.Ref != "refs/heads/"+branch {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "branch mismatch"})
			return
		}

		go func() {
			if err := s.deploy.ProcessPush(context.Background(), appID); err != nil {
				slog.Error("deploy: process push", "app_id", appID, "error", err)
			}
		}()

		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "unsupported event"})
	}
}

// webhookPath returns the right webhook path for a repo URL — /webhooks/gitlab/
// for gitlab.com repos, /webhooks/github/ for everything else.
func webhookPath(repoURL, appID string) string {
	if strings.Contains(repoURL, "gitlab.com") {
		return "/webhooks/gitlab/" + appID
	}
	return "/webhooks/github/" + appID
}

// handleGitLabWebhook receives GitLab push hook deliveries. GitLab authenticates
// with a plain X-Gitlab-Token header (constant-time compared against the stored
// secret) rather than GitHub's HMAC-SHA256 scheme. The push payload's "ref"
// field is identical in both providers, so ProcessPush is shared.
func (s *Server) handleGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")

	var branch, webhookSecretID string
	err := s.db.QueryRow(r.Context(), `
		SELECT branch, webhook_secret_id::text FROM deployment_apps WHERE id = $1
	`, appID).Scan(&branch, &webhookSecretID)
	if err != nil {
		writeError(w, http.StatusNotFound, "deployment app not found")
		return
	}

	secret, err := s.vault.GetByID(r.Context(), webhookSecretID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify token")
		return
	}

	// GitLab sends the secret as a plaintext header, not HMAC — constant-time compare.
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Gitlab-Token")), secret) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	switch event := r.Header.Get("X-Gitlab-Event"); event {
	case "Push Hook", "Tag Push Hook":
		var payload struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid push payload")
			return
		}
		if payload.Ref != "refs/heads/"+branch {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "branch mismatch"})
			return
		}
		go func() {
			if err := s.deploy.ProcessPush(context.Background(), appID); err != nil {
				slog.Error("deploy: process push (gitlab)", "app_id", appID, "error", err)
			}
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "unsupported event"})
	}
}

func (s *Server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, name, endpoint, status, created_at
		FROM k8s_clusters ORDER BY created_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list clusters")
		return
	}
	defer rows.Close()

	type row struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Endpoint  string    `json:"endpoint"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
	}
	var results []row
	for rows.Next() {
		var c row
		if err := rows.Scan(&c.ID, &c.Name, &c.Endpoint, &c.Status, &c.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan cluster")
			return
		}
		results = append(results, c)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

// handleListPods returns the current pod-health rows the K8s watch loop has
// persisted (system namespaces excluded, deleted pods removed). The Clusters
// page seeds its live SSE-driven map from this so it shows the real pod set
// immediately on load and after any remount — instead of flickering from empty
// until the next pod.health event arrives.
func (s *Server) handleListPods(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT cluster_id::text, namespace, pod_name, status, restart_count
		FROM pod_health ORDER BY namespace, pod_name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pods")
		return
	}
	defer rows.Close()

	type row struct {
		ClusterID    string `json:"cluster_id"`
		Namespace    string `json:"namespace"`
		PodName      string `json:"pod_name"`
		Status       string `json:"status"`
		RestartCount int32  `json:"restart_count"`
	}
	var results []row
	for rows.Next() {
		var p row
		if err := rows.Scan(&p.ClusterID, &p.Namespace, &p.PodName, &p.Status, &p.RestartCount); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan pod")
			return
		}
		results = append(results, p)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleListCloudAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, provider, name, created_at
		FROM cloud_accounts ORDER BY created_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list cloud accounts")
		return
	}
	defer rows.Close()

	type row struct {
		ID        string    `json:"id"`
		Provider  string    `json:"provider"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}
	var results []row
	for rows.Next() {
		var a row
		if err := rows.Scan(&a.ID, &a.Provider, &a.Name, &a.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan cloud account")
			return
		}
		results = append(results, a)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

// handleCreateCloudAccount registers a cloud account for the T11 poller to
// query. Credentials are stored encrypted in the vault under a derived secret
// name, and cloud_accounts.credentials_secret_id is resolved to that secret's
// UUID in the same INSERT — mirroring handlePutSecret's "encrypt before it
// touches the database" rule.
func (s *Server) handleCreateCloudAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider    string          `json:"provider"`
		Name        string          `json:"name"`
		Credentials json.RawMessage `json:"credentials"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Provider != "aws" && req.Provider != "azure" {
		writeError(w, http.StatusBadRequest, `provider must be "aws" or "azure"`)
		return
	}
	if req.Name == "" || len(req.Credentials) == 0 {
		writeError(w, http.StatusBadRequest, "name and credentials required")
		return
	}

	validation, err := cloud.ValidateCredentials(r.Context(), req.Provider, req.Credentials)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	secretName := "cloud-account:" + req.Name
	if err := s.vault.Put(r.Context(), secretName, req.Credentials); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store credentials")
		return
	}

	var accountID string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO cloud_accounts (provider, name, credentials_secret_id)
		SELECT $1, $2, id FROM secrets WHERE name = $3
		RETURNING id::text
	`, req.Provider, req.Name, secretName).Scan(&accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create cloud account")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         accountID,
		"provider":   req.Provider,
		"name":       req.Name,
		"validation": validation,
	})
}

// handleDeleteCloudAccount unregisters a cloud account. cloud_resources rows
// for it cascade via FK; if provisioned_resources or terraform_runs still
// reference it, the delete is rejected (23503) until those are cleared.
func (s *Server) handleDeleteCloudAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var name string
	if err := s.db.QueryRow(r.Context(),
		`SELECT name FROM cloud_accounts WHERE id = $1`, id,
	).Scan(&name); err != nil {
		writeError(w, http.StatusNotFound, "cloud account not found")
		return
	}

	if _, err := s.db.Exec(r.Context(), `DELETE FROM cloud_accounts WHERE id = $1`, id); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			writeError(w, http.StatusConflict, "cloud account is still referenced by provisioned resources or terraform runs")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete cloud account")
		return
	}

	if err := s.vault.Delete(r.Context(), "cloud-account:"+name); err != nil {
		slog.Error("api: delete cloud account credentials", "name", name, "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleListCloudAccountNetworks lists existing VPCs/VNets and their subnets
// for a cloud account in the given region — backs the New Resource wizard's
// "use existing network" picker (see the design notes: Terraform engine scope notes:
// existing-resource references are wired as `data` blocks backed by these
// IDs, not remote state).
func (s *Server) handleListCloudAccountNetworks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	region := r.URL.Query().Get("region")
	if region == "" {
		writeError(w, http.StatusBadRequest, "region query parameter required")
		return
	}

	networks, err := cloud.ListNetworks(r.Context(), s.db, s.vault, id, region)
	if err != nil {
		slog.Error("api: list cloud account networks", "cloud_account_id", id, "region", region, "error", err)
		writeError(w, http.StatusBadGateway, "failed to list networks")
		return
	}
	if networks == nil {
		networks = []cloud.Network{}
	}
	writeJSON(w, http.StatusOK, networks)
}

// handleListDockerNetworks lists existing Docker bridge networks for the
// "Docker network" picker on the local-resource (Docker container) form.
func (s *Server) handleListDockerNetworks(w http.ResponseWriter, r *http.Request) {
	if s.dockerMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "docker daemon unavailable — check server logs")
		return
	}
	networks, err := s.dockerMgr.ListNetworks(r.Context())
	if err != nil {
		var notFound *docker.ErrDockerNotFound
		if errors.As(err, &notFound) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		slog.Error("api: list docker networks", "error", err)
		writeError(w, http.StatusBadGateway, "failed to list docker networks")
		return
	}
	if networks == nil {
		networks = []docker.NetworkInfo{}
	}
	writeJSON(w, http.StatusOK, networks)
}

// handleListRegistryImages lists every (repo, tag) in the local registry
// (config.RegistryPush, host-accessible from this process) for the Image
// Registry dashboard page.
func (s *Server) handleListRegistryImages(w http.ResponseWriter, r *http.Request) {
	images, err := registry.ListImages(r.Context(), s.registryHost)
	if err != nil {
		slog.Error("api: list registry images", "error", err)
		writeError(w, http.StatusBadGateway, "failed to list registry images")
		return
	}
	if images == nil {
		images = []registry.ImageInfo{}
	}
	writeJSON(w, http.StatusOK, images)
}

// handleDeleteRegistryImage removes repo:tag from the local registry.
func (s *Server) handleDeleteRegistryImage(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	tag := r.URL.Query().Get("tag")
	if repo == "" || tag == "" {
		writeError(w, http.StatusBadRequest, "repo and tag query parameters are required")
		return
	}

	if err := registry.DeleteImage(r.Context(), s.registryHost, repo, tag); err != nil {
		slog.Error("api: delete registry image", "repo", repo, "tag", tag, "error", err)
		writeError(w, http.StatusBadGateway, "failed to delete image: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type registryImportRequest struct {
	Source string `json:"source"`
	Repo   string `json:"repo"`
	Tag    string `json:"tag"`
}

// handleImportRegistryImage pulls an image from an external reference (e.g.
// "postgres:16") and pushes it into the local registry, optionally under a
// caller-chosen repo/tag.
func (s *Server) handleImportRegistryImage(w http.ResponseWriter, r *http.Request) {
	var req registryImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Source = strings.TrimSpace(req.Source)
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}

	repo, tag := strings.TrimSpace(req.Repo), strings.TrimSpace(req.Tag)
	if repo == "" || tag == "" {
		defRepo, defTag := registry.DefaultRepoTag(req.Source)
		if repo == "" {
			repo = defRepo
		}
		if tag == "" {
			tag = defTag
		}
	}
	if repo == "" {
		writeError(w, http.StatusBadRequest, "could not determine a repo name for this source — specify repo explicitly")
		return
	}

	if err := registry.ImportImage(r.Context(), s.registryHost, req.Source, repo, tag); err != nil {
		slog.Error("api: import registry image", "source", req.Source, "repo", repo, "tag", tag, "error", err)
		writeError(w, http.StatusBadGateway, "failed to import image: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"repo": repo, "tag": tag})
}

func (s *Server) handlePutSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Value == "" {
		writeError(w, http.StatusBadRequest, "name and value required")
		return
	}
	if err := s.vault.Put(r.Context(), req.Name, []byte(req.Value)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store secret")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": req.Name})
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	name := secretNameParam(r)
	value, err := s.vault.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "value": string(value)})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := secretNameParam(r)
	if err := s.vault.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete secret")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListComplianceFindings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT cf.id::text, cf.app_id::text, da.name,
		       cf.scanner, cf.severity, cf.rule_id, cf.message,
		       COALESCE(cf.file_path, ''), COALESCE(cf.line_number, 0),
		       cf.created_at
		FROM compliance_findings cf
		JOIN deployment_apps da ON da.id = cf.app_id
		ORDER BY cf.created_at DESC
		LIMIT 500
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list findings")
		return
	}
	defer rows.Close()

	type row struct {
		ID             string    `json:"id"`
		DeploymentID   string    `json:"deployment_id"`
		DeploymentName string    `json:"deployment_name"`
		Scanner        string    `json:"scanner"`
		Severity       string    `json:"severity"`
		RuleID         string    `json:"rule_id"`
		Message        string    `json:"message"`
		FilePath       string    `json:"file_path,omitempty"`
		LineNumber     int       `json:"line_number,omitempty"`
		CreatedAt      time.Time `json:"created_at"`
	}
	var results []row
	for rows.Next() {
		var f row
		if err := rows.Scan(&f.ID, &f.DeploymentID, &f.DeploymentName,
			&f.Scanner, &f.Severity, &f.RuleID, &f.Message,
			&f.FilePath, &f.LineNumber, &f.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan finding")
			return
		}
		results = append(results, f)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleListCloudResources(w http.ResponseWriter, r *http.Request) {
	// The same real-world resource can be discovered via more than one
	// registered cloud account (e.g. two accounts on the same Azure
	// subscription). Dedupe on the resource's own identity, keeping the
	// most recently polled row for each.
	rows, err := s.db.Query(r.Context(), `
		SELECT id, provider, account_name, resource_type, resource_id, region, name, state, polled_at
		FROM (
			SELECT DISTINCT ON (cr.provider, cr.resource_type, cr.resource_id)
			       cr.id::text AS id, ca.provider, ca.name AS account_name,
			       cr.resource_type, cr.resource_id, cr.region,
			       COALESCE(cr.name, '') AS name, cr.state::text AS state, cr.polled_at
			FROM cloud_resources cr
			JOIN cloud_accounts ca ON ca.id = cr.cloud_account_id
			WHERE cr.dismissed_at IS NULL
			ORDER BY cr.provider, cr.resource_type, cr.resource_id, cr.polled_at DESC
		) deduped
		ORDER BY polled_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list cloud resources")
		return
	}
	defer rows.Close()

	type row struct {
		ID           string    `json:"id"`
		Provider     string    `json:"provider"`
		AccountName  string    `json:"account_name"`
		ResourceType string    `json:"resource_type"`
		ResourceID   string    `json:"resource_id"`
		Region       string    `json:"region"`
		Name         string    `json:"name,omitempty"`
		State        string    `json:"state"`
		PolledAt     time.Time `json:"polled_at"`
	}
	var results []row
	for rows.Next() {
		var cr row
		if err := rows.Scan(&cr.ID, &cr.Provider, &cr.AccountName,
			&cr.ResourceType, &cr.ResourceID, &cr.Region,
			&cr.Name, &cr.State, &cr.PolledAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan resource")
			return
		}
		results = append(results, cr)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

// handleDismissCloudResource hides a cloud_resources row from the default
// Cloud Inventory list. The poller's upsert doesn't touch dismissed_at, so
// this persists across polls; once the resource truly disappears from the
// provider's API, pruneStaleResources deletes the row outright.
func (s *Server) handleDismissCloudResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tag, err := s.db.Exec(r.Context(), `UPDATE cloud_resources SET dismissed_at = now() WHERE id = $1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to dismiss cloud resource")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "cloud resource not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed", "id": id})
}

func (s *Server) handleCreateCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "name and endpoint required")
		return
	}

	var id string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO k8s_clusters (name, endpoint, status)
		VALUES ($1, $2, 'unknown')
		ON CONFLICT (name) DO UPDATE SET endpoint = EXCLUDED.endpoint
		RETURNING id::text
	`, req.Name, req.Endpoint).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create cluster")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": req.Name, "endpoint": req.Endpoint})
}

func (s *Server) handleListTerraformRuns(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, COALESCE(deployment_id::text,''), COALESCE(resource_id::text,''), COALESCE(cloud_account_id::text,''),
		       template, action, status, COALESCE(plan_output,''), created_at
		FROM terraform_runs ORDER BY created_at DESC LIMIT 100
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list terraform runs")
		return
	}
	defer rows.Close()
	type row struct {
		ID             string    `json:"id"`
		DeploymentID   string    `json:"deployment_id,omitempty"`
		ResourceID     string    `json:"resource_id,omitempty"`
		CloudAccountID string    `json:"cloud_account_id,omitempty"`
		Template       string    `json:"template"`
		Action         string    `json:"action"`
		Status         string    `json:"status"`
		PlanOutput     string    `json:"plan_output,omitempty"`
		CreatedAt      time.Time `json:"created_at"`
	}
	var results []row
	for rows.Next() {
		var rr row
		if err := rows.Scan(&rr.ID, &rr.DeploymentID, &rr.ResourceID, &rr.CloudAccountID,
			&rr.Template, &rr.Action, &rr.Status, &rr.PlanOutput, &rr.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan run")
			return
		}
		results = append(results, rr)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

// resourceRow is a provisioned_resources row — a standalone, Terraform-backed
// AWS/Azure resource with no git deployment behind it (the "Resources" tab,
// distinct from git-driven Deployments). Its status reflects the most recent
// plan/apply/destroy run; the run history itself is in terraform_runs,
// grouped by resource_id (see GET /terraform/runs).
type resourceRow struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Provider           string `json:"provider"`
	CloudAccountID     string `json:"cloud_account_id,omitempty"`
	Region             string `json:"region"`
	ResourceType       string `json:"resource_type"`
	Template           string `json:"template"`
	Status             string `json:"status"`
	// RegistryResourceID, for resource_type "ecs"/"aci", optionally links to
	// an applied "ecr"/"acr" provisioned_resources row whose outputs supply
	// the registry connection info for git-app deploys onto this resource
	// (see loadECSResource/loadACIResource in internal/deploy).
	RegistryResourceID string `json:"registry_resource_id,omitempty"`
	// Outputs is the JSON-encoded terraform outputs (vpc_id, public IP, db
	// endpoint, etc. — see `output` blocks in templates/*/stack.tf.tmpl),
	// "{}" until the first successful Apply. Cast to text in SQL (same
	// pattern as cloud_resources.state) and re-parsed by the frontend with
	// JSON.parse.
	Outputs   string    `json:"outputs"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Server) handleListResources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, name, provider, COALESCE(cloud_account_id::text,''),
		       region, resource_type, template, status, COALESCE(outputs::text, '{}'),
		       COALESCE(registry_resource_id::text, ''), created_at, updated_at
		FROM provisioned_resources WHERE dismissed_at IS NULL ORDER BY created_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list resources")
		return
	}
	defer rows.Close()

	results := []resourceRow{}
	for rows.Next() {
		var rr resourceRow
		if err := rows.Scan(&rr.ID, &rr.Name, &rr.Provider, &rr.CloudAccountID,
			&rr.Region, &rr.ResourceType, &rr.Template, &rr.Status, &rr.Outputs, &rr.RegistryResourceID, &rr.CreatedAt, &rr.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan resource")
			return
		}
		results = append(results, rr)
	}
	writeJSON(w, http.StatusOK, results)
}

// handleGetResource returns a single provisioned_resources row, including the
// terraform outputs captured after the last successful Apply (vpc_id, public
// IP, db endpoint, etc.) — backs the resource detail page.
func (s *Server) handleGetResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	writeJSON(w, http.StatusOK, rr)
}

// getResource loads a single provisioned_resources row, used both for the
// create response and to look up cloud_account_id/template before Apply/Destroy.
func (s *Server) getResource(ctx context.Context, id string) (resourceRow, error) {
	var rr resourceRow
	err := s.db.QueryRow(ctx, `
		SELECT id::text, name, provider, COALESCE(cloud_account_id::text,''),
		       region, resource_type, template, status, COALESCE(outputs::text, '{}'),
		       COALESCE(registry_resource_id::text, ''), created_at, updated_at
		FROM provisioned_resources WHERE id = $1
	`, id).Scan(&rr.ID, &rr.Name, &rr.Provider, &rr.CloudAccountID,
		&rr.Region, &rr.ResourceType, &rr.Template, &rr.Status, &rr.Outputs, &rr.RegistryResourceID, &rr.CreatedAt, &rr.UpdatedAt)
	return rr, err
}

// validateRegistryLink checks that registryResourceID (if non-empty) is a
// valid optional link target for a resource of resourceType/provider — an
// applied "ecr" resource (for "ecs") or "acr" resource (for "aci") owned by
// the same provider. Used by handleCreateResource and
// handleUpdateResourceRegistry; loadECSResource/loadACIResource
// (internal/deploy) read the linked row's outputs for registry connection
// info at deploy time.
func (s *Server) validateRegistryLink(ctx context.Context, provider, resourceType, registryResourceID string) error {
	if registryResourceID == "" {
		return nil
	}
	var wantType string
	switch resourceType {
	case "ecs":
		wantType = "ecr"
	case "aci":
		wantType = "acr"
	default:
		return fmt.Errorf("registry_resource_id is only valid for ecs/aci resources")
	}
	rr, err := s.getResource(ctx, registryResourceID)
	if err != nil {
		return fmt.Errorf("registry resource not found")
	}
	if rr.Provider != provider || rr.ResourceType != wantType {
		return fmt.Errorf("registry_resource_id must reference an applied %s %s resource", provider, wantType)
	}
	if rr.Status != "applied" {
		return fmt.Errorf("registry resource %q is not applied yet", rr.Name)
	}
	return nil
}

// handleGetResourceTerraform returns the exact Terraform HCL last planned
// for this resource — read from its workspace's main.tf, whether generated
// by buildTerraformConfig or hand-edited via handlePlanResourceTerraform.
// Backs the resource detail page's "View Terraform" panel and download
// action.
func (s *Server) handleGetResourceTerraform(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if rr.Provider == "local" {
		writeError(w, http.StatusBadRequest, "terraform config is not applicable to local resources")
		return
	}
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform binary unavailable — check server logs")
		return
	}

	config, err := s.tfRunner.ReadConfig("", id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "no terraform configuration has been planned for this resource yet")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to read terraform config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"config": string(config)})
}

// setResourceStatus updates a resource's status and publishes the refreshed
// row over SSE — the "resource" event the Resources page listens for, mirroring
// how Runner.publish streams "terraform.run" events for the run-log view.
func (s *Server) setResourceStatus(ctx context.Context, id, status string) {
	if _, err := s.db.Exec(ctx, `UPDATE provisioned_resources SET status = $2, updated_at = now() WHERE id = $1`, id, status); err != nil {
		slog.Error("resources: update status", "id", id, "status", status, "error", err)
		return
	}
	rr, err := s.getResource(ctx, id)
	if err != nil {
		slog.Error("resources: reload after status update", "id", id, "error", err)
		return
	}
	s.hub.Publish(sse.Event{Type: "resource", Payload: rr})
}

// setResourceOutputs persists the terraform outputs captured after a
// successful Apply (vpc_id, public/private IPs, RDS endpoint, db FQDN, etc. —
// see the `output` blocks in internal/terraform/templates/*/stack.tf.tmpl)
// for the resource detail page. A nil/empty map clears any previously stored
// outputs (used after Destroy, since the underlying infrastructure is gone).
func (s *Server) setResourceOutputs(ctx context.Context, id string, outputs map[string]json.RawMessage) {
	var data []byte
	if len(outputs) > 0 {
		var err error
		data, err = json.Marshal(outputs)
		if err != nil {
			slog.Error("resources: marshal outputs", "id", id, "error", err)
			return
		}
	}
	if _, err := s.db.Exec(ctx, `UPDATE provisioned_resources SET outputs = $2, updated_at = now() WHERE id = $1`, id, data); err != nil {
		slog.Error("resources: update outputs", "id", id, "error", err)
	}
}

// handlePreviewResource renders the Terraform HCL for the given resource
// form without any side effects — no provisioned_resources row, no
// terraform-exec call. Lets the /new-resource wizard show the generated
// config before the user confirms creation, reusing the same
// buildTerraformConfig path handleCreateResource uses for the real plan.
func (s *Server) handlePreviewResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string            `json:"name"`
		Provider     string            `json:"provider"`
		Region       string            `json:"region"`
		ResourceType string            `json:"resource_type"`
		ResourceOpts map[string]string `json:"resource_opts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Provider == "local" {
		writeError(w, http.StatusBadRequest, "terraform preview is not applicable to local resources")
		return
	}
	if req.Name == "" || req.Provider == "" || req.Region == "" || req.ResourceType == "" {
		writeError(w, http.StatusBadRequest, "name, provider, region, resource_type required")
		return
	}

	config, err := s.buildTerraformConfig(req.Provider, req.ResourceType, req.Name, req.Region, req.ResourceOpts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"config": string(config)})
}

// handleGenerateSSHKey creates a new ed25519 SSH keypair for the New
// Resource wizard's "generate a key for me" option. The private key is
// returned once and never stored server-side — it's the user's
// responsibility to copy/download it from the response before it's gone.
func (s *Server) handleGenerateSSHKey(w http.ResponseWriter, r *http.Request) {
	publicKey, privateKey, err := generateSSHKeyPair()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate SSH key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"public_key":  publicKey,
		"private_key": privateKey,
	})
}

// handleCreateResource provisions a standalone cloud resource for the
// Resources tab — registers it in provisioned_resources and runs `terraform
// plan` in the background. Unlike POST /deployments, no git repo is involved.
func (s *Server) handleCreateResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name               string            `json:"name"`
		Provider           string            `json:"provider"`
		CloudAccountID     string            `json:"cloud_account_id"`
		Region             string            `json:"region"`
		ResourceType       string            `json:"resource_type"`
		ResourceOpts       map[string]string `json:"resource_opts"`
		RegistryResourceID string            `json:"registry_resource_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Provider == "" || req.ResourceType == "" {
		writeError(w, http.StatusBadRequest, "name, provider, resource_type required")
		return
	}

	if req.Provider == "local" {
		s.createLocalResource(w, r, req.Name, req.ResourceType, req.ResourceOpts)
		return
	}

	if req.CloudAccountID == "" || req.Region == "" {
		writeError(w, http.StatusBadRequest, "cloud_account_id, region required")
		return
	}
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform binary unavailable — check server logs")
		return
	}
	if err := s.validateRegistryLink(r.Context(), req.Provider, req.ResourceType, req.RegistryResourceID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	config, err := s.buildTerraformConfig(req.Provider, req.ResourceType, req.Name, req.Region, req.ResourceOpts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	template := req.Provider + "-stack"
	var resourceID string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO provisioned_resources (name, provider, cloud_account_id, region, resource_type, template, status, registry_resource_id)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', NULLIF($7, '')::uuid)
		RETURNING id::text
	`, req.Name, req.Provider, req.CloudAccountID, req.Region, req.ResourceType, template, req.RegistryResourceID).Scan(&resourceID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, fmt.Sprintf("a resource named %q already exists — choose a different name or dismiss the existing one", req.Name))
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register resource")
		return
	}

	rr, err := s.getResource(r.Context(), resourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load created resource")
		return
	}

	go func() {
		ctx := context.Background()
		status := "planned"
		if _, err := s.tfRunner.Plan(ctx, "", resourceID, req.CloudAccountID, template, config); err != nil {
			slog.Error("terraform: plan failed", "name", req.Name, "error", err)
			status = "failed"
		}
		s.setResourceStatus(ctx, resourceID, status)
	}()

	writeJSON(w, http.StatusAccepted, rr)
}

// createLocalResource provisions a standalone "local" Resource — a Docker
// container or K8s workload on the existing kind cluster, created directly
// via the Docker SDK / client-go (no Terraform, no cloud_account_id). Unlike
// the AWS/Azure path, create = immediate apply: the row goes
// pending -> applying -> applied/failed in the background, skipping
// planning/planned entirely.
func (s *Server) createLocalResource(w http.ResponseWriter, r *http.Request, name, resourceType string, opts map[string]string) {
	image := strOpt(opts, "image", "")
	if image == "" {
		writeError(w, http.StatusBadRequest, "resource_opts.image required")
		return
	}
	port, err := intOpt(opts, "container_port", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if port <= 0 || port > 65535 {
		writeError(w, http.StatusBadRequest, "resource_opts.container_port must be between 1 and 65535")
		return
	}
	hostPortOpt, err := intOpt(opts, "host_port", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hostPortOpt < 0 || hostPortOpt > 65535 {
		writeError(w, http.StatusBadRequest, "resource_opts.host_port must be between 1 and 65535")
		return
	}
	networkName := strOpt(opts, "network", "")
	createNetwork := strOpt(opts, "create_network", "") == "true"

	var template string
	switch resourceType {
	case "docker":
		if s.dockerMgr == nil {
			writeError(w, http.StatusServiceUnavailable, "docker daemon unavailable — check server logs")
			return
		}
		template = "docker-local"
	case "k8s":
		if s.k8sCtrl == nil {
			writeError(w, http.StatusServiceUnavailable, "kubernetes cluster unavailable — check server logs")
			return
		}
		template = "k8s-local"
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported local resource_type %q", resourceType))
		return
	}

	exposeHTTP := strOpt(opts, "expose_http", "") == "true"

	var resourceID string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO provisioned_resources (name, provider, region, resource_type, template, status)
		VALUES ($1, 'local', 'local', $2, $3, 'pending')
		RETURNING id::text
	`, name, resourceType, template).Scan(&resourceID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, fmt.Sprintf("a resource named %q already exists — choose a different name or dismiss the existing one", name))
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register resource")
		return
	}

	rr, err := s.getResource(r.Context(), resourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load created resource")
		return
	}

	go func() {
		ctx := context.Background()
		s.setResourceStatus(ctx, resourceID, "applying")

		var (
			outputs  map[string]json.RawMessage
			summary  string
			applyErr error
		)
		switch resourceType {
		case "docker":
			containerID, hostPort, err := s.dockerMgr.CreateContainer(ctx, name, image, uint16(port), uint16(hostPortOpt), networkName, createNetwork, resourceID)
			if err != nil {
				applyErr = err
				break
			}
			networkUsed := networkName
			if networkUsed == "" {
				networkUsed = docker.DefaultNetwork
			}
			outputs = map[string]json.RawMessage{
				"image":          jsonString(image),
				"container_id":   jsonString(containerID),
				"container_port": json.RawMessage(strconv.Itoa(port)),
				"host_port":      json.RawMessage(strconv.FormatUint(uint64(hostPort), 10)),
				"network":        jsonString(networkUsed),
			}
			summary = fmt.Sprintf("container %s started on host port %d", containerID, hostPort)
		case "k8s":
			namespace := k8s.ResourceNamespace(name)
			serviceIP, ingressURL, err := s.k8sCtrl.KubeDeployResource(ctx, name, namespace, image, int32(port), exposeHTTP)
			if err != nil {
				applyErr = err
				break
			}
			outputs = map[string]json.RawMessage{
				"image":       jsonString(image),
				"namespace":   jsonString(namespace),
				"service_ip":  jsonString(serviceIP),
				"port":        json.RawMessage(strconv.Itoa(port)),
				"expose_http": json.RawMessage(strconv.FormatBool(exposeHTTP)),
			}
			if ingressURL != "" {
				outputs["ingress_url"] = jsonString(ingressURL)
			}
			summary = fmt.Sprintf("deployment %s/%s available (service %s)", namespace, name, serviceIP)
		}

		status := "applied"
		runStatus := "succeeded"
		runOutput := summary
		if applyErr != nil {
			slog.Error("resources: local apply failed", "id", resourceID, "type", resourceType, "error", applyErr)
			status = "failed"
			runStatus = "failed"
			runOutput = applyErr.Error()
		} else {
			s.setResourceOutputs(ctx, resourceID, outputs)
		}
		s.insertSyntheticRun(ctx, resourceID, template, "apply", runStatus, runOutput)
		s.setResourceStatus(ctx, resourceID, status)
	}()

	writeJSON(w, http.StatusAccepted, rr)
}

// handleEditResource updates the settings of a "local" Resource (Docker
// container or K8s workload) — the edit-path counterpart to
// createLocalResource. Docker edits recreate the container via the
// already-idempotent CreateContainer (brief restart); K8s edits patch the
// Deployment/Service/Ingress in place via KubeUpdateResource. AWS/Azure
// resources are edited by re-planning Terraform (see
// handlePlanResourceTerraform), not through this endpoint.
func (s *Server) handleEditResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if rr.Provider != "local" {
		writeError(w, http.StatusBadRequest, "editing is only supported for local resources")
		return
	}
	if rr.Status != "applied" && rr.Status != "failed" {
		writeError(w, http.StatusConflict, fmt.Sprintf("resource is %s — editing is only available for applied or failed resources", rr.Status))
		return
	}

	var req struct {
		ResourceOpts map[string]string `json:"resource_opts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	opts := req.ResourceOpts

	image := strOpt(opts, "image", "")
	if image == "" {
		writeError(w, http.StatusBadRequest, "resource_opts.image required")
		return
	}
	port, err := intOpt(opts, "container_port", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if port <= 0 || port > 65535 {
		writeError(w, http.StatusBadRequest, "resource_opts.container_port must be between 1 and 65535")
		return
	}
	hostPortOpt, err := intOpt(opts, "host_port", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hostPortOpt < 0 || hostPortOpt > 65535 {
		writeError(w, http.StatusBadRequest, "resource_opts.host_port must be between 1 and 65535")
		return
	}
	networkName := strOpt(opts, "network", "")
	createNetwork := strOpt(opts, "create_network", "") == "true"
	exposeHTTP := strOpt(opts, "expose_http", "") == "true"

	switch rr.ResourceType {
	case "docker":
		if s.dockerMgr == nil {
			writeError(w, http.StatusServiceUnavailable, "docker daemon unavailable — check server logs")
			return
		}
	case "k8s":
		if s.k8sCtrl == nil {
			writeError(w, http.StatusServiceUnavailable, "kubernetes cluster unavailable — check server logs")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported local resource_type %q", rr.ResourceType))
		return
	}

	s.setResourceStatus(r.Context(), id, "applying")

	go func() {
		ctx := context.Background()

		var (
			outputs  map[string]json.RawMessage
			summary  string
			applyErr error
		)
		switch rr.ResourceType {
		case "docker":
			containerID, hostPort, err := s.dockerMgr.CreateContainer(ctx, rr.Name, image, uint16(port), uint16(hostPortOpt), networkName, createNetwork, id)
			if err != nil {
				applyErr = err
				break
			}
			networkUsed := networkName
			if networkUsed == "" {
				networkUsed = docker.DefaultNetwork
			}
			outputs = map[string]json.RawMessage{
				"image":          jsonString(image),
				"container_id":   jsonString(containerID),
				"container_port": json.RawMessage(strconv.Itoa(port)),
				"host_port":      json.RawMessage(strconv.FormatUint(uint64(hostPort), 10)),
				"network":        jsonString(networkUsed),
			}
			summary = fmt.Sprintf("container %s updated, running on host port %d", containerID, hostPort)
		case "k8s":
			namespace := k8s.ResourceNamespace(rr.Name)
			serviceIP, ingressURL, err := s.k8sCtrl.KubeUpdateResource(ctx, rr.Name, namespace, image, int32(port), exposeHTTP)
			if err != nil {
				applyErr = err
				break
			}
			outputs = map[string]json.RawMessage{
				"image":       jsonString(image),
				"namespace":   jsonString(namespace),
				"service_ip":  jsonString(serviceIP),
				"port":        json.RawMessage(strconv.Itoa(port)),
				"expose_http": json.RawMessage(strconv.FormatBool(exposeHTTP)),
			}
			if ingressURL != "" {
				outputs["ingress_url"] = jsonString(ingressURL)
			}
			summary = fmt.Sprintf("deployment %s/%s updated (service %s)", namespace, rr.Name, serviceIP)
		}

		status := "applied"
		runStatus := "succeeded"
		runOutput := summary
		if applyErr != nil {
			slog.Error("resources: local edit failed", "id", id, "type", rr.ResourceType, "error", applyErr)
			status = "failed"
			runStatus = "failed"
			runOutput = applyErr.Error()
		} else {
			s.setResourceOutputs(ctx, id, outputs)
		}
		s.insertSyntheticRun(ctx, id, rr.Template, "update", runStatus, runOutput)
		s.setResourceStatus(ctx, id, status)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "edit_initiated", "id": id})
}

// insertSyntheticRun records a completed action for a "local" Resource — no
// terraform-exec was involved (the action was a direct Docker SDK / client-go
// call), but a terraform_runs row reuses the existing "Latest Run" card on
// the resource detail page and the GET /terraform/runs history for free.
func (s *Server) insertSyntheticRun(ctx context.Context, resourceID, template, action, status, output string) {
	if _, err := s.db.Exec(ctx, `
		INSERT INTO terraform_runs (resource_id, template, action, status, plan_output)
		VALUES ($1, $2, $3, $4, $5)
	`, resourceID, template, action, status, output); err != nil {
		slog.Error("resources: insert synthetic run", "id", resourceID, "error", err)
	}
}

// jsonString marshals a Go string to a JSON string value for assembly into a
// resourceRow.Outputs map[string]json.RawMessage — json.Marshal on a string
// never errors.
func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// handleApplyResource runs `terraform apply` against a resource's saved plan.
func (s *Server) handleApplyResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform binary unavailable — check server logs")
		return
	}

	s.setResourceStatus(r.Context(), id, "applying")

	go func() {
		ctx := context.Background()
		status := "applied"
		run, err := s.tfRunner.Apply(ctx, "", id, rr.CloudAccountID, rr.Template)
		if err != nil {
			slog.Error("terraform: apply failed", "id", id, "error", err)
			status = "failed"
		} else {
			s.setResourceOutputs(ctx, id, run.Outputs)
		}
		s.setResourceStatus(ctx, id, status)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "apply_initiated", "id": id})
}

// handlePlanResourceTerraform re-plans a resource's workspace against
// hand-edited Terraform HCL submitted from the resource detail page — the
// same Runner.Plan used by handleCreateResource, but with user-supplied
// config bytes in place of buildTerraformConfig's output. On success the
// resource becomes 'planned' again, so the existing Apply action applies the
// edited config against the workspace's current state.
func (s *Server) handlePlanResourceTerraform(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if rr.Provider == "local" {
		writeError(w, http.StatusBadRequest, "terraform config is not applicable to local resources")
		return
	}
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform binary unavailable — check server logs")
		return
	}

	var req struct {
		Config string `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Config) == "" {
		writeError(w, http.StatusBadRequest, "config required")
		return
	}

	s.setResourceStatus(r.Context(), id, "planning")

	go func() {
		ctx := context.Background()
		status := "planned"
		if _, err := s.tfRunner.Plan(ctx, "", id, rr.CloudAccountID, rr.Template, []byte(req.Config)); err != nil {
			slog.Error("terraform: re-plan with edited config failed", "id", id, "error", err)
			status = "failed"
		}
		s.scanWorkspaceTfsec(ctx, id) // tfsec the (re)written config — report-only
		s.setResourceStatus(ctx, id, status)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "plan_initiated", "id": id})
}

// handleDestroyResource runs `terraform destroy` against a resource's
// workspace (AWS/Azure), or removes the underlying Docker container / K8s
// namespace directly for provider='local' resources (see
// destroyLocalResource).
func (s *Server) handleDestroyResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	if rr.Provider == "local" {
		s.destroyLocalResource(w, r, rr)
		return
	}

	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform binary unavailable — check server logs")
		return
	}

	s.setResourceStatus(r.Context(), id, "destroying")

	go func() {
		ctx := context.Background()
		status := "destroyed"
		if _, err := s.tfRunner.Destroy(ctx, "", id, rr.CloudAccountID, rr.Template); err != nil {
			slog.Error("terraform: destroy failed", "id", id, "error", err)
			status = "failed"
		} else {
			s.setResourceOutputs(ctx, id, nil)
		}
		s.setResourceStatus(ctx, id, status)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "destroy_initiated", "id": id})
}

// destroyLocalResource removes the Docker container / K8s namespace backing
// a "local" Resource — the destroy-path counterpart to createLocalResource.
// The K8s namespace is recomputed via k8s.ResourceNamespace(rr.Name) (the
// same deterministic derivation used at create time), so no outputs lookup
// is needed.
func (s *Server) destroyLocalResource(w http.ResponseWriter, r *http.Request, rr resourceRow) {
	s.setResourceStatus(r.Context(), rr.ID, "destroying")

	go func() {
		ctx := context.Background()
		var destroyErr error
		switch rr.ResourceType {
		case "docker":
			if s.dockerMgr == nil {
				destroyErr = fmt.Errorf("docker manager unavailable")
				break
			}
			destroyErr = s.dockerMgr.RemoveContainer(ctx, rr.Name)
		case "k8s":
			if s.k8sCtrl == nil {
				destroyErr = fmt.Errorf("k8s controller unavailable")
				break
			}
			destroyErr = s.k8sCtrl.KubeDestroyResource(ctx, k8s.ResourceNamespace(rr.Name))
		default:
			destroyErr = fmt.Errorf("unsupported local resource_type %q", rr.ResourceType)
		}

		status := "destroyed"
		runStatus := "succeeded"
		runOutput := fmt.Sprintf("%s resource %q removed", rr.ResourceType, rr.Name)
		if destroyErr != nil {
			slog.Error("resources: local destroy failed", "id", rr.ID, "type", rr.ResourceType, "error", destroyErr)
			status = "failed"
			runStatus = "failed"
			runOutput = destroyErr.Error()
		} else {
			s.setResourceOutputs(ctx, rr.ID, nil)
		}
		s.insertSyntheticRun(ctx, rr.ID, rr.Template, "destroy", runStatus, runOutput)
		s.setResourceStatus(ctx, rr.ID, status)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "destroy_initiated", "id": rr.ID})
}

// handleDismissResource hides a finished provisioned_resources row from the
// default Resources list. Only resources with no remaining live
// infrastructure (status = destroyed) can be dismissed — the row and its
// terraform_runs history stay in the database as an audit trail, just
// excluded from GET /resources.
func (s *Server) handleDismissResource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tag, err := s.db.Exec(r.Context(), `
		UPDATE provisioned_resources SET dismissed_at = now()
		WHERE id = $1 AND status = 'destroyed'
	`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to dismiss resource")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "resource not found or not in a dismissable state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed", "id": id})
}

// handleUpdateResourceRegistry sets or clears the optional registry link on
// an ECS/ACI resource — the linked "ecr"/"acr" resource's outputs supply the
// registry connection info loadECSResource/loadACIResource (internal/deploy)
// use when redeploying a git app onto this resource. An empty
// registry_resource_id clears the link.
func (s *Server) handleUpdateResourceRegistry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	var req struct {
		RegistryResourceID string `json:"registry_resource_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.validateRegistryLink(r.Context(), rr.Provider, rr.ResourceType, req.RegistryResourceID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := s.db.Exec(r.Context(), `
		UPDATE provisioned_resources SET registry_resource_id = NULLIF($2, '')::uuid, updated_at = now() WHERE id = $1
	`, id, req.RegistryResourceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update registry link")
		return
	}

	updated, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload resource")
		return
	}
	s.hub.Publish(sse.Event{Type: "resource", Payload: updated})
	writeJSON(w, http.StatusOK, updated)
}

func strOpt(opts map[string]string, key, def string) string {
	if v, ok := opts[key]; ok && v != "" {
		return v
	}
	return def
}

// intOpt parses opts[key] as an int, falling back to def if the key is
// absent and returning a friendly error if it's present but not a number.
func intOpt(opts map[string]string, key string, def int) (int, error) {
	v, ok := opts[key]
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s %q: must be a whole number", key, v)
	}
	return n, nil
}

// floatOpt parses opts[key] as a float64, falling back to def if the key is
// absent and returning a friendly error if it's present but not a number.
func floatOpt(opts map[string]string, key string, def float64) (float64, error) {
	v, ok := opts[key]
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q: must be a number", key, v)
	}
	return n, nil
}

// acrNameSanitizer strips everything but lowercase letters and digits from a
// resource name to satisfy Azure Container Registry's alphanumeric-only
// naming rule (internal/terraform.ACRParams.Validate enforces 5-50 chars).
var acrNameSanitizer = regexp.MustCompile(`[^a-z0-9]`)

// generateACRName derives a globally-unique Azure Container Registry name
// from the resource's name plus an 8-hex-char random suffix — ACR names must
// be unique across all of Azure, alphanumeric only, 5-50 characters
// (internal/terraform.ACRParams.Validate). The suffix alone satisfies the
// 5-char minimum even if name sanitizes to empty.
func generateACRName(name string) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate acr name: %w", err)
	}
	base := acrNameSanitizer.ReplaceAllString(strings.ToLower(name), "")
	if len(base) > 42 {
		base = base[:42]
	}
	return base + hex.EncodeToString(suffix), nil
}

// awsZonalSubnets builds the two-AZ subnet pair RDS and ECS both require for
// their subnet groups / ALB placement, derived from the stack's region — the
// wizard doesn't collect subnet placement directly, so AutoDevOps carves out
// fixed, non-overlapping blocks within the standard 10.0.0.0/16 VPC.
func awsZonalSubnets(region string) []terraform.AWSSubnet {
	return []terraform.AWSSubnet{
		{CIDR: "10.0.2.0/24", AvailabilityZone: region + "a"},
		{CIDR: "10.0.3.0/24", AvailabilityZone: region + "b"},
	}
}

// buildTerraformConfig renders the HCL for one deployment's combined
// Terraform stack via the engine's typed, validated parameter structs —
// resource_opts (free-form strings from the wizard) are converted to the
// concrete types AWSStackParams/AzureStackParams expect, and Validate()
// surfaces any missing/malformed fields as a friendly error before anything
// is written to disk.
func (s *Server) buildTerraformConfig(provider, resourceType, name, region string, opts map[string]string) ([]byte, error) {
	if opts == nil {
		opts = map[string]string{}
	}
	if s.tfEngine == nil {
		return nil, fmt.Errorf("terraform engine unavailable")
	}

	switch provider {
	case "aws":
		params := terraform.AWSStackParams{Name: name, Region: region, VPCCIDR: "10.0.0.0/16"}
		if vpcID := strOpt(opts, "existing_vpc_id", ""); vpcID != "" {
			params.ExistingVPCID = vpcID
		}
		switch resourceType {
		case "vm":
			params.EC2 = &terraform.EC2Params{
				InstanceType:     strOpt(opts, "instance_type", "t3.micro"),
				SubnetCIDR:       "10.0.1.0/24",
				AvailabilityZone: region + "a",
			}
		case "rds":
			engine := strOpt(opts, "engine", "postgres")
			// Major-version-only: RDS resolves this to its current default
			// minor at creation time, and the AWS provider suppresses the
			// resulting diff (state ends up e.g. "16.10") — pinning a full
			// "16.4" goes stale as AWS retires old minors and breaks plan.
			engineVersion, port := "16", 5432
			if engine == "mysql" {
				engineVersion, port = "8.0", 3306
			}
			storage, err := intOpt(opts, "allocated_storage", 20)
			if err != nil {
				return nil, err
			}
			params.RDS = &terraform.RDSParams{
				Engine:           engine,
				EngineVersion:    engineVersion,
				InstanceClass:    strOpt(opts, "instance_class", "db.t3.micro"),
				AllocatedStorage: storage,
				// "admin" is a reserved master username on RDS for both
				// postgres and mysql — AWS rejects CreateDBInstance with
				// InvalidParameterValue if used.
				Username: strOpt(opts, "username", "appadmin"),
				Port:     port,
				Subnets:  awsZonalSubnets(region),
			}
		case "ecs":
			// Default 0.5 vCPU / 1GB — the previous 256/512 starves a real web
			// app (nginx + php-fpm/node + the app + DB work), which gets
			// OOM-killed and silently restarted by ECS. Still a single small task.
			cpu, err := intOpt(opts, "cpu", 512)
			if err != nil {
				return nil, err
			}
			memory, err := intOpt(opts, "memory", 1024)
			if err != nil {
				return nil, err
			}
			desiredCount, err := intOpt(opts, "desired_count", 1)
			if err != nil {
				return nil, err
			}
			// ECS hosts Paketo-built git apps that bind $PORT=8080 as the non-root
			// cnb user (privileged ports <1024 are unbindable), so the whole stack —
			// task def, ALB target group, task SG — is pinned to 8080 rather than the
			// wizard's port field. ACI solves the same problem at redeploy time; ECS
			// can't, because the ALB/SG are baked at apply time.
			params.ECS = &terraform.ECSParams{
				ContainerImage: strOpt(opts, "image", "nginx:latest"),
				ContainerPort:  8080,
				CPU:            cpu,
				Memory:         memory,
				DesiredCount:   desiredCount,
				Subnets:        awsZonalSubnets(region),
			}
		case "ecr":
			params.ECR = &terraform.ECRParams{}
		default:
			return nil, fmt.Errorf("unsupported aws resource type %q", resourceType)
		}
		out, err := s.tfEngine.Render(terraform.AWSStackID, params)
		if err != nil {
			return nil, err
		}
		return out, nil

	case "azure":
		params := terraform.AzureStackParams{Name: name, Location: region, VNetCIDR: "10.0.0.0/16"}
		if vnetName := strOpt(opts, "existing_vnet_name", ""); vnetName != "" {
			params.ExistingVNet = &terraform.AzureExistingVNet{
				Name:          vnetName,
				ResourceGroup: strOpt(opts, "existing_vnet_resource_group", ""),
			}
		}
		switch resourceType {
		case "aci":
			cpu, err := floatOpt(opts, "cpu", 1)
			if err != nil {
				return nil, err
			}
			memoryGB, err := floatOpt(opts, "memory_gb", 1.5)
			if err != nil {
				return nil, err
			}
			port, err := intOpt(opts, "port", 80)
			if err != nil {
				return nil, err
			}
			params.ACI = &terraform.AzureACIParams{
				ContainerImage: strOpt(opts, "image", "nginx:latest"),
				ContainerPort:  port,
				CPU:            cpu,
				MemoryGB:       memoryGB,
			}
		case "acr":
			acrName, err := generateACRName(name)
			if err != nil {
				return nil, err
			}
			params.ACR = &terraform.ACRParams{Name: acrName}
		case "vm":
			params.VM = &terraform.AzureVMParams{
				Size:          strOpt(opts, "size", "Standard_D2as_v7"),
				SubnetCIDR:    "10.0.1.0/24",
				AdminUsername: strOpt(opts, "admin_username", "azureuser"),
				SSHPublicKey:  strOpt(opts, "ssh_public_key", ""),
			}
		case "db":
			storageMB, err := intOpt(opts, "storage_mb", 32768)
			if err != nil {
				return nil, err
			}
			params.DB = &terraform.AzureDBParams{
				Version:               strOpt(opts, "version", "16"),
				SKU:                   strOpt(opts, "sku", "B_Standard_B1ms"),
				StorageMB:             storageMB,
				AADAdminObjectID:      strOpt(opts, "aad_admin_object_id", ""),
				AADAdminPrincipalName: strOpt(opts, "aad_admin_principal_name", ""),
				AADAdminPrincipalType: strOpt(opts, "aad_admin_principal_type", "User"),
			}
		default:
			return nil, fmt.Errorf("unsupported azure resource type %q", resourceType)
		}
		out, err := s.tfEngine.Render(terraform.AzureStackID, params)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown provider %q", provider)
}

// teardownApp removes the live K8s/Docker resources for a single app, shared
// by handleDeleteApp (per-app) and handleDestroyDeployment (whole-stack,
// called once per app before any shared namespace/network is removed). For
// local_k8s, only the app's own Deployment/Service/Ingress are deleted — the
// stack namespace is shared by sibling apps and is removed separately once
// the whole stack is torn down. A nil controller/manager (K8s or Docker
// unavailable) is treated as a no-op so the DB row can still be soft-deleted.
func (s *Server) teardownApp(ctx context.Context, stackName, appName, targetKind, namespace, domain string) error {
	switch targetKind {
	case "local_k8s":
		if s.k8sCtrl == nil || namespace == "" {
			return nil
		}
		return s.k8sCtrl.KubeDeleteApp(ctx, namespace, appName)
	case "local_docker":
		if domain != "" && s.deploy != nil {
			if err := s.deploy.DeprovisionTLS(ctx, domain); err != nil {
				slog.Warn("teardown: nginx vhost cleanup failed", "domain", domain, "error", err)
			}
		}
		if s.dockerMgr == nil {
			return nil
		}
		return s.dockerMgr.RemoveAppContainer(ctx, stackName, appName)
	case "cloud_aci", "cloud_ecs":
		if domain != "" && s.deploy != nil {
			if err := s.deploy.DeprovisionTLS(ctx, domain); err != nil {
				slog.Warn("teardown: nginx vhost cleanup failed", "domain", domain, "error", err)
			}
		}
	}
	return nil
}

// handleDestroyDeployment tears down every live app in the stack (K8s
// Deployment/Service/Ingress per app, then the shared namespace; Docker
// containers per app, then the shared network) and removes the deployment
// record entirely (deployment_apps/deployment_app_links/compliance_findings
// cascade via FK ON DELETE CASCADE) — so the stack disappears from the
// deployments list instead of lingering as an empty "idle" entry. Teardown is
// best-effort per app — failures are logged but don't block the rest of the
// stack from being torn down and removed.
func (s *Server) handleDestroyDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var stackName string
	if err := s.db.QueryRow(r.Context(), `SELECT name FROM deployments WHERE id = $1`, id).Scan(&stackName); err != nil {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT name, target_kind, COALESCE(namespace, ''), COALESCE(domain, '')
		FROM deployment_apps WHERE deployment_id = $1 AND status != 'destroyed'
	`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load apps")
		return
	}
	namespaces := map[string]bool{}
	hasDocker := false
	for rows.Next() {
		var appName, targetKind, namespace, domain string
		if err := rows.Scan(&appName, &targetKind, &namespace, &domain); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "failed to read app row")
			return
		}
		if err := s.teardownApp(r.Context(), stackName, appName, targetKind, namespace, domain); err != nil {
			slog.Error("deployments: teardown app failed", "stack", stackName, "app", appName, "error", err)
		}
		if targetKind == "local_k8s" && namespace != "" {
			namespaces[namespace] = true
		}
		if targetKind == "local_docker" {
			hasDocker = true
		}
	}
	rows.Close()

	if s.k8sCtrl != nil {
		for ns := range namespaces {
			if err := s.k8sCtrl.KubeDestroyResource(r.Context(), ns); err != nil {
				slog.Error("deployments: destroy namespace failed", "namespace", ns, "error", err)
			}
		}
	}
	if hasDocker && s.dockerMgr != nil {
		s.dockerMgr.RemoveStackNetwork(r.Context(), stackName)
	}

	if _, err := s.db.Exec(r.Context(), `DELETE FROM deployments WHERE id = $1`, id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete deployment")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteApp tears down a single app's live K8s/Docker resources and
// soft-deletes it (status=destroyed), leaving its siblings untouched — the
// multi-app counterpart of handleDestroyDeployment, which tears down every
// app in the stack at once. If teardown fails, the row is left in its current
// status so the dashboard keeps showing it (and the user can retry) instead
// of silently hiding an app whose resources are still running.
func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var stackName, appName, targetKind, namespace, domain string
	err := s.db.QueryRow(r.Context(), `
		SELECT d.name, da.name, da.target_kind, COALESCE(da.namespace, ''), COALESCE(da.domain, '')
		FROM deployment_apps da
		JOIN deployments d ON d.id = da.deployment_id
		WHERE da.id = $1 AND da.deployment_id = $2
	`, appID, deploymentID).Scan(&stackName, &appName, &targetKind, &namespace, &domain)
	if err != nil {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}

	if err := s.teardownApp(r.Context(), stackName, appName, targetKind, namespace, domain); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to remove running resources: %v", err))
		return
	}

	tag, err := s.db.Exec(r.Context(),
		`UPDATE deployment_apps SET status = 'destroyed', updated_at = NOW() WHERE id = $1 AND deployment_id = $2`,
		appID, deploymentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete app")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTriggerApp triggers a rebuild/redeploy of a single app in a stack.
func (s *Server) handleTriggerApp(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var kind string
	if err := s.db.QueryRow(r.Context(),
		`SELECT kind FROM deployment_apps WHERE id = $1 AND deployment_id = $2`, appID, deploymentID,
	).Scan(&kind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "app not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to look up app")
		return
	}
	go func() {
		var err error
		if kind == "image" {
			err = s.deploy.DeployImageApp(context.Background(), appID)
		} else {
			err = s.deploy.ProcessPush(context.Background(), appID)
		}
		if err != nil {
			slog.Error("deploy: manual trigger", "app_id", appID, "error", err)
			s.hub.Publish(sse.Event{Type: "deploy.log", Payload: deploy.LogEvent{AppID: appID, Line: "❌ " + err.Error()}})
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "deployment_id": deploymentID, "app_id": appID})
}

// handleGetAppWorkflow generates and returns a GitHub Actions workflow YAML
// for a single app in a stack. The file is ready to commit verbatim to
// .github/workflows/autodevops.yml — credentials are never embedded, only
// referenced as repository secrets (AUTODEVOPS_WEBHOOK_SECRET, AUTODEVOPS_URL).
// handleGetAppEnv decrypts and returns the image env vars for an image-kind app.
// GET /deployments/{id}/apps/{appId}/env
func (s *Server) handleGetAppEnv(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var secretID *string
	if err := s.db.QueryRow(r.Context(),
		`SELECT image_env_secret_id::text FROM deployment_apps
		 WHERE id = $1 AND deployment_id = $2 AND kind = 'image' AND status != 'destroyed'`,
		appID, deploymentID,
	).Scan(&secretID); err != nil {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	if secretID == nil {
		writeJSON(w, http.StatusOK, map[string]map[string]string{"env": {}})
		return
	}
	raw, err := s.vault.GetByID(r.Context(), *secretID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load env")
		return
	}
	var env map[string]string
	if err := json.Unmarshal(raw, &env); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse env")
		return
	}
	writeJSON(w, http.StatusOK, map[string]map[string]string{"env": env})
}

// handleGetK8sManifests returns the K8s manifest YAML for an app. When the
// app is deployed (status=running), it fetches the live objects from the
// cluster. Otherwise it generates the template from the app's config.
// Query param ?mode=template forces template mode even when running.
func (s *Server) handleGetK8sManifests(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var name, kind, targetKind, namespace, imageRef, domain, volumePath, status string
	var port int
	var k8sConfig json.RawMessage
	err := s.db.QueryRow(r.Context(), `
		SELECT da.name, da.kind, da.target_kind, COALESCE(da.namespace, ''),
		       COALESCE(da.image_ref, ''), COALESCE(da.domain, ''),
		       COALESCE(da.volume_path, ''), da.status, COALESCE(da.port, 0),
		       da.k8s_config
		FROM deployment_apps da
		WHERE da.id = $1 AND da.deployment_id = $2
	`, appID, deploymentID).Scan(&name, &kind, &targetKind, &namespace, &imageRef, &domain, &volumePath, &status, &port, &k8sConfig)
	if err != nil {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	if targetKind != "local_k8s" {
		writeError(w, http.StatusBadRequest, "manifests are only available for Kubernetes deployments")
		return
	}
	if s.k8sCtrl == nil {
		writeError(w, http.StatusServiceUnavailable, "Kubernetes controller not available")
		return
	}

	mode := r.URL.Query().Get("mode")

	// Try live manifests first (unless template mode forced).
	if mode != "template" && (status == "running" || status == "failed") {
		yaml, err := s.k8sCtrl.GetLiveManifests(r.Context(), name, namespace)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"yaml": yaml,
				"mode": "live",
			})
			return
		}
	}

	// Fall back to generated template.
	yaml, err := k8s.GenerateManifests(k8s.ManifestParams{
		Name:          name,
		Namespace:     namespace,
		ImageRef:      imageRef,
		Domain:        domain,
		Kind:          kind,
		Port:          int32(port),
		VolumePath:    volumePath,
		K8sConfigJSON: k8sConfig,
		IngressDomain: s.ingressDomain,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to generate manifests: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"yaml": yaml,
		"mode": "template",
	})
}

// handleApplyK8sManifest applies user-edited YAML directly to the cluster.
// This is a one-shot operation — the next redeploy will regenerate manifests
// from the app's stored config, overwriting these changes.
func (s *Server) handleApplyK8sManifest(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var targetKind string
	err := s.db.QueryRow(r.Context(),
		`SELECT target_kind FROM deployment_apps WHERE id = $1 AND deployment_id = $2`,
		appID, deploymentID,
	).Scan(&targetKind)
	if err != nil {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}
	if targetKind != "local_k8s" {
		writeError(w, http.StatusBadRequest, "apply is only available for Kubernetes deployments")
		return
	}
	if s.k8sCtrl == nil {
		writeError(w, http.StatusServiceUnavailable, "Kubernetes controller not available")
		return
	}

	var req struct {
		YAML string `json:"yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.YAML) == "" {
		writeError(w, http.StatusBadRequest, "yaml is required")
		return
	}

	applied, err := s.k8sCtrl.ApplyManifestYAML(r.Context(), req.YAML)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("apply failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"applied": applied,
		"message": fmt.Sprintf("Applied %d K8s object(s). Note: changes will be overwritten on next redeploy.", applied),
	})
}

func (s *Server) handleGetAppWorkflow(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")

	var params cicd.WorkflowParams
	err := s.db.QueryRow(r.Context(), `
		SELECT id::text, name, branch, COALESCE(framework, 'unknown')
		FROM deployment_apps WHERE id = $1 AND deployment_id = $2
	`, appID, deploymentID).Scan(&params.AppID, &params.Name, &params.Branch, &params.Framework)
	if err != nil {
		writeError(w, http.StatusNotFound, "app not found")
		return
	}

	yaml := cicd.Workflow(params)
	w.Header().Set("Content-Type", "text/x-yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="autodevops-`+params.Name+`.yml"`)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(yaml))
}
