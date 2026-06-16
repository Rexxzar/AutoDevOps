package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Rexxzar/autodevops/internal/console"
)

// consoleExecTimeout bounds POST /resources/{id}/console/exec — one-shot
// commands must complete within this window (separate from interactive
// sessions, which run until idle/max-duration timeout in console.Manager).
// 2 minutes because the Azure VM connector's key push (BeginRunCommand)
// routinely takes 30-90s on its own, before SSH even dials.
const consoleExecTimeout = 2 * time.Minute

// consoleResource loads a provisioned_resources row and converts it to
// console.Resource, the shape connectors need to open a session or run a
// command. Returns an error (for a 400 response) if the resource isn't live
// yet — its terraform outputs (instance IDs, IPs, etc.) only exist after a
// successful Apply.
// consoleIDParam returns the {id} chi URL param, URL-decoded — same reason as
// inventoryID in inventory.go: chi routes on the raw encoded path.
func consoleIDParam(r *http.Request) string {
	raw := chi.URLParam(r, "id")
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

func (s *Server) consoleResource(ctx context.Context, id string) (console.Resource, error) {
	// Synthetic IDs for host-inventory items — no DB lookup needed.
	if strings.HasPrefix(id, "docker:") {
		cid := strings.TrimPrefix(id, "docker:")
		return console.Resource{
			Name:         cid,
			Provider:     "local",
			ResourceType: "docker",
			Outputs:      map[string]any{"container_id": cid},
		}, nil
	}
	if strings.HasPrefix(id, "k8s:") {
		_, ns, pod, parseErr := parseInventoryID(id)
		if parseErr != nil {
			return console.Resource{}, parseErr
		}
		return console.Resource{
			Name:         pod,
			Provider:     "local",
			ResourceType: "k8s",
			Outputs:      map[string]any{"namespace": ns, "pod_name": pod},
		}, nil
	}

	rr, err := s.getResource(ctx, id)
	if err != nil {
		return console.Resource{}, fmt.Errorf("resource not found")
	}
	if rr.Status != "applied" {
		return console.Resource{}, fmt.Errorf("resource is not applied (status: %s) — apply it before opening a console", rr.Status)
	}
	if rr.CloudAccountID == "" && rr.Provider != "local" {
		return console.Resource{}, fmt.Errorf("resource has no associated cloud account")
	}

	var outputs map[string]any
	if err := json.Unmarshal([]byte(rr.Outputs), &outputs); err != nil {
		return console.Resource{}, fmt.Errorf("decode resource outputs: %w", err)
	}

	return console.Resource{
		Name:           rr.Name,
		Provider:       rr.Provider,
		ResourceType:   rr.ResourceType,
		CloudAccountID: rr.CloudAccountID,
		Region:         rr.Region,
		Outputs:        outputs,
	}, nil
}

type consoleOpenRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

type consoleOpenResponse struct {
	SessionID string `json:"session_id"`
}

// handleOpenConsoleSession opens an interactive shell session against the
// resource and returns its ID. The frontend then opens
// GET .../sessions/{sessionId}/stream (SSE) for output and POSTs to
// .../input and .../resize.
func (s *Server) handleOpenConsoleSession(w http.ResponseWriter, r *http.Request) {
	id := consoleIDParam(r)
	res, err := s.consoleResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req consoleOpenRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	rows, cols := req.Rows, req.Cols
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}

	sessionID, err := s.console.Open(r.Context(), res, uint16(rows), uint16(cols))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, consoleOpenResponse{SessionID: sessionID})
}

// handleConsoleStream streams a session's PTY output to the browser over SSE
// — each "output" event carries a base64 chunk (terminal output is arbitrary
// bytes: ANSI escapes, partial UTF-8, etc., which wouldn't survive raw in an
// SSE data line). A "closed" event is sent when the remote session ends.
// Disconnecting the SSE stream closes the underlying session.
func (s *Server) handleConsoleStream(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	out, done, ok := s.console.Stream(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "console session not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			s.console.Close(sessionID)
			return
		case <-done:
			fmt.Fprint(w, "event: closed\ndata: {}\n\n")
			flusher.Flush()
			return
		case chunk := <-out:
			fmt.Fprintf(w, "event: output\ndata: %s\n\n", base64.StdEncoding.EncodeToString(chunk))
			flusher.Flush()
		}
	}
}

type consoleInputRequest struct {
	Data string `json:"data"`
}

// handleConsoleInput writes keystrokes (from xterm.js's onData) to a
// session's PTY stdin.
func (s *Server) handleConsoleInput(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	var req consoleInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.console.Write(sessionID, []byte(req.Data)); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type consoleResizeRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// handleConsoleResize updates a session's PTY size, e.g. on browser window
// resize.
func (s *Server) handleConsoleResize(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	var req consoleResizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Rows <= 0 || req.Cols <= 0 {
		writeError(w, http.StatusBadRequest, "rows and cols must be positive")
		return
	}
	if err := s.console.Resize(sessionID, uint16(req.Rows), uint16(req.Cols)); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleCloseConsoleSession ends a session early, e.g. when the user closes
// the terminal tab. Idempotent.
func (s *Server) handleCloseConsoleSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	_ = s.console.Close(sessionID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type consoleExecRequest struct {
	Command string `json:"command"`
}

// handleConsoleExec runs a one-shot command against the resource and returns
// its stdout/stderr/exit code — no session bookkeeping involved.
func (s *Server) handleConsoleExec(w http.ResponseWriter, r *http.Request) {
	id := consoleIDParam(r)
	res, err := s.consoleResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req consoleExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Command == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), consoleExecTimeout)
	defer cancel()

	result, err := s.console.Exec(ctx, res, req.Command)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
