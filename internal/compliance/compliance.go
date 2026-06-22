// Package compliance runs Hadolint, Trivy, tfsec, and GitLeaks against
// deployment artefacts, persists findings to compliance_findings, publishes
// SSE events, and evaluates a configurable severity-threshold gate.
// All four scanners are optional: if the binary is not on PATH the scanner
// logs a warning and continues — the gate only fires on actual findings.
package compliance

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Severity is an ordered integer so findings can be compared against a
// threshold with a single >= check.
type Severity int

const (
	SeverityInfo     Severity = 0
	SeverityLow      Severity = 1
	SeverityMedium   Severity = 2
	SeverityHigh     Severity = 3
	SeverityCritical Severity = 4
)

// ParseSeverity converts a case-insensitive string (e.g. "HIGH", "critical")
// to a Severity. Unrecognised values map to SeverityInfo.
func ParseSeverity(s string) Severity {
	switch strings.ToLower(s) {
	case "critical":
		return SeverityCritical
	case "high":
		return SeverityHigh
	case "medium", "moderate":
		return SeverityMedium
	case "low":
		return SeverityLow
	default:
		return SeverityInfo
	}
}

func (s Severity) String() string {
	switch s {
	case SeverityCritical:
		return "critical"
	case SeverityHigh:
		return "high"
	case SeverityMedium:
		return "medium"
	case SeverityLow:
		return "low"
	default:
		return "info"
	}
}

// Finding is the normalised shape produced by every scanner and persisted to
// compliance_findings — one row per finding regardless of which tool found it.
type Finding struct {
	Scanner    string   `json:"scanner"`
	Severity   Severity `json:"severity"`
	RuleID     string   `json:"rule_id"`
	Message    string   `json:"message"`
	FilePath   string   `json:"file_path,omitempty"`
	LineNumber int      `json:"line_number,omitempty"`
}

// Scanner orchestrates all four compliance tools and owns the DB + SSE writes.
type Scanner struct {
	pool      *pgxpool.Pool
	hub       *sse.Hub
	threshold Severity
}

// New builds a Scanner. threshold is parsed via ParseSeverity; an unrecognised
// value defaults to SeverityInfo (everything blocks — safe default).
func New(pool *pgxpool.Pool, hub *sse.Hub, threshold string) *Scanner {
	return &Scanner{
		pool:      pool,
		hub:       hub,
		threshold: ParseSeverity(threshold),
	}
}

// RunAll runs GitLeaks, Trivy, Hadolint, and tfsec in sequence against the
// given artefacts. Every finding is persisted and published via SSE regardless
// of severity. Returns a non-nil error only when the gate blocks (one or more
// findings meet or exceed the configured threshold). Scanner-level failures
// (binary absent, registry unreachable) are logged and skipped.
func (s *Scanner) RunAll(ctx context.Context, appID, repoPath, imageRef string) error {
	var all []Finding

	if findings, err := runGitleaks(ctx, repoPath); err != nil {
		slog.Warn("compliance: gitleaks skipped", "error", err)
	} else {
		all = append(all, findings...)
	}

	if findings, err := runTrivy(ctx, imageRef); err != nil {
		slog.Warn("compliance: trivy skipped", "error", err)
	} else {
		all = append(all, findings...)
	}

	if findings, err := runHadolint(ctx, repoPath); err != nil {
		slog.Warn("compliance: hadolint skipped", "error", err)
	} else {
		all = append(all, findings...)
	}

	if findings, err := runTfsec(ctx, repoPath); err != nil {
		slog.Warn("compliance: tfsec skipped", "error", err)
	} else {
		all = append(all, findings...)
	}

	for i := range all {
		all[i].FilePath = repoRelative(repoPath, all[i].FilePath)
	}
	for _, f := range all {
		if err := s.persist(ctx, appID, f); err != nil {
			slog.Warn("compliance: persist finding", "scanner", f.Scanner, "rule", f.RuleID, "error", err)
		}
	}

	slog.Info("compliance: scan complete", "app_id", appID, "total_findings", len(all))
	return Gate(all, s.threshold)
}

