package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

type logEntry struct {
	ID        int64           `json:"id"`
	Level     string          `json:"level"`
	Source    string          `json:"source"`
	Message   string          `json:"message"`
	Attrs     json.RawMessage `json:"attrs"`
	CreatedAt time.Time       `json:"created_at"`
}

// handleListLogs returns recent system logs (newest first) with optional filters:
//
//	?level=ERROR        exact level (DEBUG|INFO|WARN|ERROR)
//	?source=docker      exact component
//	?q=restart          case-insensitive substring of the message
//	?since=15m          only logs newer than this duration ago
//	?limit=200          cap (default 200, max 1000)
func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	level := q.Get("level")
	source := q.Get("source")
	search := q.Get("q")

	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	var since time.Time
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			since = time.Now().Add(-d)
		}
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT id, level, source, message, attrs, created_at
		FROM logs
		WHERE ($1 = '' OR level = $1)
		  AND ($2 = '' OR source = $2)
		  AND ($3 = '' OR message ILIKE '%' || $3 || '%')
		  AND ($4::timestamptz IS NULL OR created_at >= $4)
		ORDER BY created_at DESC, id DESC
		LIMIT $5
	`, level, source, search, nullableTime(since), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list logs")
		return
	}
	defer rows.Close()

	out := []logEntry{}
	for rows.Next() {
		var e logEntry
		if err := rows.Scan(&e.ID, &e.Level, &e.Source, &e.Message, &e.Attrs, &e.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read logs")
			return
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read logs")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLogSources returns the distinct sources present, for the filter dropdown.
func (s *Server) handleLogSources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(),
		"SELECT DISTINCT source FROM logs ORDER BY source")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list log sources")
		return
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read log sources")
			return
		}
		out = append(out, src)
	}
	writeJSON(w, http.StatusOK, out)
}

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
