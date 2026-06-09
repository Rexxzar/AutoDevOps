package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
)

type tfsecReport struct {
	Results []tfsecResult `json:"results"`
}

type tfsecResult struct {
	RuleID          string        `json:"rule_id"`
	Severity        string        `json:"severity"`
	Description     string        `json:"description"`
	RuleDescription string        `json:"rule_description"`
	Location        tfsecLocation `json:"location"`
}

type tfsecLocation struct {
	Filename  string `json:"filename"`
	StartLine int    `json:"start_line"`
}

func runTfsec(ctx context.Context, dir string) ([]Finding, error) {
	// Only scan if .tf files exist in the repo root — avoids running tfsec
	// on non-IaC repos and masks "no terraform files" errors from the binary.
	matches, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil || len(matches) == 0 {
		return nil, nil
	}

	path, err := exec.LookPath("tfsec")
	if err != nil {
		return nil, fmt.Errorf("tfsec not on PATH")
	}

	out, err := exec.CommandContext(ctx, path,
		"--format", "json",
		"--no-color",
		"--soft-fail", // exit 0 even with findings; we evaluate severity ourselves
		dir,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("tfsec: run: %w", err)
	}
	return parseTfsec(out)
}

func parseTfsec(data []byte) ([]Finding, error) {
	var report tfsecReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("tfsec: parse output: %w", err)
	}

	var findings []Finding
	for _, r := range report.Results {
		msg := r.Description
		if msg == "" {
			msg = r.RuleDescription
		}
		findings = append(findings, Finding{
			Scanner:    "tfsec",
			Severity:   ParseSeverity(r.Severity),
			RuleID:     r.RuleID,
			Message:    msg,
			FilePath:   r.Location.Filename,
			LineNumber: r.Location.StartLine,
		})
	}
	return findings, nil
}
