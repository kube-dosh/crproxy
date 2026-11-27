package registry

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"crproxy/internal/auth"
)

type TokenHandler struct {
	Auth   *auth.Authenticator
	Logger *slog.Logger
}

func (h *TokenHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Logger == nil {
		h.Logger = slog.Default()
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, "GET")
		return
	}
	identity, err := h.Auth.AuthenticateBasicOrPAT(r.Context(), r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", h.Auth.BasicChallenge())
		writeRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials", nil)
		return
	}
	service := r.URL.Query().Get("service")
	if service != "" && service != h.Auth.ServiceName() {
		writeRegistryError(w, http.StatusBadRequest, "UNAUTHORIZED", "invalid token service", nil)
		return
	}
	if account := r.URL.Query().Get("account"); account != "" && account != identity.Username {
		writeRegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "account does not match credentials", nil)
		return
	}
	scopes, err := parseTokenScopes(r.URL.Query()["scope"])
	if err != nil {
		writeRegistryError(w, http.StatusBadRequest, "UNAUTHORIZED", err.Error(), nil)
		return
	}
	token, claims, err := h.Auth.IssueToken(identity, scopes)
	if errors.Is(err, auth.ErrForbidden) {
		writeRegistryError(w, http.StatusForbidden, "DENIED", "requested scope is not permitted", nil)
		return
	}
	if err != nil {
		h.Logger.Error("issue registry token failed", "error", err)
		writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "internal server error", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = writeJSON(w, map[string]any{
		"token":        token,
		"access_token": token,
		"expires_in":   int64(time.Until(time.Unix(claims.ExpiresAt, 0).UTC()).Seconds()),
		"issued_at":    time.Unix(claims.IssuedAt, 0).UTC().Format(time.RFC3339),
	})
}

func parseTokenScopes(values []string) ([]auth.ScopeRequest, error) {
	var result []auth.ScopeRequest
	for _, value := range values {
		for _, raw := range strings.Fields(value) {
			if raw == "" {
				continue
			}
			parts := strings.SplitN(raw, ":", 3)
			if len(parts) != 3 || parts[0] != "repository" || parts[1] == "" || parts[2] == "" {
				return nil, errors.New("invalid token scope")
			}
			var actions []string
			for _, action := range strings.Split(parts[2], ",") {
				action = strings.TrimSpace(action)
				switch action {
				case "pull", "push", "delete":
					actions = append(actions, action)
				default:
					return nil, errors.New("unsupported token scope action")
				}
			}
			if len(actions) == 0 {
				return nil, errors.New("empty token scope action")
			}
			result = append(result, auth.ScopeRequest{Repository: parts[1], Actions: actions})
		}
	}
	return result, nil
}
