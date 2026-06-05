package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

const (
	kdfSalt      = "autodevops-vault-v1"
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
)

type Vault struct {
	key []byte
	db  *pgxpool.Pool
}

func New(masterKey string, db *pgxpool.Pool) (*Vault, error) {
	if masterKey == "" {
		return nil, fmt.Errorf("secrets: master key is required")
	}
	return &Vault{key: deriveKey(masterKey), db: db}, nil
}

func (v *Vault) Put(ctx context.Context, name string, plaintext []byte) error {
	ciphertext, nonce, err := encrypt(v.key, plaintext)
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
	plaintext, err := decrypt(v.key, ciphertext, nonce)
	if err != nil {
		return nil, fmt.Errorf("secrets: get %q: %w", name, err)
	}
	return plaintext, nil
}

func (v *Vault) Delete(ctx context.Context, name string) error {
	if _, err := v.db.Exec(ctx, "DELETE FROM secrets WHERE name = $1", name); err != nil {
		return fmt.Errorf("secrets: delete %q: %w", name, err)
	}
	return nil
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
