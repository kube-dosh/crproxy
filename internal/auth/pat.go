package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"crproxy/internal/id"
)

func GeneratePAT() (full, tokenID, prefix, secretHash string, err error) {
	tokenID = id.New("", 6)
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	full = "crp_" + tokenID + "_" + secret
	prefix = "crp_" + tokenID[:6] + "..."
	secretHash = HashPATSecret(secret)
	return
}

func ParsePAT(full string) (tokenID, secret string, ok bool) {
	if !strings.HasPrefix(full, "crp_") {
		return "", "", false
	}
	parts := strings.SplitN(full, "_", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func HashPATSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func VerifyPATSecret(expectedHash, secret string) bool {
	actual := HashPATSecret(secret)
	return subtle.ConstantTimeCompare([]byte(expectedHash), []byte(actual)) == 1
}

func ValidatePATScopes(scopes []string) error {
	allowed := map[string]bool{"pull": true, "push": true, "delete": true, "manage": true, "admin": true}
	for _, scope := range scopes {
		if !allowed[scope] {
			return fmt.Errorf("invalid scope %q", scope)
		}
	}
	return nil
}
