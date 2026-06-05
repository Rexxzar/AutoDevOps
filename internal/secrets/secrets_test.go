package secrets

import (
	"bytes"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	plaintext := []byte("AKIAIOSFODNN7EXAMPLE/wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")

	ciphertext, nonce, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext must not equal plaintext")
	}

	got, err := decrypt(key, ciphertext, nonce)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip failed: got %q, want %q", got, plaintext)
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	plaintext := []byte("sensitive-cloud-credential")

	ciphertext, nonce, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	ciphertext[0] ^= 0xff

	if _, err := decrypt(key, ciphertext, nonce); err == nil {
		t.Fatal("expected error for tampered ciphertext, got nil")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key1 := bytes.Repeat([]byte{0x01}, 32)
	key2 := bytes.Repeat([]byte{0x02}, 32)

	ciphertext, nonce, err := encrypt(key1, []byte("secret"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if _, err := decrypt(key2, ciphertext, nonce); err == nil {
		t.Fatal("expected error decrypting with wrong key, got nil")
	}
}

func TestUniqueNonces(t *testing.T) {
	key := make([]byte, 32)
	plaintext := []byte("same data")

	_, nonce1, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt 1: %v", err)
	}
	_, nonce2, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt 2: %v", err)
	}

	if bytes.Equal(nonce1, nonce2) {
		t.Fatal("nonces must be unique per encryption")
	}
}

func TestDeriveKeyConsistency(t *testing.T) {
	k1 := deriveKey("my-master-password")
	k2 := deriveKey("my-master-password")

	if !bytes.Equal(k1, k2) {
		t.Fatal("same master key must always derive the same encryption key")
	}

	k3 := deriveKey("different-password")
	if bytes.Equal(k1, k3) {
		t.Fatal("different master keys must produce different derived keys")
	}
}
