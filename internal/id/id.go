package id

import (
	"crypto/rand"
	"encoding/hex"
)

func New(prefix string, bytes int) string {
	if bytes <= 0 {
		bytes = 16
	}
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(raw)
}
