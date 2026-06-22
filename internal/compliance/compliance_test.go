package compliance

import (
	"strings"
	"testing"
)

func TestParseSeverity(t *testing.T) {
	cases := []struct {
		in   string
		want Severity
	}{
		{"critical", SeverityCritical},
		{"CRITICAL", SeverityCritical},
		{"high", SeverityHigh},
		{"HIGH", SeverityHigh},
		{"medium", SeverityMedium},
		{"moderate", SeverityMedium},
		{"low", SeverityLow},
		{"info", SeverityInfo},
		{"unknown", SeverityInfo},
		{"", SeverityInfo},
	}
	for _, tc := range cases {
		if got := ParseSeverity(tc.in); got != tc.want {
			t.Errorf("ParseSeverity(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSeverityOrdering(t *testing.T) {
	if !(SeverityCritical > SeverityHigh) {
		t.Error("critical must be greater than high")
	}
	if !(SeverityHigh > SeverityMedium) {
		t.Error("high must be greater than medium")
	}
	if !(SeverityMedium > SeverityLow) {
		t.Error("medium must be greater than low")
	}
	if !(SeverityLow > SeverityInfo) {
		t.Error("low must be greater than info")
	}
}

func TestSeverityString(t *testing.T) {
	cases := []struct {
		s    Severity
		want string
	}{
		{SeverityCritical, "critical"},
		{SeverityHigh, "high"},
		{SeverityMedium, "medium"},
		{SeverityLow, "low"},
		{SeverityInfo, "info"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("Severity(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestGate_Pass(t *testing.T) {
	findings := []Finding{
		{Scanner: "trivy", Severity: SeverityLow, RuleID: "CVE-001"},
		{Scanner: "hadolint", Severity: SeverityMedium, RuleID: "DL3006"},
	}
	if err := Gate(findings, SeverityHigh); err != nil {
		t.Errorf("expected gate to pass, got: %v", err)
	}
}

func TestGate_BlockOnActionable(t *testing.T) {
	findings := []Finding{
		{Scanner: "trivy", Severity: SeverityCritical, RuleID: "CVE-2023-999"},
		{Scanner: "gitleaks", Severity: SeverityHigh, RuleID: "aws-access-key"},
	}
	err := Gate(findings, SeverityHigh)
	if err == nil {
		t.Error("expected gate to block on the actionable gitleaks high finding, got nil")
	}
}

// Base-image OS CVEs (os-pkgs) are report-only — even a wall of criticals must
// never block, since the app developer can't fix them.
func TestGate_TrivyBaseImageNeverBlocks(t *testing.T) {
	findings := []Finding{
		{Scanner: "trivy", Class: "os-pkgs", Severity: SeverityCritical, RuleID: "CVE-2021-3711"},
		{Scanner: "trivy", Class: "os-pkgs", Severity: SeverityHigh, RuleID: "CVE-2022-0778"},
		{Scanner: "trivy", Class: "os-pkgs", Severity: SeverityCritical, RuleID: "CVE-2022-37434"},
	}
	if err := Gate(findings, SeverityHigh); err != nil {
		t.Errorf("base-image (os-pkgs) CVEs are report-only and must not block, got: %v", err)
	}
}

// A CRITICAL CVE in the app's own dependency (lang-pkgs) blocks.
func TestGate_TrivyDependencyCriticalBlocks(t *testing.T) {
	findings := []Finding{
		{Scanner: "trivy", Class: "lang-pkgs", Severity: SeverityCritical, RuleID: "CVE-2024-DEP"},
	}
	if err := Gate(findings, SeverityHigh); err == nil {
		t.Error("critical CVE in app dependency (lang-pkgs) should block")
	}
}

// A HIGH (not critical) lang-pkgs CVE does NOT block — keeps the buildpack's own
// Go-binary highs from being false positives.
func TestGate_TrivyDependencyHighDoesNotBlock(t *testing.T) {
	findings := []Finding{
		{Scanner: "trivy", Class: "lang-pkgs", Severity: SeverityHigh, RuleID: "CVE-2024-GOBIN"},
	}
	if err := Gate(findings, SeverityHigh); err != nil {
		t.Errorf("a high (non-critical) lang-pkgs CVE should not block, got: %v", err)
	}
}

func TestGate_BlocksAtThreshold(t *testing.T) {
	findings := []Finding{
		{Scanner: "tfsec", Severity: SeverityHigh, RuleID: "aws-s3-public"},
	}
	if err := Gate(findings, SeverityHigh); err == nil {
		t.Error("finding exactly at threshold should block")
	}
}

func TestGate_EmptyFindings(t *testing.T) {
	if err := Gate(nil, SeverityHigh); err != nil {
		t.Errorf("empty findings should pass gate: %v", err)
	}
}

func TestGate_ErrorMessageListsActionable(t *testing.T) {
	findings := []Finding{
		{Scanner: "trivy", Severity: SeverityCritical, RuleID: "CVE-A"}, // report-only — excluded
		{Scanner: "hadolint", Severity: SeverityHigh, RuleID: "DL4000"},
		{Scanner: "gitleaks", Severity: SeverityHigh, RuleID: "aws-key"},
	}
	err := Gate(findings, SeverityHigh)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, ruleID := range []string{"DL4000", "aws-key"} {
		if !contains(msg, ruleID) {
			t.Errorf("error message missing actionable finding %q: %s", ruleID, msg)
		}
	}
	if contains(msg, "CVE-A") {
		t.Errorf("report-only trivy finding must not appear in the block message: %s", msg)
	}
}

func TestParseTrivy(t *testing.T) {
	data := []byte(`{
		"Results": [{
			"Target": "myapp:latest",
			"Vulnerabilities": [
				{
					"VulnerabilityID": "CVE-2023-1234",
					"Severity": "HIGH",
					"Title": "Buffer overflow in libssl",
					"PkgName": "libssl1.1",
					"InstalledVersion": "1.1.1f",
					"FixedVersion": "1.1.1t"
				},
				{
					"VulnerabilityID": "CVE-2023-5678",
					"Severity": "CRITICAL",
					"Title": "RCE in libz",
					"PkgName": "zlib1g",
					"InstalledVersion": "1.2.11"
				}
			]
		}]
	}`)

	findings, err := parseTrivy(data)
	if err != nil {
		t.Fatalf("parseTrivy: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	if findings[0].Severity != SeverityHigh {
		t.Errorf("first finding severity = %v, want high", findings[0].Severity)
	}
	if findings[1].Severity != SeverityCritical {
		t.Errorf("second finding severity = %v, want critical", findings[1].Severity)
	}
	if findings[0].RuleID != "CVE-2023-1234" {
		t.Errorf("rule ID = %q, want CVE-2023-1234", findings[0].RuleID)
	}
}

func TestParseTrivy_Empty(t *testing.T) {
	data := []byte(`{"Results": [{"Target": "myapp:latest"}]}`)
	findings, err := parseTrivy(data)
	if err != nil {
		t.Fatalf("parseTrivy: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

func TestParseTfsec(t *testing.T) {
	data := []byte(`{
		"results": [{
			"rule_id": "aws-s3-no-public-buckets",
			"severity": "CRITICAL",
			"description": "Bucket is publicly accessible",
			"location": {
				"filename": "/workspace/main.tf",
				"start_line": 42
			}
		}]
	}`)

	findings, err := parseTfsec(data)
	if err != nil {
		t.Fatalf("parseTfsec: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Scanner != "tfsec" {
		t.Errorf("scanner = %q, want tfsec", f.Scanner)
	}
	if f.Severity != SeverityCritical {
		t.Errorf("severity = %v, want critical", f.Severity)
	}
	if f.LineNumber != 42 {
		t.Errorf("line = %d, want 42", f.LineNumber)
	}
}

func TestParseGitleaks(t *testing.T) {
	data := []byte(`[
		{
			"RuleID": "aws-access-key",
			"Description": "AWS Access Key",
			"File": "config/secrets.yaml",
			"StartLine": 10
		}
	]`)

	findings, err := parseGitleaks(data)
	if err != nil {
		t.Fatalf("parseGitleaks: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Severity != SeverityHigh {
		t.Errorf("gitleaks finding severity = %v, want high", findings[0].Severity)
	}
}

func TestParseGitleaks_Null(t *testing.T) {
	for _, input := range []string{"null", "", "null\n"} {
		findings, err := parseGitleaks([]byte(input))
		if err != nil {
			t.Errorf("parseGitleaks(%q): unexpected error: %v", input, err)
		}
		if len(findings) != 0 {
			t.Errorf("parseGitleaks(%q): expected 0 findings, got %d", input, len(findings))
		}
	}
}

func TestParseHadolint(t *testing.T) {
	data := []byte(`[
		{"file":"Dockerfile","line":3,"code":"DL3006","message":"Always tag the version","level":"warning"},
		{"file":"Dockerfile","line":7,"code":"DL3002","message":"Do not switch to root USER","level":"error"}
	]`)

	findings, err := parseHadolint(data)
	if err != nil {
		t.Fatalf("parseHadolint: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	if findings[0].Severity != SeverityMedium {
		t.Errorf("warning level = %v, want medium", findings[0].Severity)
	}
	if findings[1].Severity != SeverityHigh {
		t.Errorf("error level = %v, want high", findings[1].Severity)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func TestRepoRelative(t *testing.T) {
	cases := []struct {
		name, base, p, want string
	}{
		{"relative clone path", "data/repos/repos/abc", "data/repos/repos/abc/config/credentials.env", "config/credentials.env"},
		{"nested file", "data/repos/x", "data/repos/x/src/app/main.go", "src/app/main.go"},
		{"already at root", "data/repos/x", "data/repos/x/Dockerfile", "Dockerfile"},
		{"empty stays empty", "data/repos/x", "", ""},
		{"unrelated path unchanged", "data/repos/x", "elsewhere/file.txt", "elsewhere/file.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := repoRelative(c.base, c.p); got != c.want {
				t.Errorf("repoRelative(%q, %q) = %q, want %q", c.base, c.p, got, c.want)
			}
		})
	}
}
