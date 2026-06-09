package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Rexxzar/autodevops/internal/api"
	"github.com/Rexxzar/autodevops/internal/auth"
	"github.com/Rexxzar/autodevops/internal/cloud"
	"github.com/Rexxzar/autodevops/internal/db"
	"github.com/Rexxzar/autodevops/internal/deploy"
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

	vault, err := secrets.New(cfg.MasterKey, pool)
	if err != nil {
		slog.Error("init vault", "error", err)
		os.Exit(1)
	}

	startK8sController(ctx, cfg, pool, hub)
	ensureTerraformBinary(ctx, cfg)
	startCloudPoller(ctx, cfg, pool, hub, vault)

	reposDir := filepath.Join(cfg.DataDir, "repos")
	orchestrator := deploy.New(pool, hub, vault, reposDir, cfg.BuilderImage, cfg.RegistryPush, cfg.RegistryPull)

	handler := api.NewServer(pool, a, hub, vault, orchestrator)

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

// startK8sController registers the local cluster in the DB and starts the Watch loop.
// Logs a warning and returns without error if no kubeconfig is available.
func startK8sController(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, hub *sse.Hub) {
	serverURL, err := k8s.ServerURL(cfg.KubeconfigPath)
	if err != nil {
		slog.Warn("k8s: no kubeconfig found, self-healing controller disabled", "error", err)
		return
	}

	clusterID, err := upsertCluster(ctx, pool, cfg.ClusterName, serverURL)
	if err != nil {
		slog.Error("k8s: register cluster", "error", err)
		return
	}

	ctrl, err := k8s.New(cfg.KubeconfigPath, clusterID, pool, hub)
	if err != nil {
		slog.Error("k8s: init controller", "error", err)
		return
	}

	go func() {
		if err := ctrl.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("k8s: controller exited", "error", err)
		}
	}()
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
func ensureTerraformBinary(ctx context.Context, cfg *config.Config) {
	installDir := filepath.Join(cfg.DataDir, "terraform", "bin")
	execPath, err := terraform.EnsureBinary(ctx, installDir)
	if err != nil {
		slog.Warn("terraform: binary unavailable, provisioning disabled", "error", err)
		return
	}
	slog.Info("terraform: binary resolved", "path", execPath)
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
