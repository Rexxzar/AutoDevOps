package secrets

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEnvelopeWrapUnwrap proves the core envelope property at the crypto level
// (no DB): a random DEK wrapped by the KEK round-trips, and the wrong KEK fails.
func TestEnvelopeWrapUnwrap(t *testing.T) {
	kek := deriveKey("master-key-one")
	dek := bytes.Repeat([]byte{0xAB}, dekLen)

	wrapped, nonce, err := encrypt(kek, dek)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if bytes.Equal(wrapped, dek) {
		t.Fatal("wrapped dek must not equal plaintext dek")
	}

	got, err := decrypt(kek, wrapped, nonce)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("unwrapped dek mismatch")
	}

	wrongKEK := deriveKey("master-key-two")
	if _, err := decrypt(wrongKEK, wrapped, nonce); err == nil {
		t.Fatal("expected unwrap with wrong KEK to fail")
	}
}

// TestVaultEnvelopeRoundTrip exercises the full DB-backed flow: bootstrap, put,
// get, data-key rotation (plaintext preserved, version bumps), and master-key
// rotation (DEK re-wrapped, secrets still readable). It runs inside a throwaway
// schema so it never touches the real vault/secrets of the dev DB. Skips if no
// DB is reachable.
func TestVaultEnvelopeRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool, schema := testSchemaPool(t)

	const name = "envelope-test-secret"
	master := "envelope-test-master-key"
	v, err := New(ctx, master, pool)
	if err != nil {
		t.Fatalf("vault init in schema %s: %v", schema, err)
	}

	plaintext := []byte("super-secret-value")
	if err := v.Put(ctx, name, plaintext); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got, err := v.Get(ctx, name); err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("get: got %q err %v", got, err)
	}

	// Data-key rotation: plaintext unchanged, version increments.
	beforeVersion := v.version
	newVersion, count, err := v.RotateDataKey(ctx)
	if err != nil {
		t.Fatalf("rotate data key: %v", err)
	}
	if newVersion != beforeVersion+1 {
		t.Fatalf("expected version %d, got %d", beforeVersion+1, newVersion)
	}
	if count < 1 {
		t.Fatalf("expected >=1 secret re-encrypted, got %d", count)
	}
	if got, err := v.Get(ctx, name); err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("get after data-key rotation: got %q err %v", got, err)
	}

	// A fresh Vault with the same master key must unwrap the rotated DEK.
	v2, err := New(ctx, master, pool)
	if err != nil {
		t.Fatalf("reopen after rotation: %v", err)
	}
	if got, err := v2.Get(ctx, name); err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("get from reopened vault: got %q err %v", got, err)
	}

	// Master-key rotation: re-wrap DEK; secrets still readable, and a fresh Vault
	// with the NEW master key reads them while the OLD one no longer can.
	newMaster := "envelope-test-master-key-rotated"
	if err := v2.RotateMasterKey(ctx, newMaster); err != nil {
		t.Fatalf("rotate master key: %v", err)
	}
	if got, err := v2.Get(ctx, name); err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("get after master-key rotation: got %q err %v", got, err)
	}
	if _, err := New(ctx, newMaster, pool); err != nil {
		t.Fatalf("reopen with new master key: %v", err)
	}
	if _, err := New(ctx, master, pool); err == nil {
		t.Fatal("expected reopen with stale master key to fail")
	}
}

// testSchemaPool returns a pool whose search_path points at a unique throwaway
// schema (containing the secrets + vault_keys tables this package needs), torn
// down on cleanup. Hermetic — never mutates the dev DB's real vault.
func testSchemaPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable"
	}
	schema := fmt.Sprintf("vault_test_%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Skipf("parse db url: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("cannot create postgres pool for %s: %v", url, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres not reachable at %s: %v", url, err)
	}

	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schema); err != nil {
		pool.Close()
		t.Skipf("create test schema: %v", err)
	}
	// Minimal tables the vault touches (subset of migrations 001 + 030).
	ddl := []string{
		`CREATE TABLE secrets (
			id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			name       TEXT        NOT NULL UNIQUE,
			ciphertext BYTEA       NOT NULL,
			nonce      BYTEA       NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE vault_keys (
			id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			version     INTEGER     NOT NULL UNIQUE,
			wrapped_dek BYTEA       NOT NULL,
			wrap_nonce  BYTEA       NOT NULL,
			active      BOOLEAN     NOT NULL DEFAULT FALSE,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE UNIQUE INDEX vault_keys_one_active ON vault_keys (active) WHERE active`,
	}
	for _, q := range ddl {
		if _, err := pool.Exec(ctx, q); err != nil {
			pool.Close()
			t.Fatalf("create test tables: %v", err)
		}
	}

	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_, _ = pool.Exec(dctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		pool.Close()
	})
	return pool, schema
}
