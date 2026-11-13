package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestKeygenRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "signing.key")
	pub, err := keygen(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 64 {
		t.Fatalf("chiave pubblica di %d caratteri, attesi 64", len(pub))
	}
	if _, err := hex.DecodeString(pub); err != nil {
		t.Fatalf("chiave pubblica non hex: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("permessi %o, attesi 600", fi.Mode().Perm())
	}
	before, _ := os.ReadFile(path)
	if _, err := keygen(path); err == nil {
		t.Fatal("la seconda keygen doveva fallire")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("la chiave è stata sovrascritta")
	}
}

func TestSignVerifies(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signing.key")
	pubHex, err := keygen(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	sums := []byte("abc  encluca-0.2.0-linux-amd64.tar.gz\n")
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(sumsPath, sums, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := signFile(keyPath, sumsPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sumsPath + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(raw)))
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := hex.DecodeString(pubHex)
	if !ed25519.Verify(ed25519.PublicKey(pub), sums, sig) {
		t.Fatal("firma non valida")
	}
}
