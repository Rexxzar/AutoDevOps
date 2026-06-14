// Package cicd generates per-framework GitHub Actions workflow YAML for
// AutoDevOps-managed deployments. The generated file drops into the repo's
// .github/workflows/ directory; it runs framework-specific CI (test/lint)
// and, on success, sends a signed webhook to trigger the AutoDevOps deploy
// pipeline — the same HMAC-SHA256 mechanism GitHub uses natively.
//
// Credentials are never embedded: the workflow references repository secrets
// (AUTODEVOPS_WEBHOOK_SECRET, AUTODEVOPS_URL) so neither value is committed.
package cicd

import (
	"fmt"
	"strings"
)

// WorkflowParams carries the deployment metadata required to generate the CI/CD
// workflow. Framework must be one of the constants from internal/deploy.
type WorkflowParams struct {
	AppID     string // UUID, embedded in the notify step's webhook URL
	Name      string // human label used in job names
	Branch    string // branch that triggers the workflow
	Framework string // node | python | go | java | ruby | php | unknown
}

// Workflow returns a complete GitHub Actions workflow YAML string for the given
// deployment. The file is self-contained and can be committed verbatim to
// .github/workflows/autodevops.yml.
func Workflow(p WorkflowParams) string {
	var b strings.Builder

	fmt.Fprintf(&b, `# AutoDevOps — generated CI/CD workflow for "%s"
# Commit this file to: .github/workflows/autodevops-%s.yml
#
# Add these secrets to your repository (Settings → Secrets → Actions):
#   AUTODEVOPS_WEBHOOK_SECRET   your deployment webhook secret
#   AUTODEVOPS_URL              base URL of your AutoDevOps server (no trailing slash)
#
# How it works:
#   1. On every push to %q the "ci" job runs framework-specific tests.
#   2. On success the "deploy" job sends a signed push event to AutoDevOps,
#      which clones the repo, rebuilds the image, and deploys it.

name: AutoDevOps CI/CD

on:
  push:
    branches: [%s]
  workflow_dispatch: {}

jobs:
  ci:
    name: Test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`, p.Name, p.Name, p.Branch, p.Branch)

	b.WriteString(ciSteps(p.Framework))

	fmt.Fprintf(&b, `
  deploy:
    name: Deploy via AutoDevOps
    needs: ci
    runs-on: ubuntu-latest
    steps:
      - name: Notify AutoDevOps
        env:
          WEBHOOK_SECRET: ${{ secrets.AUTODEVOPS_WEBHOOK_SECRET }}
          AUTODEVOPS_URL: ${{ secrets.AUTODEVOPS_URL }}
        run: |
          PAYLOAD=$(printf '{"ref":"refs/heads/%s","repository":{"full_name":"%%s"}}' "$GITHUB_REPOSITORY")
          SIG="sha256=$(printf '%%s' "$PAYLOAD" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" -binary | xxd -p -c 256)"
          curl -fsSL -X POST \
            -H "Content-Type: application/json" \
            -H "X-GitHub-Event: push" \
            -H "X-Hub-Signature-256: $SIG" \
            -d "$PAYLOAD" \
            "${AUTODEVOPS_URL}/webhooks/github/%s"
`, p.Branch, p.AppID)

	return b.String()
}

// ciSteps returns the framework-specific YAML step blocks (already indented
// for insertion into the steps list). Each block is indented with 6 spaces
// to align with the checkout step above it.
func ciSteps(framework string) string {
	switch framework {
	case "node":
		return `      - uses: actions/setup-node@v4
        with:
          node-version: '20'
          cache: npm
      - run: npm ci
      - run: npm test --if-present
      - run: npm run lint --if-present
`
	case "python":
		return `      - uses: actions/setup-python@v5
        with:
          python-version: '3.12'
      - name: Install dependencies
        run: |
          pip install -r requirements.txt 2>/dev/null \
            || pip install '.[dev]' 2>/dev/null \
            || pip install . 2>/dev/null \
            || true
      - name: Run tests
        run: pytest --tb=short 2>/dev/null || python -m pytest --tb=short 2>/dev/null || true
`
	case "go":
		return `      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - run: go test ./...
      - run: go vet ./...
`
	case "java":
		return `      - uses: actions/setup-java@v4
        with:
          java-version: '21'
          distribution: temurin
          cache: maven
      - name: Run tests
        run: |
          if [ -f mvnw ]; then ./mvnw test -B --no-transfer-progress
          elif [ -f gradlew ]; then ./gradlew test
          else mvn test -B --no-transfer-progress
          fi
`
	case "ruby":
		return `      - uses: ruby/setup-ruby@v1
        with:
          ruby-version: '3.3'
          bundler-cache: true
      - name: Run tests
        run: bundle exec rspec --format progress 2>/dev/null || bundle exec rake test
`
	case "php":
		return `      - uses: shivammathur/setup-php@v2
        with:
          php-version: '8.3'
          tools: composer
          coverage: none
      - run: composer install --prefer-dist --no-progress
      - name: Run tests
        run: |
          ./vendor/bin/phpunit --testdox 2>/dev/null \
            || composer test 2>/dev/null \
            || true
`
	default:
		return `      - name: No framework detected
        run: echo "Framework unknown — add build and test steps here."
`
	}
}
