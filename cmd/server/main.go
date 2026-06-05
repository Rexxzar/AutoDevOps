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
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/Rexxzar/autodevops/pkg/config"
)

func main() {
	cfg := config.Load()

	if cfg.JWTSecret == "" {
		slog.Error("JWT_SECRET is required")
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
	handler := api.NewServer(pool, a, hub)

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
