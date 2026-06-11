package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// generateSSHKeyPair creates a new ed25519 SSH keypair for users who don't
// have one of their own. The private key is returned to the caller exactly
// once (handleGenerateSSHKey) and is never persisted — same "relay, don't
// generate" boundary as AzureVMParams.SSHPublicKey, which only ever sees the
// public half.
func generateSSHKeyPair() (publicKey string, privateKeyPEM string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("ssh: generate ed25519 key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("ssh: derive public key: %w", err)
	}
	authorizedKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))

	block, err := ssh.MarshalPrivateKey(priv, "autodevops-generated")
	if err != nil {
		return "", "", fmt.Errorf("ssh: marshal private key: %w", err)
	}

	return authorizedKey + " autodevops-generated", string(pem.EncodeToMemory(block)), nil
}
