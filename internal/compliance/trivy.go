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
	Target          string               `json:"Target"`
	Class           string               `json:"Class"` // "os-pkgs" (base image) | "lang-pkgs" (app deps)
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
		"--scanners", "vuln", // CVEs only — gitleaks handles secrets; also faster
		"--ignore-unfixed",   // only CVEs with an available fix (drops won't-fix base-OS noise)
		"--severity", "HIGH,CRITICAL", // skip low/medium — pure base-image noise that never reaches the gate
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
	// Dedupe by CVE id — the same vulnerability is reported once per package that
	// links the affected library (e.g. one zlib CVE shows up 10+ times), which
	// massively inflates the count. Show each CVE once.
	seen := make(map[string]bool)
	for _, result := range report.Results {
		for _, v := range result.Vulnerabilities {
			if v.VulnerabilityID != "" {
				if seen[v.VulnerabilityID] {
					continue
				}
				seen[v.VulnerabilityID] = true
			}
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
				Class:    result.Class,
			})
		}
	}
	return findings, nil
}
