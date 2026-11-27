package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"crproxy/internal/id"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
)

type Access struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

type Claims struct {
	Issuer     string   `json:"iss"`
	Audience   string   `json:"aud"`
	Subject    string   `json:"sub"`
	IssuedAt   int64    `json:"iat"`
	NotBefore  int64    `json:"nbf"`
	ExpiresAt  int64    `json:"exp"`
	JTI        string   `json:"jti"`
	AuthSource string   `json:"auth_source"`
	Role       string   `json:"role,omitempty"`
	Access     []Access `json:"access"`
}

type Signer struct {
	mu       sync.RWMutex
	secret   []byte
	issuer   string
	audience string
	ttl      time.Duration
}

func NewSigner(secret []byte, issuer, audience string, ttl time.Duration) (*Signer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("jwt secret must be at least 32 bytes")
	}
	if ttl <= 0 || ttl > 15*time.Minute {
		return nil, fmt.Errorf("invalid token ttl")
	}
	return &Signer{secret: append([]byte(nil), secret...), issuer: issuer, audience: audience, ttl: ttl}, nil
}

func (s *Signer) TTL() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ttl
}

// Update changes the audience and token lifetime while preserving the signing
// secret. Changing the audience intentionally invalidates previously issued
// registry tokens.
func (s *Signer) Update(audience string, ttl time.Duration) error {
	if strings.TrimSpace(audience) == "" {
		return fmt.Errorf("token audience is required")
	}
	if ttl <= 0 || ttl > 15*time.Minute {
		return fmt.Errorf("invalid token ttl")
	}
	s.mu.Lock()
	s.audience = audience
	s.ttl = ttl
	s.mu.Unlock()
	return nil
}

func (s *Signer) Issue(subject, role, authSource string, access []Access) (string, Claims, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	claims := Claims{
		Issuer:     s.issuer,
		Audience:   s.audience,
		Subject:    subject,
		IssuedAt:   now.Unix(),
		NotBefore:  now.Add(-10 * time.Second).Unix(),
		ExpiresAt:  now.Add(s.ttl).Unix(),
		JTI:        id.New("", 12),
		AuthSource: authSource,
		Role:       role,
		Access:     access,
	}
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", Claims{}, err
	}
	headerPart := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsPart := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerPart + "." + claimsPart
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + signature, claims, nil
}

func (s *Signer) Verify(token string) (Claims, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalidToken
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil || header["alg"] != "HS256" {
		return Claims{}, ErrInvalidToken
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[2])) != 1 {
		return Claims{}, ErrInvalidToken
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return Claims{}, ErrInvalidToken
	}
	now := time.Now().UTC().Unix()
	if claims.Issuer != s.issuer || claims.Audience != s.audience {
		return Claims{}, ErrInvalidToken
	}
	if claims.ExpiresAt <= now {
		return Claims{}, ErrTokenExpired
	}
	if claims.NotBefore > now+30 {
		return Claims{}, ErrInvalidToken
	}
	if claims.IssuedAt > now+30 {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}
