package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// autofillLocalGeo runs once per process (guarded by Server.geoipOnce) when
// the Topology Map has no stored Local coordinates. It geolocates the server's
// public IP (city-level) via a keyless HTTPS endpoint and persists the result
// into settings, so the Local pin lands where the hardware actually is (e.g. a
// UK laptop or a German VPS). Best-effort: on any failure it simply leaves the
// neutral default in place, and the operator can always set an exact location
// in Settings, which takes precedence and suppresses this lookup entirely.
func (s *Server) autofillLocalGeo() {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	// ipwho.is echoes the caller's own IP location when no IP is given, which
	// is exactly the server's outbound location. If we know the public IP,
	// query it explicitly.
	url := "https://ipwho.is/"
	if s.publicIP != "" {
		url += s.publicIP
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("topology: geoip request build failed", "error", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("topology: geoip lookup failed; Local pin stays at default", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("topology: geoip lookup non-200", "status", resp.StatusCode)
		return
	}

	var body struct {
		Success   bool    `json:"success"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		City      string  `json:"city"`
		Country   string  `json:"country"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		slog.Warn("topology: geoip decode failed", "error", err)
		return
	}
	if !body.Success || (body.Latitude == 0 && body.Longitude == 0) {
		slog.Warn("topology: geoip returned no usable location")
		return
	}

	label := "Local"
	switch {
	case body.City != "" && body.Country != "":
		label = "Local (" + body.City + ", " + body.Country + ")"
	case body.City != "":
		label = "Local (" + body.City + ")"
	}

	if err := s.putSetting(ctx, "topology_local_lat", strconv.FormatFloat(body.Latitude, 'f', 4, 64)); err != nil {
		slog.Warn("topology: persist geoip lat failed", "error", err)
		return
	}
	if err := s.putSetting(ctx, "topology_local_lon", strconv.FormatFloat(body.Longitude, 'f', 4, 64)); err != nil {
		slog.Warn("topology: persist geoip lon failed", "error", err)
		return
	}
	// Only set the label if the operator hasn't chosen one.
	_ = s.db.QueryRow(ctx,
		`INSERT INTO settings (key, value) VALUES ('topology_local_label', $1)
		 ON CONFLICT (key) DO NOTHING RETURNING value`, label).Scan(new(string))

	slog.Info("topology: Local pin auto-located", "label", label, "lat", body.Latitude, "lon", body.Longitude)
}

// putSetting upserts a single settings row (same shape as handlePutSetting).
func (s *Server) putSetting(ctx context.Context, key, value string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}
