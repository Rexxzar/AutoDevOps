package cicd

import (
	"strings"
	"testing"
)

var baseParams = WorkflowParams{
	DeploymentID: "abc123",
	Name:         "my-app",
	Branch:       "main",
}

func TestWorkflowContainsRequiredFields(t *testing.T) {
	for _, fw := range []string{"node", "python", "go", "java", "ruby", "php", "unknown"} {
		t.Run(fw, func(t *testing.T) {
			p := baseParams
			p.Framework = fw
			out := Workflow(p)

			required := []string{
				"name: AutoDevOps CI/CD",
				"branches: [main]",
				"AUTODEVOPS_WEBHOOK_SECRET",
				"AUTODEVOPS_URL",
				"/webhooks/github/abc123",
				"X-Hub-Signature-256",
				"X-GitHub-Event: push",
				"refs/heads/main",
				"actions/checkout@v4",
			}
			for _, want := range required {
				if !strings.Contains(out, want) {
					t.Errorf("framework=%q: output missing %q", fw, want)
				}
			}
		})
	}
}

func TestWorkflowFrameworkSteps(t *testing.T) {
	cases := []struct {
		framework string
		want      []string
	}{
		{"node", []string{"setup-node@v4", "npm ci", "npm test"}},
		{"python", []string{"setup-python@v5", "pytest"}},
		{"go", []string{"setup-go@v5", "go test ./...", "go vet ./..."}},
		{"java", []string{"setup-java@v4", "temurin", "mvnw"}},
		{"ruby", []string{"setup-ruby@v1", "bundle exec"}},
		{"php", []string{"setup-php@v2", "composer install"}},
		{"unknown", []string{"Framework unknown"}},
	}
	for _, tc := range cases {
		t.Run(tc.framework, func(t *testing.T) {
			out := Workflow(WorkflowParams{
				DeploymentID: "dep1",
				Name:         "svc",
				Branch:       "main",
				Framework:    tc.framework,
			})
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("framework=%q: missing %q", tc.framework, want)
				}
			}
		})
	}
}

func TestWorkflowBranch(t *testing.T) {
	out := Workflow(WorkflowParams{
		DeploymentID: "dep1",
		Name:         "svc",
		Branch:       "develop",
		Framework:    "go",
	})
	if !strings.Contains(out, "branches: [develop]") {
		t.Error("branch not reflected in trigger")
	}
	if !strings.Contains(out, "refs/heads/develop") {
		t.Error("branch not in webhook payload")
	}
}

func TestWorkflowDeploymentID(t *testing.T) {
	out := Workflow(WorkflowParams{
		DeploymentID: "unique-deploy-id",
		Name:         "svc",
		Branch:       "main",
		Framework:    "node",
	})
	if !strings.Contains(out, "/webhooks/github/unique-deploy-id") {
		t.Error("deployment ID not in webhook URL")
	}
}

func TestWorkflowCIBeforeDeploy(t *testing.T) {
	out := Workflow(WorkflowParams{
		DeploymentID: "dep1",
		Name:         "svc",
		Branch:       "main",
		Framework:    "go",
	})
	// "needs: ci" must appear in the deploy job
	if !strings.Contains(out, "needs: ci") {
		t.Error("deploy job does not declare 'needs: ci' — deploy would run in parallel with tests")
	}
}

func TestWorkflowNoPlaintextSecret(t *testing.T) {
	out := Workflow(WorkflowParams{
		DeploymentID: "dep1",
		Name:         "svc",
		Branch:       "main",
		Framework:    "go",
	})
	// Secret must only appear as a GitHub Actions secret reference, never as a
	// literal value that could be committed to the repository.
	if strings.Contains(out, "webhook_secret: ") && !strings.Contains(out, "secrets.AUTODEVOPS_WEBHOOK_SECRET") {
		t.Error("plaintext secret value found in generated workflow")
	}
}