// repoRelative converts a scanner-reported file path to one relative to the
// repo root (slash-separated), so findings reference paths the editor can open
// (e.g. "config/credentials.env", not the full clone path). Scanners report
// paths either absolute or relative to the server's working dir (e.g.
// "data/repos/repos/<id>/config/credentials.env"), so both base and p are
// resolved against the working dir before computing the relative path. Paths
// outside base are returned unchanged.
func repoRelative(base, p string) string {
	if p == "" {
		return p
	}
	absBase, e1 := filepath.Abs(base)
	absP, e2 := filepath.Abs(p)
	if e1 == nil && e2 == nil {
		if rel, err := filepath.Rel(absBase, absP); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return p
}

// ScanSource runs the source-level scanners (GitLeaks, Hadolint, tfsec) against
// a repo working tree with no built image — the manual "Run compliance check"
// action on a stored repo. Trivy's image scan is deliberately skipped (there is
// no image to scan; that runs at deploy time via RunAll). Previous findings for
// the repo are cleared first so each scan reflects the current tree. Findings
// are persisted with repo_id (not app_id) and published via SSE. Returns a
// non-nil error only when the gate blocks.
func (s *Scanner) ScanSource(ctx context.Context, repoID, repoPath string) error {
	var all []Finding

	if findings, err := runGitleaks(ctx, repoPath); err != nil {
		slog.Warn("compliance: gitleaks skipped", "error", err)
	} else {
		all = append(all, findings...)
	}

	if findings, err := runHadolint(ctx, repoPath); err != nil {
		slog.Warn("compliance: hadolint skipped", "error", err)
	} else {
		all = append(all, findings...)
	}

	if findings, err := runTfsec(ctx, repoPath); err != nil {
		slog.Warn("compliance: tfsec skipped", "error", err)
	} else {
		all = append(all, findings...)
	}

	for i := range all {
		all[i].FilePath = repoRelative(repoPath, all[i].FilePath)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM compliance_findings WHERE repo_id = $1`, repoID); err != nil {
		slog.Warn("compliance: clear previous repo findings", "repo_id", repoID, "error", err)
	}
	for _, f := range all {
		if err := s.persistRepo(ctx, repoID, f); err != nil {
			slog.Warn("compliance: persist finding", "scanner", f.Scanner, "rule", f.RuleID, "error", err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE git_repos SET last_scanned_at = now() WHERE id = $1`, repoID); err != nil {
		slog.Warn("compliance: update last_scanned_at", "repo_id", repoID, "error", err)
	}

	slog.Info("compliance: source scan complete", "repo_id", repoID, "total_findings", len(all))
	return Gate(all, s.threshold)
}

// Gate returns a non-nil error if any finding meets or exceeds threshold,
// listing all blocking findings in the message.
func Gate(findings []Finding, threshold Severity) error {
	var blocked []string
	for _, f := range findings {
		if f.Severity >= threshold {
			blocked = append(blocked, fmt.Sprintf("%s[%s]:%s", f.Scanner, f.Severity, f.RuleID))
		}
	}
	if len(blocked) > 0 {
		return fmt.Errorf("compliance gate blocked — %d finding(s) at or above %s: %s",
			len(blocked), threshold, strings.Join(blocked, "; "))
	}
	return nil
}

func (s *Scanner) persist(ctx context.Context, appID string, f Finding) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO compliance_findings
		  (app_id, scanner, severity, rule_id, message, file_path, line_number)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7, 0))
	`, appID, f.Scanner, f.Severity.String(), f.RuleID, f.Message, f.FilePath, f.LineNumber)
	if err != nil {
		return fmt.Errorf("compliance: persist: %w", err)
	}
	s.hub.Publish(sse.Event{Type: "compliance.finding", Payload: f})
	return nil
}

// persistRepo inserts a repo-scoped finding (manual repo-store scan) — same
// shape as persist but keyed on repo_id instead of app_id.
func (s *Scanner) persistRepo(ctx context.Context, repoID string, f Finding) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO compliance_findings
		  (repo_id, scanner, severity, rule_id, message, file_path, line_number)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7, 0))
	`, repoID, f.Scanner, f.Severity.String(), f.RuleID, f.Message, f.FilePath, f.LineNumber)
	if err != nil {
		return fmt.Errorf("compliance: persist repo finding: %w", err)
	}
	s.hub.Publish(sse.Event{Type: "compliance.finding", Payload: f})
	return nil
}
