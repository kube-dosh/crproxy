package secretbox

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSealOpenAndAAD(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	sealer, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	first, err := sealer.Seal([]byte("secret"), "upstream:a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealer.Seal([]byte("secret"), "upstream:a")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("nonce must not be reused")
	}
	plaintext, err := sealer.Open(first, "upstream:a")
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("open failed: %q %v", plaintext, err)
	}
	if _, err := sealer.Open(first, "upstream:b"); err == nil {
		t.Fatal("wrong additional data must fail authentication")
	}
}

func TestLoadOrCreateKeyPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta", "master.key")
	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(first) != KeySize {
		t.Fatal("master key must be stable and 32 bytes")
	}
}
