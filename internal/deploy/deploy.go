// Package deploy implements the Git-to-deploy orchestrator: receiving GitHub
// push webhooks, cloning the target repository, and detecting its framework —
// the entry point that turns a registered deployment into a build/deploy
// pipeline for T13/T14 to drive. Mirrors internal/cloud's shape (constructor
// DI over pool/hub/vault, raw-SQL persistence, hub.Publish of classified state).
package deploy

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/docker/docker/api/types/registry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KubeDeployer applies K8s manifests for a completed image build. It is
// implemented by internal/k8s.Controller and injected into Orchestrator at
// construction time — keeping the import graph clean (deploy must not import
// k8s directly per the project package boundary rules). extraEnv carries
// AUTODEVOPS_* dependency-link vars (T-Phase3 resolveLinks) injected into the
// app container alongside PORT. KubeDeployImageApp is the Phase-4 counterpart
// for "image"-kind apps (no build pipeline): env is the app's own
// image_env_secret_id contents (e.g. POSTGRES_PASSWORD), applied as-is.
// secretFiles is the decrypted secret_files_id contents (container path ->
// file content, may be nil) mounted read-only via a K8s Secret + Volume.
type KubeDeployer interface {
	KubeDeploy(ctx context.Context, name, namespace, imageRef, domain string, extraEnv map[string]string, secretFiles map[string][]byte, k8sConfigJSON []byte) error
	KubeDeployImageApp(ctx context.Context, name, namespace, imageRef string, port int32, env map[string]string, volumePath string, secretFiles map[string][]byte) error
}

// DockerDeployer deploys a built image as a Docker container on the stack's
// per-stack network. It is implemented by internal/docker.Manager and
// injected into Orchestrator at construction time — same import-boundary
// reason as KubeDeployer. extraEnv carries AUTODEVOPS_* dependency-link vars
// (T-Phase3 resolveLinks) injected into the container alongside PORT.
// DockerDeployImageApp is the Phase-4 counterpart for "image"-kind apps (no
// build pipeline): env is the app's own image_env_secret_id contents (e.g.
// POSTGRES_PASSWORD), applied as-is. secretFiles is the decrypted
// secret_files_id contents (container path -> file content, may be nil)
// bind-mounted read-only from the host.
type DockerDeployer interface {
	DockerDeploy(ctx context.Context, stackName, appName, imageRef string, extraEnv map[string]string, secretFiles map[string][]byte) error
	DockerDeployImageApp(ctx context.Context, stackName, appName, imageRef string, containerPort uint16, env map[string]string, volumePath string, secretFiles map[string][]byte) error
}

// ImagePusher pushes a locally-built image to the registry, and builds images
// for "Dockerfile-if-present, else Paketo" apps. Implemented by
// internal/docker.Manager and injected into Orchestrator at construction time
// — same import-boundary reason as KubeDeployer/DockerDeployer.
//
// PushImage is needed because pack's lifecycle containers run in their own
// network namespace and can't reach the registry via its host-mapped
// "localhost:<port>" address (connection refused); buildImage builds with
// Publish: false and pushes from here instead, since the host Docker daemon
// CAN reach localhost registries directly (Docker treats loopback registries
// as insecure/HTTP automatically). auth is nil for the local registry (no
// credentials configured) and non-nil for the ECR/ACR pushes used by the
// cloud_ecs/cloud_aci targets.
//
// BuildImage builds the Dockerfile at contextDir's root via the Docker SDK and
// tags the result tag in the host daemon's local image store — the
// Dockerfile-path counterpart to buildImage's Paketo (pack) branch. Both paths
// land a tagged image in the same local daemon store, so PushImage works
// unchanged afterward regardless of which branch produced the image.
//
// TagImage applies an additional tag to an image already in the local daemon
// store — used to retag the locally-built image under an ECR/ACR repository
// reference before pushing it there.
type ImagePusher interface {
	PushImage(ctx context.Context, ref string, auth *registry.AuthConfig) error
	BuildImage(ctx context.Context, contextDir, tag string, logLine func(string)) error
	TagImage(ctx context.Context, source, target string) error
	PullImage(ctx context.Context, ref string) error
}

// ImageSaver exports a locally-built image as a tar stream, implemented by
// internal/docker.Manager (Docker SDK ImageSave) and injected into
// Orchestrator at construction time — same import-boundary reason as
// ImagePusher. Used by the cloud_vm SSH deployer to "docker load" the image
// on a remote VM without a registry round-trip.
type ImageSaver interface {
	SaveImage(ctx context.Context, ref string) (io.ReadCloser, error)
}

// ACIRedeployer swaps the running image (and environment variables) on an
// already-applied Azure Container Instance group via terraform HCL surgery +
// re-apply, implemented by internal/terraform.Runner and injected into
// Orchestrator at construction time — same import-boundary reason as
// KubeDeployer. resourceID is the provisioned_resources.id of the "aci"
// resource. The returned outputs let deployToACI refresh
// provisioned_resources.outputs after the re-apply.
type ACIRedeployer interface {
	RedeployACIImage(ctx context.Context, resourceID, cloudAccountID, image string, env map[string]string, regServer, regUser, regPass string) (map[string]json.RawMessage, error)
}

// ComplianceGate runs all scanners against an app's build artefacts and
// returns a non-nil error if any finding meets or exceeds the configured
// threshold. Implemented by internal/compliance.Scanner and injected at
// construction time.
type ComplianceGate interface {
	RunAll(ctx context.Context, appID, repoPath, imageRef string) error
}

