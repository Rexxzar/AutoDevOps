package deploy

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

func TestParseGitHubRepo(t *testing.T) {
	cases := []struct {
		url               string
		wantOwner, wantRepo string
		wantOK            bool
	}{
		{"https://github.com/Rexxzar/AutoDevOps", "Rexxzar", "AutoDevOps", true},
		{"https://github.com/Rexxzar/AutoDevOps.git", "Rexxzar", "AutoDevOps", true},
		{"git@github.com:Rexxzar/AutoDevOps.git", "Rexxzar", "AutoDevOps", true},
		{"https://github.com/owner/repo/", "owner", "repo", true},
		{"https://gitlab.com/owner/repo", "", "", false},
		{"https://github.com/onlyowner", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := parseGitHubRepo(c.url)
		if ok != c.wantOK || owner != c.wantOwner || repo != c.wantRepo {
			t.Errorf("parseGitHubRepo(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.url, owner, repo, ok, c.wantOwner, c.wantRepo, c.wantOK)
		}
	}
}

func TestSealSecret(t *testing.T) {
	// Generate a recipient keypair; encrypt with sealSecret; decrypt with the
	// private half to confirm the sealed box round-trips (GitHub-compatible).
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub[:])

	plaintext := "super-secret-webhook-value"
	sealedB64, err := sealSecret(pubB64, plaintext)
	if err != nil {
		t.Fatalf("sealSecret: %v", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(sealedB64)
	if err != nil {
		t.Fatalf("decode sealed: %v", err)
	}
	opened, ok := box.OpenAnonymous(nil, sealed, pub, priv)
	if !ok {
		t.Fatal("OpenAnonymous failed")
	}
	if string(opened) != plaintext {
		t.Fatalf("round-trip mismatch: got %q want %q", opened, plaintext)
	}

	if _, err := sealSecret("not-base64!!", "x"); err == nil {
		t.Error("expected error for invalid base64 public key")
	}
	if _, err := sealSecret(base64.StdEncoding.EncodeToString([]byte("short")), "x"); err == nil {
		t.Error("expected error for wrong-length public key")
	}
}

func TestSanitizeWorkflowName(t *testing.T) {
	cases := map[string]string{
		"my-app":      "my-app",
		"My App":      "my-app",
		"ShopHub_API": "shophub-api",
		"  spaced  ":  "spaced",
		"!!!":         "app",
		"":            "app",
	}
	for in, want := range cases {
		if got := sanitizeWorkflowName(in); got != want {
			t.Errorf("sanitizeWorkflowName(%q) = %q, want %q", in, got, want)
		}
	}
}
