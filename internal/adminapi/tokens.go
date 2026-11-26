package adminapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"crproxy/internal/auth"
	"crproxy/internal/store"
)

const maxActiveTokensPerUser = 20

func (h *Handler) handleTokenList(w http.ResponseWriter, r *http.Request, p *principal) {
	targetUser := p.Identity.UserID
	queryUser := strings.TrimSpace(r.URL.Query().Get("user_id"))
	all := r.URL.Query().Get("all") == "1"
	if all || (queryUser != "" && queryUser != p.Identity.UserID) {
		if !p.Identity.HasScope("admin") {
			writeAPIError(w, http.StatusForbidden, "forbidden", "only administrators can list other users' tokens")
			return
		}
		targetUser = queryUser
	}
	tokens, err := h.Store.ListAPITokens(r.Context(), targetUser)
	if err != nil {
		h.Logger.Error("list API tokens failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	result := make([]tokenResponse, 0, len(tokens))
	for _, token := range tokens {
		result = append(result, toTokenResponse(token))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": result})
}

func (h *Handler) handleTokenCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var body struct {
		Name          string   `json:"name"`
		Scopes        []string `json:"scopes"`
		ExpiresInDays int      `json:"expiresInDays"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 100 {
		writeAPIError(w, http.StatusBadRequest, "invalid_name", "token name must be between 1 and 100 characters")
		return
	}
	if len(body.Scopes) == 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_scopes", "at least one scope is required")
		return
	}
	if err := auth.ValidatePATScopes(body.Scopes); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_scopes", err.Error())
		return
	}
	granted := identityScopes(p.Identity)
	for _, scope := range body.Scopes {
		if !granted[scope] {
			writeAPIError(w, http.StatusForbidden, "scope_exceeds_role", "token scope exceeds your permissions")
			return
		}
	}
	if body.ExpiresInDays < 0 || body.ExpiresInDays > 3650 {
		writeAPIError(w, http.StatusBadRequest, "invalid_expiry", "expiresInDays must be between 0 and 3650")
		return
	}
	active, err := h.Store.CountActiveAPITokens(r.Context(), p.Identity.UserID, time.Now().UTC())
	if err != nil {
		h.Logger.Error("count active API tokens failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if active >= maxActiveTokensPerUser {
		writeAPIError(w, http.StatusConflict, "token_limit", "active token limit reached")
		return
	}
	var expiresAt *time.Time
	if body.ExpiresInDays > 0 {
		value := time.Now().UTC().Add(time.Duration(body.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &value
	}
	full, tokenID, prefix, secretHash, err := auth.GeneratePAT()
	if err != nil {
		h.Logger.Error("generate API token failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if _, err := h.Store.CreateAPIToken(r.Context(), tokenID, p.Identity.UserID, body.Name, prefix, secretHash, body.Scopes, expiresAt); err != nil {
		if isUniqueConstraint(err) {
			writeAPIError(w, http.StatusConflict, "token_name_exists", "an active token with this name already exists")
			return
		}
		h.Logger.Error("create API token failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	token, err := h.Store.APITokenByID(r.Context(), tokenID)
	if err != nil {
		h.Logger.Error("read created API token failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "auth.token.create", "token", tokenID, "success", "")
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":    full,
		"metadata": toTokenResponse(token),
	})
}

func (h *Handler) handleTokenRevoke(w http.ResponseWriter, r *http.Request, p *principal) {
	tokenID := r.PathValue("id")
	token, err := h.Store.APITokenByID(r.Context(), tokenID)
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "token_not_found", "token not found")
		return
	}
	if err != nil {
		h.Logger.Error("read API token failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if token.UserID != p.Identity.UserID && !p.Identity.HasScope("admin") {
		writeAPIError(w, http.StatusNotFound, "token_not_found", "token not found")
		return
	}
	if token.RevokedAt == nil {
		if err := h.Store.RevokeAPIToken(r.Context(), tokenID); err != nil && !errors.Is(err, store.ErrNotFound) {
			h.Logger.Error("revoke API token failed", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		h.recordAudit(r, "user", p.Identity.UserID, "auth.token.revoke", "token", tokenID, "success", "")
	}
	w.WriteHeader(http.StatusNoContent)
}
