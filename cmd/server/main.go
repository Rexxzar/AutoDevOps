package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rexxzar/autodevops/internal/api"
	"github.com/Rexxzar/autodevops/internal/auth"
	"github.com/Rexxzar/autodevops/internal/cloud"
	"github.com/Rexxzar/autodevops/internal/compliance"
	"github.com/Rexxzar/autodevops/internal/console"
	"github.com/Rexxzar/autodevops/internal/db"
	"github.com/Rexxzar/autodevops/internal/deploy"
	"github.com/Rexxzar/autodevops/internal/docker"
	"github.com/Rexxzar/autodevops/internal/k8s"
	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/Rexxzar/autodevops/internal/terraform"
	"github.com/Rexxzar/autodevops/pkg/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cfg := config.Load()

	if cfg.JWTSecret == "" {
		slog.Error("JWT_SECRET is required")
		os.Exit(1)
	}
	if cfg.MasterKey == "" {
		slog.Error("MASTER_KEY is required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("db open failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, "./migrations"); err != nil {
		slog.Error("migrations failed", "error", err)
		os.Exit(1)
	}

	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		slog.Error("hash admin password", "error", err)
		os.Exit(1)
	}
	if err := db.EnsureAdminUser(ctx, pool, cfg.AdminEmail, hash); err != nil {
		slog.Error("seed admin user", "error", err)
		os.Exit(1)
	}

	a := auth.New(cfg.JWTSecret)
	hub := sse.NewHub()

	vault, err := secrets.New(ctx, cfg.MasterKey, pool)
	if err != nil {
		slog.Error("init vault", "error", err)
		os.Exit(1)
	}

	k8sCtrl := startK8sController(ctx, cfg, pool, hub)
	tfExecPath := ensureTerraformBinary(ctx, cfg)
	startCloudPoller(ctx, cfg, pool, hub, vault)

	reposDir := filepath.Join(cfg.DataDir, "repos")

	var kubeDeployer deploy.KubeDeployer
	if k8sCtrl != nil {
		kubeDeployer = k8sCtrl
	}

	dockerMgr := startDockerManager(ctx, cfg, pool, hub, reposDir)
	var dockerDeployer deploy.DockerDeployer
	var imagePusher deploy.ImagePusher
	var imageSaver deploy.ImageSaver
	if dockerMgr != nil {
		dockerDeployer = dockerMgr
		imagePusher = dockerMgr
		imageSaver = dockerMgr
	}

	var tfRunner *terraform.Runner
	if tfExecPath != "" {
		tfRunner = terraform.NewRunner(tfExecPath, filepath.Join(cfg.DataDir, "terraform", "workspaces"), pool, hub, vault)
	}
	var aciRedeployer deploy.ACIRedeployer
	if tfRunner != nil {
		aciRedeployer = tfRunner
	}

	complianceScanner := compliance.New(pool, hub, cfg.ComplianceThreshold)
	// Auto-install the scanner binaries (gitleaks/hadolint/tfsec/trivy) the same
	// way terraform is resolved — detect on PATH or download pinned versions into
	// a user-writable dir — so a fresh AutoDevOps host gets a working compliance
	// gate with no manual setup. Best-effort + non-blocking: absent scanners are
	// skipped at scan time, and the ~90MB of downloads must not delay boot.
	go compliance.EnsureTools(ctx, filepath.Join(cfg.DataDir, "compliance", "bin"))
	orchestrator := deploy.New(pool, hub, vault, reposDir, cfg.BuilderImage, cfg.RegistryPush, cfg.RegistryPull, cfg.IngressDomain, kubeDeployer, dockerDeployer, imagePusher, imageSaver, aciRedeployer, complianceScanner)
	if dockerMgr != nil {
		if cfg.PublicIP != "" {
			kindIngressPort := uint32(8880)
			if p, err := strconv.ParseUint(cfg.KindIngressHTTPPort, 10, 32); err == nil {
				kindIngressPort = uint32(p)
			}
			tlsProv := deploy.NewTLSProvisioner(dockerMgr.Client(), pool, cfg.PublicIP, cfg.AdminEmail, kindIngressPort)
			orchestrator.SetTLSProvisioner(tlsProv)
		}
		// Use Publish:true pack mode when the Docker daemon uses containerd image store,
		// which is incompatible with Publish:false. The lifecycle registry address is the
		// registry as seen from inside Docker containers (bridge gateway IP + port).
		// Auto-detected from the Docker bridge network; can be overridden via REGISTRY_LIFECYCLE_ADDR.
		lifecycleAddr := cfg.RegistryLifecycle
		if lifecycleAddr == "" {
			gw := dockerMgr.BridgeGatewayIP(ctx)
			// Only auto-switch if registryPush uses localhost — on remote registries
			// the existing Publish:false + PushImage path is correct.
			if strings.HasPrefix(cfg.RegistryPush, "localhost:") {
				port := strings.TrimPrefix(cfg.RegistryPush, "localhost:")
				lifecycleAddr = gw + ":" + port
			}
		}
		if lifecycleAddr != "" {
			orchestrator.SetRegistryLifecycle(lifecycleAddr)
			slog.Info("pack: using Publish:true with lifecycle registry", "addr", lifecycleAddr)
		}
	}

	tfEngine, err := terraform.New([]terraform.Definition{
		{ID: terraform.AWSStackID, Provider: terraform.ProviderAWS, Path: "templates/aws/stack.tf.tmpl"},
		{ID: terraform.AzureStackID, Provider: terraform.ProviderAzure, Path: "templates/azure/stack.tf.tmpl"},
	})
	if err != nil {
		slog.Error("init terraform engine", "error", err)
		os.Exit(1)
	}

	consoleManager := console.NewManager(console.NewRegistry(pool, vault, cfg.KubeconfigPath))
	go consoleManager.Run(ctx)

	handler := api.NewServer(pool, a, hub, vault, orchestrator, tfRunner, tfEngine, consoleManager, k8sCtrl, dockerMgr, complianceScanner, cfg.PublicIP, cfg.IngressDomain, cfg.RegistryPush)

	srv := &http.Server{
		Addr:        fmt.Sprintf(":%s", cfg.Port),
		Handler:     handler,
		ReadTimeout: 15 * time.Second,
		IdleTimeout: 60 * time.Second,
	}

	go func() {
		slog.Info("autodevops server starting", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := srv.Shutdown(stopCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

// startK8sController registers the local cluster in the DB and starts the Watch
// loop. Returns the controller so callers can use it as a KubeDeployer for T14.
// Returns nil and logs a warning if no kubeconfig is available.
func startK8sController(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, hub *sse.Hub) *k8s.Controller {
	serverURL, err := k8s.ServerURL(cfg.KubeconfigPath)
	if err != nil {
		slog.Warn("k8s: no kubeconfig found, self-healing controller disabled", "error", err)
		return nil
	}

	clusterID, err := upsertCluster(ctx, pool, cfg.ClusterName, serverURL)
	if err != nil {
		slog.Error("k8s: register cluster", "error", err)
		return nil
	}

	ctrl, err := k8s.New(cfg.KubeconfigPath, clusterID, pool, hub, cfg.IngressDomain)
	if err != nil {
		slog.Error("k8s: init controller", "error", err)
		return nil
	}

	go func() {
		if err := ctrl.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("k8s: controller exited", "error", err)
		}
	}()

	return ctrl
}

// ensureTerraformBinary resolves a terraform binary at startup, silently
// auto-installing a pinned version into DataDir if no matching one is found
// on PATH (see terraform.EnsureBinary) — the locked "detect on PATH or
// download a pinned version into a user-writable dir at engine startup"
// approach for terraform's static binary (contrast Docker's privileged-daemon
// detect-only path in T14b; see the design notes: Dev Notes). Logs a warning and
// degrades gracefully on failure: provisioning stays unavailable rather than
// blocking startup. The resolved path is what T14's k8s-manifest deploy step
// will eventually pass to terraform.NewRunner via deploy.Orchestrator (T12) —
// see internal/terraform/runner.go.
func ensureTerraformBinary(ctx context.Context, cfg *config.Config) string {
	installDir := filepath.Join(cfg.DataDir, "terraform", "bin")
	execPath, err := terraform.EnsureBinary(ctx, installDir)
	if err != nil {
		slog.Warn("terraform: binary unavailable, provisioning disabled", "error", err)
		return ""
	}
	slog.Info("terraform: binary resolved", "path", execPath)
	return execPath
}

// startCloudPoller launches the AWS/Azure resource poller. Unlike
// startK8sController there is no precondition to check — the poller handles
// "zero cloud accounts registered" by simply upserting nothing each tick — so
// it always starts; bad credentials for one account are logged and skipped
// without affecting others (see cloud.Poller.Run).
func startCloudPoller(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, hub *sse.Hub, vault *secrets.Vault) {
	poller := cloud.NewPoller(pool, hub, vault, cfg.CloudPollInterval)
	go func() {
		if err := poller.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("cloud: poller exited", "error", err)
		}
	}()
}

// startDockerManager builds the Docker Manager and starts its health-poll loop.
// Returns nil and logs a warning if the Docker client cannot be initialised —
// the daemon ping happens later at DockerDeploy time, surfacing ErrDockerNotFound
// with guided install instructions rather than blocking server startup.
func startDockerManager(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, hub *sse.Hub, reposDir string) *docker.Manager {
	composeDir := filepath.Join(cfg.DataDir, "compose")
	mgr, err := docker.New(pool, hub, composeDir, cfg.RegistryPush, cfg.RegistryPull, cfg.DockerPollInterval)
	if err != nil {
		slog.Warn("docker: manager init failed, container target disabled", "error", err)
		return nil
	}
	go func() {
		if err := mgr.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("docker: manager exited", "error", err)
		}
	}()
	_ = reposDir
	return mgr
}

// upsertCluster inserts or updates the cluster record and returns its UUID.
func upsertCluster(ctx context.Context, pool *pgxpool.Pool, name, endpoint string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO k8s_clusters (name, endpoint, status)
		VALUES ($1, $2, 'connected')
		ON CONFLICT (name) DO UPDATE SET
			endpoint = EXCLUDED.endpoint,
			status   = 'connected'
		RETURNING id
	`, name, endpoint).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert cluster: %w", err)
	}
	return id, nil
}
