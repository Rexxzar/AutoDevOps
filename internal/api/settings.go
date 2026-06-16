package api

import (
	"encoding/json"
	"net/http"
)

// handleGetSettings returns all rows from the settings table as a flat
// key→value map.
//
// GET /settings
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT key, value FROM settings`)
	if err != nil {
		http.Error(w, "load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			http.Error(w, "scan settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
		out[key] = value
	}
	writeJSON(w, http.StatusOK, out)
}

type putSettingRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// handlePutSetting upserts a single setting by key.
//
// PUT /settings
func (s *Server) handlePutSetting(w http.ResponseWriter, r *http.Request) {
	var req putSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}

	_, err := s.db.Exec(r.Context(),
		`INSERT INTO settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		req.Key, req.Value,
	)
	if err != nil {
		http.Error(w, "update setting: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{req.Key: req.Value})
}
