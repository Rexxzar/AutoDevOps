package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// workspaceItem is one row in the Terraform Store catalog — a terraform
// workspace (config + state + run history) for a provisioned resource.
type workspaceItem struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Provider     string     `json:"provider"`
	ResourceType string     `json:"resource_type"`
	Kind         string     `json:"kind"` // "generated" | "imported"
	Status       string     `json:"status"`
	HasConfig    bool       `json:"has_config"`
	HasState     bool       `json:"has_state"`
	LastRun      string     `json:"last_run,omitempty"` // "apply:succeeded"
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	RunCount     int        `json:"run_count"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// workspaceKind classifies a resource as imported (user-uploaded HCL, template
// "imported") or generated (rendered from params by buildTerraformConfig).
func workspaceKind(template string) string {
	if template == "imported" {
		return "imported"
	}
	return "generated"
}

// handleListWorkspaces returns the Terraform Store catalog: every cloud
// resource's terraform workspace with status, state presence and run summary.
// Local resources are excluded — they have no terraform workspace.
func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `
		SELECT pr.id::text, pr.name, pr.provider, pr.resource_type, pr.template, pr.status,
		       pr.created_at, pr.updated_at,
		       (SELECT count(*) FROM terraform_runs tr WHERE tr.resource_id = pr.id),
		       (SELECT tr.action || ':' || tr.status FROM terraform_runs tr WHERE tr.resource_id = pr.id ORDER BY tr.created_at DESC LIMIT 1),
		       (SELECT tr.created_at FROM terraform_runs tr WHERE tr.resource_id = pr.id ORDER BY tr.created_at DESC LIMIT 1)
		FROM provisioned_resources pr
		WHERE pr.provider <> 'local' AND pr.dismissed_at IS NULL
		ORDER BY pr.created_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspaces")
		return
	}
	defer rows.Close()

	out := []workspaceItem{}
	for rows.Next() {
		var it workspaceItem
		var template string
		var lastRun *string
		if err := rows.Scan(&it.ID, &it.Name, &it.Provider, &it.ResourceType, &template, &it.Status,
			&it.CreatedAt, &it.UpdatedAt, &it.RunCount, &lastRun, &it.LastRunAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan workspace")
			return
		}
		it.Kind = workspaceKind(template)
		if lastRun != nil {
			it.LastRun = *lastRun
		}
		if s.tfRunner != nil {
			it.HasConfig, _ = s.tfRunner.HasConfig("", it.ID)
			it.HasState, _ = s.tfRunner.HasState("", it.ID)
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDownloadResourceTerraform serves a resource's main.tf as a file
// download (text/plain attachment).
func (s *Server) handleDownloadResourceTerraform(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform unavailable")
		return
	}
	config, err := s.tfRunner.ReadConfig("", id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no terraform configuration for this resource yet")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.tf"`, safeFilename(rr.Name)))
	_, _ = w.Write(config)
}

// handleExportResourceTerraform serves a portable zip of the workspace: the
// main.tf plus a README with run instructions. State is deliberately excluded
// (it can contain secrets) — this is the "no lock-in" artifact a user can take
// and `terraform init/plan/apply` themselves.
func (s *Server) handleExportResourceTerraform(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform unavailable")
		return
	}
	config, err := s.tfRunner.ReadConfig("", id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no terraform configuration for this resource yet")
		return
	}

	name := safeFilename(rr.Name)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-terraform.zip"`, name))

	zw := zip.NewWriter(w)
	defer zw.Close()
	if f, err := zw.Create("main.tf"); err == nil {
		_, _ = f.Write(config)
	}
	if f, err := zw.Create("README.md"); err == nil {
		_, _ = f.Write([]byte(exportReadme(rr.Name, rr.Provider)))
	}
}

// exportReadme is the instructions file bundled into a workspace export.
func exportReadme(name, provider string) string {
	return fmt.Sprintf(`# %s — Terraform export

Generated by AutoDevOps. This is the exact, self-contained Terraform for this
resource — no lock-in, run it anywhere.

## Run it
1. Set your %s credentials in your environment (e.g. AWS_ACCESS_KEY_ID /
   AWS_SECRET_ACCESS_KEY, or ARM_* for Azure).
2. terraform init
3. terraform plan
4. terraform apply

The state file is intentionally NOT included (it can contain secrets). Running
the steps above creates a fresh state.
`, name, provider)
}

