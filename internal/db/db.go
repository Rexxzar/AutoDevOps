package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("db: set dialect: %w", err)
	}
	if err := goose.UpContext(ctx, sqlDB, dir); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	return nil
}

func EnsureAdminUser(ctx context.Context, pool *pgxpool.Pool, email, passwordHash string) error {
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("db: count users: %w", err)
	}
	if count > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO users (email, password_hash) VALUES ($1, $2)",
		email, passwordHash,
	); err != nil {
		return fmt.Errorf("db: seed admin user: %w", err)
	}
	slog.Info("admin user seeded", "email", email)
	return nil
}
