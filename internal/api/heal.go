package api

import (
	"net/http"
	"strconv"
	"time"
)

// healEpisode is one self-healing cycle for the dashboard / T21 evaluation.
type healEpisode struct {
	ID          string     `json:"id"`
	TargetKind  string     `json:"target_kind"` // pod | container
	TargetName  string     `json:"target_name"`
	Scope       string     `json:"scope,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	FailureAt   *time.Time `json:"failure_at,omitempty"`
	DetectedAt  time.Time  `json:"detected_at"`
	RecoveredAt *time.Time `json:"recovered_at,omitempty"`
	GaveUp      bool       `json:"gave_up"`
	MTTDSeconds *float64   `json:"mttd_seconds,omitempty"` // detection latency
	MTTRSeconds *float64   `json:"mttr_seconds,omitempty"` // recovery time
}

func (s *Server) handleListHealEpisodes(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, target_kind, target_name, COALESCE(scope, ''), COALESCE(reason, ''),
		       failure_at, detected_at, recovered_at, gave_up
		FROM heal_episodes
		ORDER BY detected_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list heal episodes")
		return
	}
	defer rows.Close()

	out := []healEpisode{}
	for rows.Next() {
		var e healEpisode
		if err := rows.Scan(&e.ID, &e.TargetKind, &e.TargetName, &e.Scope, &e.Reason,
			&e.FailureAt, &e.DetectedAt, &e.RecoveredAt, &e.GaveUp); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read heal episodes")
			return
		}
		if e.FailureAt != nil && !e.DetectedAt.Before(*e.FailureAt) {
			d := e.DetectedAt.Sub(*e.FailureAt).Seconds()
			e.MTTDSeconds = &d
		}
		if e.RecoveredAt != nil {
			d := e.RecoveredAt.Sub(e.DetectedAt).Seconds()
			e.MTTRSeconds = &d
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read heal episodes")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type healKindMetrics struct {
	Total          int      `json:"total"`
	Recovered      int      `json:"recovered"`
	Unrecovered    int      `json:"unrecovered"`
	RecoveryRate   float64  `json:"recovery_rate"`             // recovered / total (0..1)
	AvgMTTDSeconds *float64 `json:"avg_mttd_seconds,omitempty"`
	AvgMTTRSeconds *float64 `json:"avg_mttr_seconds,omitempty"`
}

// handleHealMetrics returns aggregate self-healing metrics (overall + per kind):
// recovery rate, mean time to detect (MTTD), mean time to recover (MTTR).
func (s *Server) handleHealMetrics(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT target_kind,
		       count(*),
		       count(*) FILTER (WHERE recovered_at IS NOT NULL),
		       avg(EXTRACT(EPOCH FROM (recovered_at - detected_at))) FILTER (WHERE recovered_at IS NOT NULL),
		       avg(EXTRACT(EPOCH FROM (detected_at - failure_at))) FILTER (WHERE failure_at IS NOT NULL AND detected_at >= failure_at)
		FROM heal_episodes
		GROUP BY target_kind
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute heal metrics")
		return
	}
	defer rows.Close()

	byKind := map[string]healKindMetrics{}
	overall := healKindMetrics{}
	var ovMTTDNum, ovMTTRNum float64
	var ovMTTDDen, ovMTTRDen int

	for rows.Next() {
		var kind string
		var total, recovered int
		var avgMTTR, avgMTTD *float64
		if err := rows.Scan(&kind, &total, &recovered, &avgMTTR, &avgMTTD); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read heal metrics")
			return
		}
		km := healKindMetrics{Total: total, Recovered: recovered, Unrecovered: total - recovered}
		if total > 0 {
			km.RecoveryRate = float64(recovered) / float64(total)
		}
		km.AvgMTTRSeconds = avgMTTR
		km.AvgMTTDSeconds = avgMTTD
		byKind[kind] = km

		overall.Total += total
		overall.Recovered += recovered
		// Weight the per-kind averages by their sample sizes for a correct overall mean.
		if avgMTTR != nil {
			ovMTTRNum += *avgMTTR * float64(recovered)
			ovMTTRDen += recovered
		}
		if avgMTTD != nil {
			ovMTTDNum += *avgMTTD * float64(total)
			ovMTTDDen += total
		}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read heal metrics")
		return
	}

	overall.Unrecovered = overall.Total - overall.Recovered
	if overall.Total > 0 {
		overall.RecoveryRate = float64(overall.Recovered) / float64(overall.Total)
	}
	if ovMTTRDen > 0 {
		v := ovMTTRNum / float64(ovMTTRDen)
		overall.AvgMTTRSeconds = &v
	}
	if ovMTTDDen > 0 {
		v := ovMTTDNum / float64(ovMTTDDen)
		overall.AvgMTTDSeconds = &v
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"overall": overall,
		"by_kind": byKind,
	})
}
