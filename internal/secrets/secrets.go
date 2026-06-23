package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

const (
	kdfSalt      = "autodevops-vault-v1"
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	dekLen       = 32 // AES-256
)

// Vault implements envelope encryption. The KEK (key-encryption key) is derived
// from MASTER_KEY via argon2id and never touches the database; it only wraps the
// DEK (data-encryption key), a random 32-byte key persisted (wrapped) in
// vault_keys. The DEK is what actually encrypts/decrypts rows in `secrets`.
// Separating the two lets us rotate the DEK (re-encrypt all secrets, survives a
// restart with no env change) and rotate the KEK (re-wrap just the DEK) cheaply.
type Vault struct {
	db *pgxpool.Pool

	mu      sync.RWMutex
	kek     []byte
	dek     []byte
	version int
}

// New derives the KEK from masterKey and loads the active DEK from vault_keys,
// bootstrapping it (and migrating any legacy KEK-encrypted secrets onto it) on
// first run. A decrypt failure here means MASTER_KEY does not match the one the
// vault was created with — the server should fail loudly rather than proceed.
func New(ctx context.Context, masterKey string, db *pgxpool.Pool) (*Vault, error) {
	if masterKey == "" {
		return nil, fmt.Errorf("secrets: master key is required")
	}
	v := &Vault{db: db, kek: deriveKey(masterKey)}
	if err := v.loadOrBootstrap(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

// loadOrBootstrap reads the active DEK, or creates version 1 (and migrates
// pre-envelope secrets) if none exists yet.
func (v *Vault) loadOrBootstrap(ctx context.Context) error {
	var (
		version   int
		wrapped   []byte
		wrapNonce []byte
	)
	err := v.db.QueryRow(ctx,
		"SELECT version, wrapped_dek, wrap_nonce FROM vault_keys WHERE active",
	).Scan(&version, &wrapped, &wrapNonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return v.bootstrap(ctx)
	}
	if err != nil {
		return fmt.Errorf("secrets: load active key: %w", err)
	}
	dek, err := decrypt(v.kek, wrapped, wrapNonce)
	if err != nil {
		return fmt.Errorf("secrets: unwrap data key (MASTER_KEY likely wrong): %w", err)
	}
	v.dek = dek
	v.version = version
	return nil
}

// bootstrap generates the first DEK and, in one transaction, re-encrypts every
// existing secret (legacy rows are encrypted directly with the KEK) onto it.
func (v *Vault) bootstrap(ctx context.Context) error {
	dek := make([]byte, dekLen)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return fmt.Errorf("secrets: generate data key: %w", err)
	}
	wrapped, wrapNonce, err := encrypt(v.kek, dek)
	if err != nil {
		return fmt.Errorf("secrets: wrap data key: %w", err)
	}

	tx, err := v.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("secrets: bootstrap begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// Re-encrypt legacy secrets (KEK -> DEK). On a fresh DB this is a no-op.
	if err := reencryptAll(ctx, tx, v.kek, dek); err != nil {
		return fmt.Errorf("secrets: migrate legacy secrets (MASTER_KEY likely wrong): %w", err)
	}

	if _, err := tx.Exec(ctx,
		"INSERT INTO vault_keys (version, wrapped_dek, wrap_nonce, active) VALUES (1, $1, $2, true)",
		wrapped, wrapNonce,
	); err != nil {
		return fmt.Errorf("secrets: bootstrap insert key: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("secrets: bootstrap commit: %w", err)
	}

	v.dek = dek
	v.version = 1
	return nil
}

func (v *Vault) Put(ctx context.Context, name string, plaintext []byte) error {
	v.mu.RLock()
	dek := v.dek
	v.mu.RUnlock()

	ciphertext, nonce, err := encrypt(dek, plaintext)
	if err != nil {
		return fmt.Errorf("secrets: put %q: %w", name, err)
	}
	_, err = v.db.Exec(ctx, `
		INSERT INTO secrets (name, ciphertext, nonce)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE
			SET ciphertext = EXCLUDED.ciphertext,
			    nonce      = EXCLUDED.nonce,
			    updated_at = now()
	`, name, ciphertext, nonce)
	if err != nil {
		return fmt.Errorf("secrets: put %q: %w", name, err)
	}
	return nil
}

func (v *Vault) Get(ctx context.Context, name string) ([]byte, error) {
	var ciphertext, nonce []byte
	err := v.db.QueryRow(ctx,
		"SELECT ciphertext, nonce FROM secrets WHERE name = $1", name,
	).Scan(&ciphertext, &nonce)
	if err != nil {
		return nil, fmt.Errorf("secrets: get %q: %w", name, err)
	}
	v.mu.RLock()
	dek := v.dek
	v.mu.RUnlock()
	plaintext, err := decrypt(dek, ciphertext, nonce)
	if err != nil {
		return nil, fmt.Errorf("secrets: get %q: %w", name, err)
	}
	return plaintext, nil
}

// GetByID looks up and decrypts a secret by its UUID rather than its name —
// for callers that only hold a foreign-key reference (e.g. cloud_accounts.credentials_secret_id).
func (v *Vault) GetByID(ctx context.Context, id string) ([]byte, error) {
	var ciphertext, nonce []byte
	err := v.db.QueryRow(ctx,
		"SELECT ciphertext, nonce FROM secrets WHERE id = $1", id,
	).Scan(&ciphertext, &nonce)
	if err != nil {
		return nil, fmt.Errorf("secrets: get by id %q: %w", id, err)
	}
	v.mu.RLock()
	dek := v.dek
	v.mu.RUnlock()
	plaintext, err := decrypt(dek, ciphertext, nonce)
	if err != nil {
		return nil, fmt.Errorf("secrets: get by id %q: %w", id, err)
	}
	return plaintext, nil
}

func (v *Vault) Delete(ctx context.Context, name string) error {
	if _, err := v.db.Exec(ctx, "DELETE FROM secrets WHERE name = $1", name); err != nil {
		return fmt.Errorf("secrets: delete %q: %w", name, err)
	}
	return nil
}

// KeyStatus reports the current envelope-encryption state for the Secrets UI.
type KeyStatus struct {
	Version       int        `json:"version"`
	RotationCount int        `json:"rotation_count"` // total DEK versions ever created
	ActiveSince   time.Time  `json:"active_since"`
	LastRotatedAt *time.Time `json:"last_rotated_at,omitempty"`
	SecretCount   int        `json:"secret_count"`
}

func (v *Vault) KeyStatus(ctx context.Context) (KeyStatus, error) {
	v.mu.RLock()
	version := v.version
	v.mu.RUnlock()

	var st KeyStatus
	st.Version = version

	if err := v.db.QueryRow(ctx,
		"SELECT created_at FROM vault_keys WHERE version = $1", version,
	).Scan(&st.ActiveSince); err != nil {
		return KeyStatus{}, fmt.Errorf("secrets: key status active: %w", err)
	}
	if err := v.db.QueryRow(ctx,
		"SELECT count(*) FROM vault_keys",
	).Scan(&st.RotationCount); err != nil {
		return KeyStatus{}, fmt.Errorf("secrets: key status count: %w", err)
	}
	if err := v.db.QueryRow(ctx,
		"SELECT count(*) FROM secrets",
	).Scan(&st.SecretCount); err != nil {
		return KeyStatus{}, fmt.Errorf("secrets: key status secret count: %w", err)
	}
	if st.RotationCount > 1 {
		st.LastRotatedAt = &st.ActiveSince
	}
	return st, nil
}

// RotateDataKey generates a fresh DEK, re-encrypts every secret onto it, and
// persists the new wrapped DEK as a new active version — all atomically. The
// plaintext of every secret is preserved; only the stored ciphertext changes.
// It survives a restart because the new wrapped DEK lives in the DB (no env
// change needed). Returns the new version and the number of secrets re-encrypted.
func (v *Vault) RotateDataKey(ctx context.Context) (newVersion, reencrypted int, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	newDEK := make([]byte, dekLen)
	if _, err := io.ReadFull(rand.Reader, newDEK); err != nil {
		return 0, 0, fmt.Errorf("secrets: rotate: generate data key: %w", err)
	}
	wrapped, wrapNonce, err := encrypt(v.kek, newDEK)
	if err != nil {
		return 0, 0, fmt.Errorf("secrets: rotate: wrap data key: %w", err)
	}

	tx, err := v.db.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("secrets: rotate: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	count, err := reencryptAllCount(ctx, tx, v.dek, newDEK)
	if err != nil {
		return 0, 0, fmt.Errorf("secrets: rotate: re-encrypt: %w", err)
	}

	next := v.version + 1
	if _, err := tx.Exec(ctx, "UPDATE vault_keys SET active = false WHERE active"); err != nil {
		return 0, 0, fmt.Errorf("secrets: rotate: deactivate old key: %w", err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO vault_keys (version, wrapped_dek, wrap_nonce, active) VALUES ($1, $2, $3, true)",
		next, wrapped, wrapNonce,
	); err != nil {
		return 0, 0, fmt.Errorf("secrets: rotate: insert new key: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("secrets: rotate: commit: %w", err)
	}

	v.dek = newDEK
	v.version = next
	return next, count, nil
}

// RotateMasterKey re-wraps the existing DEK under a KEK derived from
// newMasterKey. Secrets are NOT re-encrypted (they live under the DEK), so this
// is cheap. The caller MUST update the MASTER_KEY env var to newMasterKey before
// the next restart, or the vault will fail to unwrap the DEK at boot.
func (v *Vault) RotateMasterKey(ctx context.Context, newMasterKey string) error {
	if newMasterKey == "" {
		return fmt.Errorf("secrets: rotate master key: new master key is required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	newKEK := deriveKey(newMasterKey)
	wrapped, wrapNonce, err := encrypt(newKEK, v.dek)
	if err != nil {
		return fmt.Errorf("secrets: rotate master key: wrap: %w", err)
	}
	if _, err := v.db.Exec(ctx,
		"UPDATE vault_keys SET wrapped_dek = $1, wrap_nonce = $2 WHERE version = $3",
		wrapped, wrapNonce, v.version,
	); err != nil {
		return fmt.Errorf("secrets: rotate master key: persist: %w", err)
	}
	v.kek = newKEK
	return nil
}

// reencryptAll re-encrypts every secret from oldKey to newKey within tx.
func reencryptAll(ctx context.Context, tx pgx.Tx, oldKey, newKey []byte) error {
	_, err := reencryptAllCount(ctx, tx, oldKey, newKey)
	return err
}

func reencryptAllCount(ctx context.Context, tx pgx.Tx, oldKey, newKey []byte) (int, error) {
	rows, err := tx.Query(ctx, "SELECT id, ciphertext, nonce FROM secrets")
	if err != nil {
		return 0, fmt.Errorf("select secrets: %w", err)
	}
	type rec struct {
		id        string
		plaintext []byte
	}
	var recs []rec
	for rows.Next() {
		var id string
		var ct, nonce []byte
		if err := rows.Scan(&id, &ct, &nonce); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan secret: %w", err)
		}
		pt, err := decrypt(oldKey, ct, nonce)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("decrypt secret %s: %w", id, err)
		}
		recs = append(recs, rec{id: id, plaintext: pt})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate secrets: %w", err)
	}
	rows.Close()

	for _, r := range recs {
		ct, nonce, err := encrypt(newKey, r.plaintext)
		if err != nil {
			return 0, fmt.Errorf("re-encrypt secret %s: %w", r.id, err)
		}
		if _, err := tx.Exec(ctx,
			"UPDATE secrets SET ciphertext = $1, nonce = $2 WHERE id = $3",
			ct, nonce, r.id,
		); err != nil {
			return 0, fmt.Errorf("update secret %s: %w", r.id, err)
		}
	}
	return len(recs), nil
}

func deriveKey(masterKey string) []byte {
	return argon2.IDKey(
		[]byte(masterKey),
		[]byte(kdfSalt),
		argonTime, argonMemory, argonThreads, argonKeyLen,
	)
}

func encrypt(key, plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("create gcm: %w", err)
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	return gcm.Seal(nil, nonce, plaintext, nil), nonce, nil
}

func decrypt(key, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}
