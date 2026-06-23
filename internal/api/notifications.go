package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Rexxzar/autodevops/internal/notify"
)

// notifyDeployStarted emits an "initiated" notification for a deploy run that
// began automatically (a git push via the CI/CD webhook), best-effort. Manual
// redeploys skip this — the user is already watching the pipeline.
func (s *Server) notifyDeployStarted(ctx context.Context, appID string) {
	var name, deploymentID string
	if err := s.db.QueryRow(ctx,
		"SELECT name, deployment_id::text FROM deployment_apps WHERE id = $1", appID,
	).Scan(&name, &deploymentID); err != nil {
		return
	}
	notify.Publish(ctx, s.db, s.hub, notify.Notification{
		Type: "deploy", Severity: notify.Info,
		Title: "CI/CD deploy started: " + name,
		Body:  "Triggered by a push to the repository.",
		Link:  "/deployments/" + deploymentID,
	})
}

// notifyDeployResult emits a bell notification for a finished deploy run
// (best-effort). deployErr nil = success. Called from the webhook + manual
// trigger paths after ProcessPush/DeployImageApp returns.
func (s *Server) notifyDeployResult(ctx context.Context, appID string, deployErr error) {
	var name, deploymentID string
	if err := s.db.QueryRow(ctx,
		"SELECT name, deployment_id::text FROM deployment_apps WHERE id = $1", appID,
	).Scan(&name, &deploymentID); err != nil {
		return
	}
	link := "/deployments/" + deploymentID
	if deployErr != nil {
		notify.Publish(ctx, s.db, s.hub, notify.Notification{
			Type: "deploy", Severity: notify.Critical,
			Title: "Deploy failed: " + name,
			Body:  deployErr.Error(),
			Link:  link,
		})
		return
	}
	notify.Publish(ctx, s.db, s.hub, notify.Notification{
		Type: "deploy", Severity: notify.Success,
		Title: "Deployed " + name,
		Link:  link,
	})
}

// handleListNotifications returns the most recent notifications (newest first).
// GET /notifications?limit=50
func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, type, severity, title, COALESCE(body, ''), COALESCE(link, ''), read, created_at
		FROM notifications
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list notifications")
		return
	}
	defer rows.Close()

	out := []notify.Notification{}
	for rows.Next() {
		var n notify.Notification
		if err := rows.Scan(&n.ID, &n.Type, &n.Severity, &n.Title, &n.Body, &n.Link, &n.Read, &n.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read notifications")
			return
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read notifications")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUnreadCount returns the number of unread notifications.
// GET /notifications/unread-count
func (s *Server) handleUnreadCount(w http.ResponseWriter, r *http.Request) {
	var count int
	if err := s.db.QueryRow(r.Context(),
		"SELECT count(*) FROM notifications WHERE NOT read",
	).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count notifications")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": count})
}

// handleMarkNotificationsRead marks notifications read — either a specific set
// of ids, or all unread when {"all": true}.
// POST /notifications/read
func (s *Server) handleMarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.All {
		if _, err := s.db.Exec(r.Context(), "UPDATE notifications SET read = true WHERE NOT read"); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to mark notifications read")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "provide ids or all:true")
		return
	}
	if _, err := s.db.Exec(r.Context(),
		"UPDATE notifications SET read = true WHERE id = ANY($1)", req.IDs,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark notifications read")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
