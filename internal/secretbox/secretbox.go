package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const KeySize = 32

type Sealer struct {
	aead cipher.AEAD
}

func New(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(plaintext []byte, additionalData string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plaintext, []byte(additionalData)), nil
}

func (s *Sealer) Open(ciphertext []byte, additionalData string) ([]byte, error) {
	if len(ciphertext) < s.aead.NonceSize()+s.aead.Overhead() {
		return nil, errors.New("encrypted payload is too short")
	}
	nonce := ciphertext[:s.aead.NonceSize()]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext[s.aead.NonceSize():], []byte(additionalData))
	if err != nil {
		return nil, errors.New("encrypted payload authentication failed")
	}
	return plaintext, nil
}

func LoadOrCreateKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("master key path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		if len(raw) != KeySize {
			return nil, fmt.Errorf("master key file %s must contain exactly %d bytes", path, KeySize)
		}
		return raw, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return key, nil
}
