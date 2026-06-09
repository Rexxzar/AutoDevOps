// Package deploy implements the Git-to-deploy orchestrator: receiving GitHub
// push webhooks, cloning the target repository, and detecting its framework —
// the entry point that turns a registered deployment into a build/deploy
// pipeline for T13/T14 to drive. Mirrors internal/cloud's shape (constructor
// DI over pool/hub/vault, raw-SQL persistence, hub.Publish of classified state).
package deploy

import (
	"context"
	"time"

	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KubeDeployer applies K8s manifests for a completed image build. It is
// implemented by internal/k8s.Controller and injected into Orchestrator at
// construction time — keeping the import graph clean (deploy must not import
// k8s directly per the project package boundary rules).
type KubeDeployer interface {
	KubeDeploy(ctx context.Context, name, namespace, imageRef string) error
}

// Orchestrator drives the Git-to-deploy flow for registered deployments.
type Orchestrator struct {
	pool         *pgxpool.Pool
	hub          *sse.Hub
	vault        *secrets.Vault
	workDir      string
	builderImage string // e.g. "paketobuildpacks/builder-jammy-full:latest"
	registryPush string // registry host:port pack pushes to, e.g. "localhost:5001"
	registryPull string // registry host:port k8s manifests use, e.g. "registry.local:5000"
	kubeDeployer KubeDeployer // nil when k8s controller is not available
}

// New builds an Orchestrator. workDir is the base directory under which each
// deployment gets its own clone workspace (workDir/<deploymentID>).
// builderImage, registryPush, and registryPull drive the T13 Paketo build step.
// kube may be nil — the k8s deploy step is skipped for non-k8s target types or
// when no cluster is reachable.
func New(pool *pgxpool.Pool, hub *sse.Hub, vault *secrets.Vault, workDir, builderImage, registryPush, registryPull string, kube KubeDeployer) *Orchestrator {
	return &Orchestrator{
		pool:         pool,
		hub:          hub,
		vault:        vault,
		workDir:      workDir,
		builderImage: builderImage,
		registryPush: registryPush,
		registryPull: registryPull,
		kubeDeployer: kube,
	}
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
	ImageRef   string    `json:"image_ref,omitempty"`  // populated by T13 after successful build
	Namespace  string    `json:"namespace,omitempty"`  // populated by T14 after K8s manifests are applied
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
