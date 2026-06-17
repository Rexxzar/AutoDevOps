package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleListRepoFiles returns the editable file paths in the app's cloned workspace.
// GET /deployments/{id}/apps/{appId}/repo/files
func (s *Server) handleListRepoFiles(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")
	if err := s.validateGitApp(r.Context(), deploymentID, appID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	files, err := s.deploy.ListRepoFiles(appID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"files": files})
}

// handleGetRepoFile returns the content of a single file in the workspace.
// GET /deployments/{id}/apps/{appId}/repo/files/*path
func (s *Server) handleGetRepoFile(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")
	if err := s.validateGitApp(r.Context(), deploymentID, appID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	path := chi.URLParam(r, "*")
	content, err := s.deploy.ReadRepoFile(appID, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "content": string(content)})
}

// handlePutRepoFile writes content to a file in the workspace (does not commit).
// PUT /deployments/{id}/apps/{appId}/repo/files/*path
func (s *Server) handlePutRepoFile(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")
	if err := s.validateGitApp(r.Context(), deploymentID, appID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	path := chi.URLParam(r, "*")
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.deploy.WriteRepoFile(appID, path, []byte(body.Content)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// handleCommitRepo commits all workspace changes and pushes to the remote.
// POST /deployments/{id}/apps/{appId}/repo/commit
func (s *Server) handleCommitRepo(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")
	appID := chi.URLParam(r, "appId")
	if err := s.validateGitApp(r.Context(), deploymentID, appID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message == "" {
		body.Message = "edit via AutoDevOps dashboard"
	}
	if err := s.deploy.CommitAndPush(r.Context(), appID, body.Message); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pushed"})
}

// validateGitApp verifies appId belongs to deploymentId and is a live git app.
func (s *Server) validateGitApp(ctx context.Context, deploymentID, appID string) error {
	var count int
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM deployment_apps
		 WHERE id = $1 AND deployment_id = $2 AND kind = 'git' AND status != 'destroyed'`,
		appID, deploymentID,
	).Scan(&count); err != nil || count == 0 {
		return fmt.Errorf("app not found")
	}
	return nil
}
