package api

import (
	"net/http"
	"strconv"
	"time"
)

// parseMetricLimit reads a bounded ?limit from the request, defaulting to def.
func parseMetricLimit(r *http.Request, def, max int) int {
	limit := def
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= max {
			limit = n
		}
	}
	return limit
}

// deployRun is one git-to-deploy pipeline execution, for the Chapter 5
// end-to-end deploy-latency evaluation. duration = finished_at - started_at.
type deployRun struct {
	ID              string     `json:"id"`
	AppName         string     `json:"app_name"`
	TargetKind      string     `json:"target_kind"`
	Status          string     `json:"status"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	DurationSeconds *float64   `json:"duration_seconds,omitempty"`
}

func (s *Server) handleListDeployRuns(w http.ResponseWriter, r *http.Request) {
	limit := parseMetricLimit(r, 200, 1000)
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, app_name, target_kind, status, started_at, finished_at
		FROM deploy_runs
		ORDER BY started_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deploy runs")
		return
	}
	defer rows.Close()

	out := []deployRun{}
	for rows.Next() {
		var d deployRun
		if err := rows.Scan(&d.ID, &d.AppName, &d.TargetKind, &d.Status, &d.StartedAt, &d.FinishedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read deploy runs")
			return
		}
		if d.FinishedAt != nil {
			secs := d.FinishedAt.Sub(d.StartedAt).Seconds()
			d.DurationSeconds = &secs
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read deploy runs")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// provisionRun is one terraform apply/destroy, for the Chapter 5 provisioning-
// latency evaluation. duration = updated_at - created_at.
type provisionRun struct {
	ID              string    `json:"id"`
	Template        string    `json:"template"`
	Action          string    `json:"action"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	DurationSeconds *float64  `json:"duration_seconds,omitempty"`
}

func (s *Server) handleListProvisionRuns(w http.ResponseWriter, r *http.Request) {
	limit := parseMetricLimit(r, 200, 1000)
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, template, action, status, created_at, updated_at
		FROM terraform_runs
		WHERE action IN ('apply', 'destroy')
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list provision runs")
		return
	}
	defer rows.Close()

	out := []provisionRun{}
	for rows.Next() {
		var p provisionRun
		var updatedAt time.Time
		if err := rows.Scan(&p.ID, &p.Template, &p.Action, &p.Status, &p.CreatedAt, &updatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read provision runs")
			return
		}
		if updatedAt.After(p.CreatedAt) {
			secs := updatedAt.Sub(p.CreatedAt).Seconds()
			p.DurationSeconds = &secs
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read provision runs")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
