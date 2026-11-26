package secretbox

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyringGenerateVersionPersistsOldKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta", "master.key")
	keyring, err := LoadOrCreateKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	if keyring.CurrentVersion() != 1 {
		t.Fatalf("unexpected initial version %d", keyring.CurrentVersion())
	}
	oldSealer, err := keyring.Sealer(1)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := oldSealer.Seal([]byte("legacy"), "aad:1")
	if err != nil {
		t.Fatal(err)
	}
	next, err := keyring.GenerateVersion()
	if err != nil {
		t.Fatal(err)
	}
	if next != 2 {
		t.Fatalf("unexpected generated version %d", next)
	}
	reloaded, err := LoadOrCreateKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CurrentVersion() != 2 {
		t.Fatalf("unexpected reloaded version %d", reloaded.CurrentVersion())
	}
	oldSealer, err = reloaded.Sealer(1)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := oldSealer.Open(ciphertext, "aad:1")
	if err != nil || string(plaintext) != "legacy" {
		t.Fatalf("old key no longer decrypts: %q %v", plaintext, err)
	}
	newSealer, err := reloaded.Sealer(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newSealer.Open(ciphertext, "aad:1"); err == nil {
		t.Fatal("new key must not decrypt old ciphertext")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected keyring permissions %o", info.Mode().Perm())
	}
}

func TestKeyringMigratesLegacyRawKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	raw := bytes.Repeat([]byte{5}, KeySize)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	keyring, err := LoadOrCreateKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := keyring.Sealer(1)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := sealer.Seal([]byte("value"), "aad")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := sealer.Open(ciphertext, "aad")
	if err != nil || string(plaintext) != "value" {
		t.Fatalf("legacy key did not migrate: %q %v", plaintext, err)
	}
	legacy, err := os.ReadFile(path + ".legacy")
	if err != nil || !bytes.Equal(legacy, raw) {
		t.Fatalf("legacy key backup missing or changed: %v", err)
	}
}
