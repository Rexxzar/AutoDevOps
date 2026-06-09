package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

type trivyReport struct {
	Results []trivyResult `json:"Results"`
}

type trivyResult struct {
	Target          string            `json:"Target"`
	Vulnerabilities []trivyVulnerability `json:"Vulnerabilities"`
}

type trivyVulnerability struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	Severity         string `json:"Severity"`
	Title            string `json:"Title"`
	PkgName          string `json:"PkgName"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
}

func runTrivy(ctx context.Context, imageRef string) ([]Finding, error) {
	path, err := exec.LookPath("trivy")
	if err != nil {
		return nil, fmt.Errorf("trivy not on PATH")
	}
	if imageRef == "" {
		return nil, nil
	}

	out, err := exec.CommandContext(ctx, path,
		"image",
		"--format", "json",
		"--quiet",
		"--exit-code", "0",
		imageRef,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("trivy: run: %w", err)
	}
	return parseTrivy(out)
}

func parseTrivy(data []byte) ([]Finding, error) {
	var report trivyReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("trivy: parse output: %w", err)
	}

	var findings []Finding
	for _, result := range report.Results {
		for _, v := range result.Vulnerabilities {
			msg := v.Title
			if msg == "" {
				msg = v.VulnerabilityID
			}
			if v.PkgName != "" {
				msg = fmt.Sprintf("%s (pkg: %s %s", msg, v.PkgName, v.InstalledVersion)
				if v.FixedVersion != "" {
					msg += ", fix: " + v.FixedVersion
				}
				msg += ")"
			}
			findings = append(findings, Finding{
				Scanner:  "trivy",
				Severity: ParseSeverity(v.Severity),
				RuleID:   v.VulnerabilityID,
				Message:  msg,
				FilePath: result.Target,
			})
		}
	}
	return findings, nil
}
