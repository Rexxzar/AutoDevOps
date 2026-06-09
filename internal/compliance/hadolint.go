package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type hadolintFinding struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Level   string `json:"level"`
}

// hadolintSeverity maps hadolint levels to compliance Severity.
// Paketo Buildpacks generate no Dockerfile — if one is present it was
// authored by the user and should be checked.
func hadolintSeverity(level string) Severity {
	switch level {
	case "error":
		return SeverityHigh
	case "warning":
		return SeverityMedium
	case "info":
		return SeverityLow
	default:
		return SeverityInfo
	}
}

func runHadolint(ctx context.Context, repoPath string) ([]Finding, error) {
	dockerfile := filepath.Join(repoPath, "Dockerfile")
	if _, err := os.Stat(dockerfile); os.IsNotExist(err) {
		return nil, nil // no Dockerfile → nothing to lint
	}

	path, err := exec.LookPath("hadolint")
	if err != nil {
		return nil, fmt.Errorf("hadolint not on PATH")
	}

	out, err := exec.CommandContext(ctx, path,
		"--format", "json",
		dockerfile,
	).Output()
	// hadolint exits non-zero when findings exist; we still parse the output.
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("hadolint: run: %w", err)
	}
	return parseHadolint(out)
}

func parseHadolint(data []byte) ([]Finding, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var raw []hadolintFinding
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("hadolint: parse output: %w", err)
	}

	findings := make([]Finding, 0, len(raw))
	for _, r := range raw {
		findings = append(findings, Finding{
			Scanner:    "hadolint",
			Severity:   hadolintSeverity(r.Level),
			RuleID:     r.Code,
			Message:    r.Message,
			FilePath:   r.File,
			LineNumber: r.Line,
		})
	}
	return findings, nil
}
