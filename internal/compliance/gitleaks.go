package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

type gitleaksFinding struct {
	RuleID      string `json:"RuleID"`
	Description string `json:"Description"`
	File        string `json:"File"`
	StartLine   int    `json:"StartLine"`
}

func runGitleaks(ctx context.Context, repoPath string) ([]Finding, error) {
	path, err := exec.LookPath("gitleaks")
	if err != nil {
		return nil, fmt.Errorf("gitleaks not on PATH")
	}

	// Use a temp file for the report — avoids /dev/stdout which doesn't exist on Windows.
	tmp, err := os.CreateTemp("", "autodevops-gitleaks-*.json")
	if err != nil {
		return nil, fmt.Errorf("gitleaks: create temp file: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	if err := exec.CommandContext(ctx, path,
		"detect",
		"--source", repoPath,
		"--no-git", // scan the working-tree files, not just git history — the
		// repo clone is shallow (depth 1) so history-only scanning misses
		// secrets that live in the files on disk; this matches how hadolint/
		// tfsec scan the checked-out files.
		"--report-format", "json",
		"--report-path", tmp.Name(),
		"--no-banner",
		"--exit-code", "0", // exit 0 regardless; we evaluate findings ourselves
	).Run(); err != nil {
		return nil, fmt.Errorf("gitleaks: run: %w", err)
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, fmt.Errorf("gitleaks: read report: %w", err)
	}
	return parseGitleaks(data)
}

func parseGitleaks(data []byte) ([]Finding, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}

	var raw []gitleaksFinding
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("gitleaks: parse output: %w", err)
	}

	findings := make([]Finding, 0, len(raw))
	for _, r := range raw {
		msg := r.Description
		if msg == "" {
			msg = r.RuleID
		}
		// Any detected secret is treated as high — there's no "low severity" leaked credential.
		findings = append(findings, Finding{
			Scanner:    "gitleaks",
			Severity:   SeverityHigh,
			RuleID:     r.RuleID,
			Message:    msg,
			FilePath:   r.File,
			LineNumber: r.StartLine,
		})
	}
	return findings, nil
}
