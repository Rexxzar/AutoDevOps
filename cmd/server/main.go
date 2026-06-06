package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Rexxzar/autodevops/internal/api"
	"github.com/Rexxzar/autodevops/internal/auth"
	"github.com/Rexxzar/autodevops/internal/db"
	"github.com/Rexxzar/autodevops/internal/k8s"
	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
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

	handler := api.NewServer(pool, a, hub, vault)

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
