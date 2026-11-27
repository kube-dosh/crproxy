package adminapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kube-dosh/crproxy/internal/auth"
)

type principal struct {
	Identity      *auth.Identity
	SessionIDHash string
	CSRFToken     string
	MustChange    bool
}

type apiHandler func(http.ResponseWriter, *http.Request, *principal)

type sessionPrincipalContextKey struct{}

type sessionResponse struct {
	User      userResponse `json:"user"`
	CSRFToken string       `json:"csrfToken,omitempty"`
	AuthMode  string       `json:"authMode"`
}

func (h *Handler) authenticated(required string, next apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := r.Context().Value(sessionPrincipalContextKey{}).(*principal)
		if !ok || p == nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		if !scopeAllows(p.Identity, required) {
			writeAPIError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
			return
		}
		next(w, r, p)
	}
}

func (p *principal) SessionIDHashCSRF() string {
	return hashToken(p.CSRFToken)
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func passwordChangeAllowedPath(path string) bool {
	switch path {
	case "/api/v1/auth/session", "/api/v1/auth/password", "/api/v1/auth/logout":
		return true
	default:
		return false
	}
}

func scopeAllows(identity *auth.Identity, required string) bool {
	if identity == nil {
		return false
	}
	switch required {
	case "":
		return true
	case "manage":
		return identity.HasAnyScope("manage", "admin")
	case "admin":
		return identity.HasScope("admin")
	default:
		return false
	}
}

func (h *Handler) authenticateSession(r *http.Request) (*principal, error) {
	sessionID, err := h.sessionCookieValue(r)
	if err != nil {
		return nil, auth.ErrUnauthenticated
	}
	session, err := h.Store.SessionByIDHash(r.Context(), hashToken(sessionID), time.Now().UTC())
	if err != nil {
		return nil, auth.ErrUnauthenticated
	}
	now := time.Now().UTC()
	if now.Sub(session.LastSeenAt) >= time.Minute {
		_, idleTimeout := h.sessionPolicy()
		_ = h.Store.TouchSession(r.Context(), session.IDHash, now, now.Add(idleTimeout))
	}
	identity := &auth.Identity{
		UserID:     session.UserID,
		Username:   session.Username,
		Role:       session.Role,
		Scopes:     auth.RoleScopes(session.Role),
		AuthSource: "session",
	}
	return &principal{
		Identity:      identity,
		SessionIDHash: session.IDHash,
		CSRFToken:     h.deriveCSRF(sessionID),
		MustChange:    session.MustChange,
	}, nil
}

func (h *Handler) deriveCSRF(sessionID string) string {
	mac := hmac.New(sha256.New, h.csrfSecret)
	_, _ = mac.Write([]byte("crproxy:csrf:v1:"))
	_, _ = mac.Write([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h *Handler) sessionCookieValue(r *http.Request) (string, error) {
	cookie, err := r.Cookie(h.sessionCookieName())
	if err != nil || cookie.Value == "" {
		return "", errors.New("session cookie missing")
	}
	return cookie.Value, nil
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, sessionID string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.sessionCookieName(),
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.sessionCookieName(),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
	})
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !h.originAllowed(r) {
		writeAPIError(w, http.StatusForbidden, "origin_invalid", "request origin is not allowed")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	username := strings.TrimSpace(body.Username)
	limitKey := h.requestInfo.ClientIP(r) + "|" + strings.ToLower(username)
	if allowed, retryAfter := h.limiter.Allow(limitKey); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		h.recordAudit(r, "anonymous", "", "auth.login.failure", "user", username, "denied", "login rate limited")
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	user, err := h.Store.UserByUsername(r.Context(), username)
	if err != nil {
		_ = auth.VerifyPassword(h.dummyPasswordHash, body.Password)
		h.recordAudit(r, "anonymous", "", "auth.login.failure", "user", username, "failure", "invalid credentials")
		h.limiter.Failure(limitKey)
		writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect")
		return
	}
	if !user.Enabled || !auth.VerifyPassword(user.PasswordHash, body.Password) {
		h.recordAudit(r, "anonymous", "", "auth.login.failure", "user", username, "failure", "invalid credentials")
		h.limiter.Failure(limitKey)
		writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect")
		return
	}
	h.limiter.Success(limitKey)
	sessionID, err := randomToken(32)
	if err != nil {
		h.Logger.Error("generate session id failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	now := time.Now().UTC()
	sessionTTL, idleTimeout := h.sessionPolicy()
	expiresAt := now.Add(sessionTTL)
	idleExpiresAt := now.Add(idleTimeout)
	csrfToken := h.deriveCSRF(sessionID)
	if err := h.Store.CreateSession(r.Context(), hashToken(sessionID), user.ID, hashToken(csrfToken), expiresAt, idleExpiresAt, h.requestInfo.ClientIP(r), r.UserAgent()); err != nil {
		h.Logger.Error("create session failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	_ = h.Store.TouchUserLogin(r.Context(), user.ID)
	h.setSessionCookie(w, sessionID, expiresAt)
	h.recordAudit(r, "user", user.ID, "auth.login.success", "user", user.ID, "success", "")
	writeJSON(w, http.StatusOK, sessionResponse{
		User:      toUserResponse(user),
		CSRFToken: csrfToken,
		AuthMode:  "session",
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := h.Store.DeleteSession(r.Context(), p.SessionIDHash); err != nil {
		h.Logger.Error("delete session failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.clearSessionCookie(w)
	h.recordAudit(r, "user", p.Identity.UserID, "auth.logout", "user", p.Identity.UserID, "success", "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request, p *principal) {
	user, err := h.Store.UserByID(r.Context(), p.Identity.UserID)
	if err != nil {
		h.clearSessionCookie(w)
		writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		User:      toUserResponse(user),
		CSRFToken: p.CSRFToken,
		AuthMode:  "session",
	})
}

func (h *Handler) handlePasswordChange(w http.ResponseWriter, r *http.Request, p *principal) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	user, err := h.Store.UserByID(r.Context(), p.Identity.UserID)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, body.CurrentPassword) {
		h.recordAudit(r, "user", user.ID, "auth.password.change", "user", user.ID, "failure", "current password is incorrect")
		writeAPIError(w, http.StatusForbidden, "invalid_password", "current password is incorrect")
		return
	}
	if err := auth.ValidatePassword(user.Username, body.NewPassword); err != nil {
		writeAPIError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		h.Logger.Error("hash password failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if err := h.Store.SetUserPassword(r.Context(), user.ID, hash, false); err != nil {
		h.Logger.Error("update password failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if err := h.Store.DeleteUserSessions(r.Context(), user.ID, p.SessionIDHash); err != nil {
		h.Logger.Error("delete sessions after password change failed", "error", err)
	}
	h.recordAudit(r, "user", user.ID, "auth.password.change", "user", user.ID, "success", "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleSystemInfo(w http.ResponseWriter, r *http.Request, _ *principal) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":          h.version,
		"startedAt":        h.startedAt,
		"database":         "sqlite",
		"journal":          "delete",
		"publicURL":        h.publicURL,
		"managementURL":    h.publicURL,
		"registryURL":      h.registryURL,
		"planeMode":        h.deploymentMode,
		"registryListen":   h.registryListen,
		"managementListen": h.adminListen,
		"proxyPathPrefix":  h.proxyPrefix,
		"allowAnonymous":   h.anonymousAllowed(),
		"anonymousActions": h.anonymousActions(),
		"proxyDisabled":    h.proxyDisabled(),
		"metricsPath":      "/metrics",
		"ociSpec":          "1.1",
	})
}
