package api

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGenerateSSHKeyPair(t *testing.T) {
	pub, priv, err := generateSSHKeyPair()
	if err != nil {
		t.Fatalf("generateSSHKeyPair: %v", err)
	}

	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Errorf("public key missing ssh-ed25519 prefix: %q", pub)
	}

	parsedPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}

	signer, err := ssh.ParsePrivateKey([]byte(priv))
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}

	if string(signer.PublicKey().Marshal()) != string(parsedPub.Marshal()) {
		t.Error("generated private key does not match returned public key")
	}
}

func TestGenerateSSHKeyPairUnique(t *testing.T) {
	pub1, priv1, err := generateSSHKeyPair()
	if err != nil {
		t.Fatalf("generateSSHKeyPair: %v", err)
	}
	pub2, priv2, err := generateSSHKeyPair()
	if err != nil {
		t.Fatalf("generateSSHKeyPair: %v", err)
	}
	if pub1 == pub2 || priv1 == priv2 {
		t.Error("expected distinct keypairs across calls")
	}
}
