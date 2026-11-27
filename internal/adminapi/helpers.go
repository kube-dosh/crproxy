package adminapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kube-dosh/crproxy/internal/auth"
	"github.com/kube-dosh/crproxy/internal/id"
	"github.com/kube-dosh/crproxy/internal/store"
)

const maxJSONBody = 1 << 20

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type userResponse struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	Role               string     `json:"role"`
	Enabled            bool       `json:"enabled"`
	MustChangePassword bool       `json:"mustChangePassword"`
	CreatedAt          time.Time  `json:"createdAt"`
	LastLoginAt        *time.Time `json:"lastLoginAt,omitempty"`
}

type tokenResponse struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userId"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiErrorBody{Code: code, Message: message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func newRequestID() string {
	return id.New("req_", 12)
}

type requestIDKey struct{}

func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func randomToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) originAllowed(r *http.Request) bool {
	return h.requestInfo.OriginAllowed(r)
}

func toUserResponse(user store.User) userResponse {
	return userResponse{
		ID:                 user.ID,
		Username:           user.Username,
		Role:               user.Role,
		Enabled:            user.Enabled,
		MustChangePassword: user.MustChangePassword,
		CreatedAt:          time.Unix(user.CreatedAt, 0).UTC(),
		LastLoginAt:        user.LastLoginAt,
	}
}

func toTokenResponse(token store.APIToken) tokenResponse {
	return tokenResponse{
		ID:        token.ID,
		UserID:    token.UserID,
		Name:      token.Name,
		Prefix:    token.Prefix,
		Scopes:    append([]string{}, token.Scopes...),
		CreatedAt: token.CreatedAt,
		ExpiresAt: token.ExpiresAt,
		RevokedAt: token.RevokedAt,
	}
}

func parseLimit(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid limit")
	}
	if value > 500 {
		value = 500
	}
	return value, nil
}

func identityScopes(identity *auth.Identity) map[string]bool {
	result := make(map[string]bool, len(identity.Scopes))
	for _, scope := range identity.Scopes {
		result[scope] = true
	}
	return result
}