// handleDeleteWorkspace removes a resource's on-disk terraform workspace
// (main.tf + state + history files). Gated to status='destroyed' — deleting
// state while cloud resources still exist would orphan them.
func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rr, err := s.getResource(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform unavailable")
		return
	}
	if rr.Status != "destroyed" {
		writeError(w, http.StatusConflict, "destroy the resource before deleting its terraform workspace — deleting state while resources exist would orphan them")
		return
	}
	if err := s.tfRunner.RemoveWorkspace("", id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete workspace")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleImportTerraform registers a user-supplied Terraform config as a new
// "imported" workspace: it seeds the workspace's main.tf with the uploaded HCL,
// runs tfsec against it (report-only), then plans it (which validates). The
// config is authoritative and never regenerated. Requires a cloud account for
// provider credentials.
func (s *Server) handleImportTerraform(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform unavailable")
		return
	}
	var req struct {
		Name           string `json:"name"`
		HCL            string `json:"hcl"`
		CloudAccountID string `json:"cloud_account_id"`
		Region         string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || strings.TrimSpace(req.HCL) == "" {
		writeError(w, http.StatusBadRequest, "name and hcl are required")
		return
	}
	if req.CloudAccountID == "" {
		writeError(w, http.StatusBadRequest, "cloud_account_id is required (for provider credentials)")
		return
	}

	// Provider comes from the chosen cloud account (drives credential injection).
	var provider string
	if err := s.db.QueryRow(ctx, `SELECT provider FROM cloud_accounts WHERE id = $1`, req.CloudAccountID).Scan(&provider); err != nil {
		writeError(w, http.StatusBadRequest, "cloud account not found")
		return
	}
	region := strings.TrimSpace(req.Region)
	if region == "" {
		region = "-"
	}

	var id string
	if err := s.db.QueryRow(ctx, `
		INSERT INTO provisioned_resources (name, provider, cloud_account_id, region, resource_type, template, status)
		VALUES ($1, $2, $3, $4, 'imported', 'imported', 'pending')
		RETURNING id::text
	`, req.Name, provider, req.CloudAccountID, region).Scan(&id); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create imported workspace: %v", err))
		return
	}

	// Seed the workspace, scan it (report-only), and plan it in the background.
	if err := s.tfRunner.WriteConfig("", id, []byte(req.HCL)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write terraform config")
		return
	}
	s.scanWorkspaceTfsec(ctx, id)
	s.setResourceStatus(ctx, id, "planning")
	go func() {
		bg := context.Background()
		status := "planned"
		if _, err := s.tfRunner.Plan(bg, "", id, req.CloudAccountID, "imported", []byte(req.HCL)); err != nil {
			slog.Error("terraform: import plan failed", "id", id, "error", err)
			status = "failed"
		}
		s.setResourceStatus(bg, id, status)
	}()

	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": req.Name})
}

// handleListResourceFindings returns the tfsec findings for a resource's
// terraform workspace (compliance_findings scoped to resource_id).
func (s *Server) handleListResourceFindings(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rows, err := s.db.Query(r.Context(), `
		SELECT scanner, severity, rule_id, message, COALESCE(file_path,''), COALESCE(line_number,0)
		FROM compliance_findings WHERE resource_id = $1
		ORDER BY CASE severity
		  WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2
		  WHEN 'low' THEN 3 ELSE 4 END, scanner`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list findings")
		return
	}
	defer rows.Close()
	type finding struct {
		Scanner    string `json:"scanner"`
		Severity   string `json:"severity"`
		RuleID     string `json:"rule_id"`
		Message    string `json:"message"`
		FilePath   string `json:"file_path"`
		LineNumber int    `json:"line_number"`
	}
	out := []finding{}
	for rows.Next() {
		var f finding
		if err := rows.Scan(&f.Scanner, &f.Severity, &f.RuleID, &f.Message, &f.FilePath, &f.LineNumber); err == nil {
			out = append(out, f)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// scanWorkspaceTfsec runs the tfsec IaC scanner against a resource's terraform
// workspace and records the findings (report-only — surfaced, never blocks).
// Best-effort: failures are logged, not fatal.
func (s *Server) scanWorkspaceTfsec(ctx context.Context, resourceID string) {
	if s.compliance == nil || s.tfRunner == nil {
		return
	}
	dir, err := s.tfRunner.WorkspaceDir("", resourceID)
	if err != nil {
		slog.Warn("compliance: resolve workspace dir", "resource_id", resourceID, "error", err)
		return
	}
	if err := s.compliance.ScanWorkspace(ctx, resourceID, dir); err != nil {
		// gate error is informational here (report-only) — findings are persisted.
		slog.Info("compliance: tfsec workspace findings present", "resource_id", resourceID, "detail", err)
	}
}

// safeFilename strips a resource name down to a filesystem/HTTP-header-safe
// token for download filenames.
func safeFilename(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "resource"
	}
	return s
}