// Orchestrator drives the Git-to-deploy flow for registered deployments.
type Orchestrator struct {
	pool           *pgxpool.Pool
	hub            *sse.Hub
	vault          *secrets.Vault
	workDir        string
	builderImage   string       // e.g. "paketobuildpacks/builder-jammy-full:latest"
	registryPush   string       // registry host:port pack pushes to, e.g. "localhost:5001"
	registryPull   string       // registry host:port k8s manifests use, e.g. "registry.local:5000"
	ingressDomain  string       // base domain for K8s Ingress hosts, e.g. "apps.127.0.0.1.sslip.io" — used by resolveLinks for docker->k8s links
	kubeDeployer    KubeDeployer   // nil when k8s controller is not available
	dockerDeployer  DockerDeployer // nil when Docker daemon is not available
	pusher          ImagePusher    // nil when Docker daemon is not available
	imageSaver      ImageSaver     // nil when Docker daemon is not available — required for cloud_vm targets
	aciRedeployer   ACIRedeployer  // nil when terraform is not available — required for cloud_aci targets
	complianceGate  ComplianceGate // nil disables compliance scanning
	tlsProvisioner  *TLSProvisioner // nil skips TLS step for local_docker
	// registryLifecycle is the registry address as seen from inside pack's
	// lifecycle containers (e.g. "172.17.0.1:5001") — used when the Docker
	// daemon uses containerd image store, where Publish:false fails. Empty
	// means fall back to Publish:false with registryPush.
	registryLifecycle string

	stepsMu sync.RWMutex
	steps   map[string]map[string]StepEvent // appID -> step -> latest StepEvent, for hydrating dashboard pages that load mid-deploy (deploy.step SSE events aren't replayed on reconnect)
}

// New builds an Orchestrator. workDir is the base directory under which each
// app gets its own clone workspace (workDir/<appID>).
// builderImage, registryPush, and registryPull drive the Paketo build step.
// ingressDomain is used by resolveLinks to address local_k8s apps from
// local_docker dependents (via their Ingress hostname).
// kube, docker, pusher, saver, aci, and gate may be nil — the corresponding
// step is skipped when nil (deploy step skipped if target type doesn't match;
// compliance skipped entirely; buildImage errors if pusher is nil since a
// build with no way to push the result can never succeed; deployToVM errors
// if saver is nil for the same reason; deployToACI errors if aci is nil since
// a redeploy with no way to re-apply terraform can never succeed).
func New(pool *pgxpool.Pool, hub *sse.Hub, vault *secrets.Vault, workDir, builderImage, registryPush, registryPull, ingressDomain string, kube KubeDeployer, docker DockerDeployer, pusher ImagePusher, saver ImageSaver, aci ACIRedeployer, gate ComplianceGate) *Orchestrator {
	return &Orchestrator{
		pool:           pool,
		hub:            hub,
		vault:          vault,
		workDir:        workDir,
		builderImage:   builderImage,
		registryPush:   registryPush,
		registryPull:   registryPull,
		ingressDomain:  ingressDomain,
		kubeDeployer:   kube,
		dockerDeployer: docker,
		pusher:         pusher,
		imageSaver:     saver,
		aciRedeployer:  aci,
		complianceGate: gate,
		steps:          make(map[string]map[string]StepEvent),
	}
}

// SetTLSProvisioner wires a TLSProvisioner into the Orchestrator after
// construction — called from cmd/server/main.go once the Docker client is
// available. A nil provisioner is a no-op: local_docker deployments with a
// domain set will still succeed but the StepTLS step is skipped.
func (o *Orchestrator) SetTLSProvisioner(p *TLSProvisioner) { o.tlsProvisioner = p }

// DeprovisionTLS removes the nginx vhost for domain when an app is deleted.
// No-op if TLS provisioner is not configured.
func (o *Orchestrator) DeprovisionTLS(ctx context.Context, domain string) error {
	if o.tlsProvisioner == nil {
		return nil
	}
	return o.tlsProvisioner.Deprovision(ctx, domain)
}

// SetRegistryLifecycle sets the registry address that pack lifecycle containers
// use to push images directly (Publish:true path). Must be reachable from
// inside Docker containers — typically the Docker bridge gateway IP + port,
// e.g. "172.17.0.1:5001". When set, the registry must also be bound to
// 0.0.0.0 (not 127.0.0.1) so containers can reach it.
func (o *Orchestrator) SetRegistryLifecycle(addr string) { o.registryLifecycle = addr }

// gitCredentials is the JSON shape stored (encrypted) via vault.Put under
// deployment_apps.git_credentials_secret_id and read back via vault.GetByID —
// mirrors awsCredentials/azureCredentials from internal/cloud. Exactly one of
// Token/SSHKey is populated, matching the user's choice of auth method.
type gitCredentials struct {
	Type   string `json:"type"`              // "token" | "ssh_key"
	Token  string `json:"token,omitempty"`   // GitHub PAT, used as HTTP basic-auth password
	SSHKey string `json:"ssh_key,omitempty"` // PEM-encoded private deploy key
}

// Deployment is the classified row state published over SSE as "deploy.status"
// after each processing step — mirrors how cloud.Resource/k8s.PodHealth carry
// the full classified state directly as the event payload. ID is the app's own
// id (deployment_apps.id); DeploymentID is the stack it belongs to.
type Deployment struct {
	ID           string    `json:"id"`
	DeploymentID string    `json:"deployment_id"`
	Name         string    `json:"name"`
	RepoURL      string    `json:"repo_url"`
	Branch       string    `json:"branch"`
	Framework    string    `json:"framework,omitempty"`
	ImageRef     string    `json:"image_ref,omitempty"` // populated by T13 after successful build
	Namespace    string    `json:"namespace,omitempty"` // populated by T14 after K8s manifests are applied
	TargetKind   string    `json:"target_kind"`
	Status       string    `json:"status"`
	UpdatedAt    time.Time `json:"updated_at"`
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
