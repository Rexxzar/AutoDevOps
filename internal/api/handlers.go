package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Rexxzar/autodevops/internal/auth"
	"github.com/Rexxzar/autodevops/internal/cicd"
	"github.com/Rexxzar/autodevops/internal/cloud"
	"github.com/Rexxzar/autodevops/internal/deploy"
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

	serverIP := s.serverIP(r)
	ok := false
	for _, a := range addrs {
		if a == serverIP {
			ok = true
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"domain":       domain,
		"resolved_ips": addrs,
		"server_ip":    serverIP,
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

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, name, repo_url, branch,
		       COALESCE(framework,''), target_type, status,
		       COALESCE(image_ref,''), created_at, updated_at
		FROM deployments ORDER BY created_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deployments")
		return
	}
	defer rows.Close()

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
	}
	var results []row
	for rows.Next() {
		var d row
		if err := rows.Scan(&d.ID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
			&d.TargetType, &d.Status, &d.ImageRef, &d.CreatedAt, &d.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan deployment")
			return
		}
		results = append(results, d)
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
		Name       string `json:"name"`
		RepoURL    string `json:"repo_url"`
		Branch     string `json:"branch"`
		TargetType string `json:"target_type"`
		GitAuth    *struct {
			Type   string `json:"type"`
			Token  string `json:"token,omitempty"`
			SSHKey string `json:"ssh_key,omitempty"`
		} `json:"git_auth,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.RepoURL == "" {
		writeError(w, http.StatusBadRequest, "name and repo_url required")
		return
	}
	if req.TargetType != "k8s" && req.TargetType != "container" && req.TargetType != "vm" {
		writeError(w, http.StatusBadRequest, `target_type must be "k8s", "container", or "vm"`)
		return
	}
	if req.GitAuth != nil && req.GitAuth.Type != deploy.GitAuthToken && req.GitAuth.Type != deploy.GitAuthSSHKey {
		writeError(w, http.StatusBadRequest, `git_auth.type must be "token" or "ssh_key"`)
		return
	}

	branch := req.Branch
	if branch == "" {
		branch = "main"
	}

	ctx := r.Context()

	webhookSecretRaw := make([]byte, 32)
	if _, err := rand.Read(webhookSecretRaw); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate webhook secret")
		return
	}
	webhookSecret := hex.EncodeToString(webhookSecretRaw)

	webhookSecretName := "webhook:" + req.Name
	if err := s.vault.Put(ctx, webhookSecretName, []byte(webhookSecret)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store webhook secret")
		return
	}

	var gitCredsSecretName *string
	if req.GitAuth != nil {
		raw, err := json.Marshal(map[string]string{
			"type":    req.GitAuth.Type,
			"token":   req.GitAuth.Token,
			"ssh_key": req.GitAuth.SSHKey,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode git credentials")
			return
		}
		name := "git-credentials:" + req.Name
		if err := s.vault.Put(ctx, name, raw); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to store git credentials")
			return
		}
		gitCredsSecretName = &name
	}

	var deploymentID string
	err := s.db.QueryRow(ctx, `
		INSERT INTO deployments (name, repo_url, branch, target_type, webhook_secret_id, git_credentials_secret_id)
		VALUES (
			$1, $2, $3, $4,
			(SELECT id FROM secrets WHERE name = $5),
			(SELECT id FROM secrets WHERE name = $6)
		)
		RETURNING id::text
	`, req.Name, req.RepoURL, branch, req.TargetType, webhookSecretName, gitCredsSecretName).Scan(&deploymentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create deployment")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":             deploymentID,
		"webhook_url":    "/webhooks/github/" + deploymentID,
		"webhook_secret": webhookSecret,
	})
}

// handleGitHubWebhook receives GitHub push deliveries for a deployment.
// Outside the JWT-authenticated group — GitHub can't present a bearer token,
// so the per-deployment HMAC secret (X-Hub-Signature-256) is the auth
// boundary instead, exactly GitHub's documented webhook security model.
// Cloning happens asynchronously: GitHub times out webhook responses after
// ~10s, so this handler verifies, decides, and returns 202 immediately,
// leaving deploy.Orchestrator.ProcessPush to run in the background.
func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var branch, webhookSecretID string
	err = s.db.QueryRow(r.Context(), `
		SELECT branch, webhook_secret_id::text FROM deployments WHERE id = $1
	`, deploymentID).Scan(&branch, &webhookSecretID)
	if err != nil {
		writeError(w, http.StatusNotFound, "deployment not found")
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
			if err := s.deploy.ProcessPush(context.Background(), deploymentID); err != nil {
				slog.Error("deploy: process push", "deployment_id", deploymentID, "error", err)
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

	secretName := "cloud-account:" + req.Name
	if err := s.vault.Put(r.Context(), secretName, req.Credentials); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store credentials")
		return
	}

	var accountID string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO cloud_accounts (provider, name, credentials_secret_id)
		SELECT $1, $2, id FROM secrets WHERE name = $3
		RETURNING id::text
	`, req.Provider, req.Name, secretName).Scan(&accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create cloud account")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": accountID, "provider": req.Provider, "name": req.Name})
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
	name := chi.URLParam(r, "name")
	value, err := s.vault.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "value": string(value)})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.vault.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete secret")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListComplianceFindings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT cf.id::text, cf.deployment_id::text, d.name,
		       cf.scanner, cf.severity, cf.rule_id, cf.message,
		       COALESCE(cf.file_path, ''), COALESCE(cf.line_number, 0),
		       cf.created_at
		FROM compliance_findings cf
		JOIN deployments d ON d.id = cf.deployment_id
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
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Provider       string    `json:"provider"`
	CloudAccountID string    `json:"cloud_account_id,omitempty"`
	Region         string    `json:"region"`
	ResourceType   string    `json:"resource_type"`
	Template       string    `json:"template"`
	Status         string    `json:"status"`
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
		       region, resource_type, template, status, COALESCE(outputs::text, '{}'), created_at, updated_at
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
			&rr.Region, &rr.ResourceType, &rr.Template, &rr.Status, &rr.Outputs, &rr.CreatedAt, &rr.UpdatedAt); err != nil {
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
		       region, resource_type, template, status, COALESCE(outputs::text, '{}'), created_at, updated_at
		FROM provisioned_resources WHERE id = $1
	`, id).Scan(&rr.ID, &rr.Name, &rr.Provider, &rr.CloudAccountID,
		&rr.Region, &rr.ResourceType, &rr.Template, &rr.Status, &rr.Outputs, &rr.CreatedAt, &rr.UpdatedAt)
	return rr, err
}

// handleGetResourceTerraform returns the exact Terraform HCL last planned
// for this resource — read from its workspace's main.tf, whether generated
// by buildTerraformConfig or hand-edited via handlePlanResourceTerraform.
// Backs the resource detail page's "View Terraform" panel and download
// action.
func (s *Server) handleGetResourceTerraform(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.getResource(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
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
		Name           string            `json:"name"`
		Provider       string            `json:"provider"`
		CloudAccountID string            `json:"cloud_account_id"`
		Region         string            `json:"region"`
		ResourceType   string            `json:"resource_type"`
		ResourceOpts   map[string]string `json:"resource_opts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Provider == "" || req.CloudAccountID == "" || req.Region == "" || req.ResourceType == "" {
		writeError(w, http.StatusBadRequest, "name, provider, cloud_account_id, region, resource_type required")
		return
	}
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform binary unavailable — check server logs")
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
		INSERT INTO provisioned_resources (name, provider, cloud_account_id, region, resource_type, template, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')
		RETURNING id::text
	`, req.Name, req.Provider, req.CloudAccountID, req.Region, req.ResourceType, template).Scan(&resourceID)
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
		s.setResourceStatus(ctx, id, status)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "plan_initiated", "id": id})
}

// handleDestroyResource runs `terraform destroy` against a resource's workspace.
func (s *Server) handleDestroyResource(w http.ResponseWriter, r *http.Request) {
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
			cpu, err := intOpt(opts, "cpu", 256)
			if err != nil {
				return nil, err
			}
			memory, err := intOpt(opts, "memory", 512)
			if err != nil {
				return nil, err
			}
			port, err := intOpt(opts, "port", 80)
			if err != nil {
				return nil, err
			}
			desiredCount, err := intOpt(opts, "desired_count", 1)
			if err != nil {
				return nil, err
			}
			params.ECS = &terraform.ECSParams{
				ContainerImage: strOpt(opts, "image", "nginx:latest"),
				ContainerPort:  port,
				CPU:            cpu,
				Memory:         memory,
				DesiredCount:   desiredCount,
				Subnets:        awsZonalSubnets(region),
			}
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

func (s *Server) handleDestroyDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tag, err := s.db.Exec(r.Context(),
		`UPDATE deployments SET status = 'destroyed', updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to destroy deployment")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTriggerDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var exists bool
	if err := s.db.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM deployments WHERE id = $1)`, id,
	).Scan(&exists); err != nil || !exists {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	go func() {
		if err := s.deploy.ProcessPush(context.Background(), id); err != nil {
			slog.Error("deploy: manual trigger", "deployment_id", id, "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "deployment_id": id})
}

// handleGetDeploymentWorkflow generates and returns a GitHub Actions workflow
// YAML for the given deployment. The file is ready to commit verbatim to
// .github/workflows/autodevops.yml — credentials are never embedded, only
// referenced as repository secrets (AUTODEVOPS_WEBHOOK_SECRET, AUTODEVOPS_URL).
func (s *Server) handleGetDeploymentWorkflow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var params cicd.WorkflowParams
	params.DeploymentID = id
	err := s.db.QueryRow(r.Context(), `
		SELECT name, branch, COALESCE(framework, 'unknown')
		FROM deployments WHERE id = $1
	`, id).Scan(&params.Name, &params.Branch, &params.Framework)
	if err != nil {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}

	yaml := cicd.Workflow(params)
	w.Header().Set("Content-Type", "text/x-yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="autodevops-`+params.Name+`.yml"`)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(yaml))
}
