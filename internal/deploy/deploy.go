// Package deploy implements the Git-to-deploy orchestrator: receiving GitHub
// push webhooks, cloning the target repository, and detecting its framework —
// the entry point that turns a registered deployment into a build/deploy
// pipeline for T13/T14 to drive. Mirrors internal/cloud's shape (constructor
// DI over pool/hub/vault, raw-SQL persistence, hub.Publish of classified state).
package deploy

import (
	"time"

	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Orchestrator drives the Git-to-deploy flow for registered deployments.
type Orchestrator struct {
	pool    *pgxpool.Pool
	hub     *sse.Hub
	vault   *secrets.Vault
	workDir string
}

// New builds an Orchestrator. workDir is the base directory under which each
// deployment gets its own clone workspace (workDir/<deploymentID>).
func New(pool *pgxpool.Pool, hub *sse.Hub, vault *secrets.Vault, workDir string) *Orchestrator {
	return &Orchestrator{pool: pool, hub: hub, vault: vault, workDir: workDir}
}

// gitCredentials is the JSON shape stored (encrypted) via vault.Put under
// deployments.git_credentials_secret_id and read back via vault.GetByID —
// mirrors awsCredentials/azureCredentials from internal/cloud. Exactly one of
// Token/SSHKey is populated, matching the user's choice of auth method.
type gitCredentials struct {
	Type   string `json:"type"`              // "token" | "ssh_key"
	Token  string `json:"token,omitempty"`   // GitHub PAT, used as HTTP basic-auth password
	SSHKey string `json:"ssh_key,omitempty"` // PEM-encoded private deploy key
}

// Deployment is the classified row state published over SSE as "deploy.status"
// after each processing step — mirrors how cloud.Resource/k8s.PodHealth carry
// the full classified state directly as the event payload.
type Deployment struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	RepoURL    string    `json:"repo_url"`
	Branch     string    `json:"branch"`
	Framework  string    `json:"framework,omitempty"`
	TargetType string    `json:"target_type"`
	Status     string    `json:"status"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Credential type identifiers — the "type" field of gitCredentials.
const (
	GitAuthToken  = "token"
	GitAuthSSHKey = "ssh_key"
)

// Framework identifiers persisted into deployments.framework — drive T13's
// Buildpacks builder selection. Plain strings, no central enum, matching how
// internal/cloud declares ProviderAWS/ResourceTypeEC2Instance.
const (
	FrameworkNode    = "node"
	FrameworkPython  = "python"
	FrameworkGo      = "go"
	FrameworkJava    = "java"
	FrameworkRuby    = "ruby"
	FrameworkPHP     = "php"
	FrameworkUnknown = "unknown"
)
