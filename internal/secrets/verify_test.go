package secrets

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestVerifyExistingSecretsDecrypt is a READ-ONLY diagnostic: it connects to the
// real DB and confirms that every existing secret decrypts cleanly with the KEK
// derived from MASTER_KEY — i.e. the envelope bootstrap migration will succeed
// and no stored credential will be lost. It never writes. Opt-in:
//
//	AUTODEVOPS_VERIFY_VAULT=1 MASTER_KEY=... DATABASE_URL=... \
//	  go test ./internal/secrets/ -run TestVerifyExistingSecretsDecrypt -v
func TestVerifyExistingSecretsDecrypt(t *testing.T) {
	if os.Getenv("AUTODEVOPS_VERIFY_VAULT") == "" {
		t.Skip("set AUTODEVOPS_VERIFY_VAULT=1 (plus MASTER_KEY, DATABASE_URL) to verify the live vault")
	}
	master := os.Getenv("MASTER_KEY")
	if master == "" {
		t.Fatal("MASTER_KEY must be set to verify")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	kek := deriveKey(master)

	rows, err := pool.Query(ctx, "SELECT name, ciphertext, nonce FROM secrets ORDER BY name")
	if err != nil {
		t.Fatalf("query secrets: %v", err)
	}
	defer rows.Close()

	var total, ok int
	var failed []string
	for rows.Next() {
		var name string
		var ct, nonce []byte
		if err := rows.Scan(&name, &ct, &nonce); err != nil {
			t.Fatalf("scan: %v", err)
		}
		total++
		if _, derr := decrypt(kek, ct, nonce); derr != nil {
			failed = append(failed, name)
		} else {
			ok++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	t.Logf("vault verification: %d/%d secrets decrypt cleanly with the current MASTER_KEY", ok, total)
	if len(failed) > 0 {
		sort.Strings(failed)
		for _, n := range failed {
			t.Logf("  WILL NOT MIGRATE (decrypt failed): %s", n)
		}
		t.Fatalf("%d secret(s) do not decrypt with this MASTER_KEY — fix before restarting the server", len(failed))
	}
}
