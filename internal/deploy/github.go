package deploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"
)

const githubAPIBase = "https://api.github.com"

// parseGitHubRepo extracts owner/repo from a GitHub repo URL. Handles
// https://github.com/owner/repo(.git) and git@github.com:owner/repo(.git).
// ok is false for non-GitHub hosts.
func parseGitHubRepo(repoURL string) (owner, repo string, ok bool) {
	u := strings.TrimSpace(repoURL)
	if !strings.Contains(u, "github.com") {
		return "", "", false
	}
	// Normalize scp-style and https forms to "owner/repo".
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "github.com"); i >= 0 {
		u = u[i+len("github.com"):]
	}
	u = strings.TrimLeft(u, ":/")
	parts := strings.Split(u, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// githubSecretError distinguishes a permission/scope failure (actionable by the
// user) from a transport error, so the API layer can surface a clear message.
type githubSecretError struct {
	status int
	body   string
	op     string
}

func (e *githubSecretError) Error() string {
	return fmt.Sprintf("github: %s failed (HTTP %d): %s", e.op, e.status, e.body)
}

// githubSetSecret creates/updates a repository Actions secret. GitHub requires
// the value encrypted with the repo's public key via libsodium sealed box
// (nacl/box SealAnonymous) — the same scheme the gh CLI uses.
func githubSetSecret(ctx context.Context, client *http.Client, token, owner, repo, name, value string) error {
	keyID, pubKey, err := githubSecretsPublicKey(ctx, client, token, owner, repo)
	if err != nil {
		return err
	}
	encrypted, err := sealSecret(pubKey, value)
	if err != nil {
		return fmt.Errorf("github: encrypt secret %q: %w", name, err)
	}

	body, _ := json.Marshal(map[string]string{
		"encrypted_value": encrypted,
		"key_id":          keyID,
	})
	url := fmt.Sprintf("%s/repos/%s/%s/actions/secrets/%s", githubAPIBase, owner, repo, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("github: build set-secret request: %w", err)
	}
	githubHeaders(req, token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("github: set secret %q: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &githubSecretError{status: resp.StatusCode, body: strings.TrimSpace(string(b)), op: "set secret " + name}
	}
	return nil
}

func githubSecretsPublicKey(ctx context.Context, client *http.Client, token, owner, repo string) (keyID, key string, err error) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/secrets/public-key", githubAPIBase, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("github: build public-key request: %w", err)
	}
	githubHeaders(req, token)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("github: get public key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", "", &githubSecretError{status: resp.StatusCode, body: strings.TrimSpace(string(b)), op: "get public key"}
	}
	var out struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("github: decode public key: %w", err)
	}
	return out.KeyID, out.Key, nil
}

// sealSecret encrypts value for the repo's base64-encoded Curve25519 public key
// using a libsodium-compatible sealed box, returning base64 ciphertext.
func sealSecret(pubKeyB64, value string) (string, error) {
	pub, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return "", fmt.Errorf("decode public key: %w", err)
	}
	if len(pub) != 32 {
		return "", fmt.Errorf("unexpected public key length %d (want 32)", len(pub))
	}
	var pubKey [32]byte
	copy(pubKey[:], pub)
	enc, err := box.SealAnonymous(nil, []byte(value), &pubKey, rand.Reader)
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func githubHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if req.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
}

func githubClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}
