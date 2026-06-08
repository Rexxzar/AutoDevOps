package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/Rexxzar/autodevops/internal/auth"
	"github.com/Rexxzar/autodevops/internal/deploy"
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
	writeJSON(w, http.StatusOK, []any{})
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
	writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) handleListCloudAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
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
