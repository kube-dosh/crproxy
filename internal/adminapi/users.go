package adminapi

import (
	"errors"
	"net/http"

	"github.com/kube-dosh/crproxy/internal/auth"
	"github.com/kube-dosh/crproxy/internal/store"
	sqlite "modernc.org/sqlite"
)

func (h *Handler) handleUserList(w http.ResponseWriter, r *http.Request, _ *principal) {
	users, err := h.Store.ListUsers(r.Context())
	if err != nil {
		h.Logger.Error("list users failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	result := make([]userResponse, 0, len(users))
	for _, user := range users {
		result = append(result, toUserResponse(user))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": result})
}

func (h *Handler) handleUserCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	if err := auth.ValidateUsername(body.Username); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_username", err.Error())
		return
	}
	if err := auth.ValidatePassword(body.Username, body.Password); err != nil {
		writeAPIError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	if err := auth.ValidateRole(body.Role); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_role", err.Error())
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		h.Logger.Error("hash password failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	user, err := h.Store.CreateUserWithPasswordChange(r.Context(), body.Username, hash, body.Role, true)
	if err != nil {
		if isUniqueConstraint(err) {
			writeAPIError(w, http.StatusConflict, "username_exists", "username already exists")
			return
		}
		h.Logger.Error("create user failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "user.create", "user", user.ID, "success", "")
	writeJSON(w, http.StatusCreated, toUserResponse(user))
}

func (h *Handler) handleUserUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	userID := r.PathValue("id")
	user, err := h.Store.UserByID(r.Context(), userID)
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
		return
	}
	if err != nil {
		h.Logger.Error("read user failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	var body struct {
		Role    *string `json:"role"`
		Enabled *bool   `json:"enabled"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	if body.Role == nil && body.Enabled == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "at least one field must be provided")
		return
	}
	role := user.Role
	enabled := user.Enabled
	if body.Role != nil {
		if err := auth.ValidateRole(*body.Role); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_role", err.Error())
			return
		}
		role = *body.Role
	}
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if userID == p.Identity.UserID && ((body.Role != nil && role != user.Role) || (body.Enabled != nil && !enabled)) {
		writeAPIError(w, http.StatusConflict, "self_modification_forbidden", "you cannot disable or change your own role")
		return
	}
	if err := h.Store.UpdateUser(r.Context(), userID, role, enabled); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeAPIError(w, http.StatusConflict, "last_admin", "the last active administrator cannot be disabled or demoted")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
			return
		}
		h.Logger.Error("update user failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	updated, _ := h.Store.UserByID(r.Context(), userID)
	h.recordAudit(r, "user", p.Identity.UserID, "user.update", "user", userID, "success", "")
	writeJSON(w, http.StatusOK, toUserResponse(updated))
}

func (h *Handler) handleUserDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	userID := r.PathValue("id")
	if userID == p.Identity.UserID {
		writeAPIError(w, http.StatusConflict, "self_modification_forbidden", "you cannot delete your own account")
		return
	}
	if err := h.Store.DeleteUser(r.Context(), userID); err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			writeAPIError(w, http.StatusConflict, "last_admin", "the last active administrator cannot be deleted")
		case errors.Is(err, store.ErrNotFound):
			writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
		default:
			h.Logger.Error("delete user failed", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "user.delete", "user", userID, "success", "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleUserPasswordReset(w http.ResponseWriter, r *http.Request, p *principal) {
	userID := r.PathValue("id")
	user, err := h.Store.UserByID(r.Context(), userID)
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "user_not_found", "user not found")
		return
	}
	if err != nil {
		h.Logger.Error("read user failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	if err := auth.ValidatePassword(user.Username, body.Password); err != nil {
		writeAPIError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		h.Logger.Error("hash password failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if err := h.Store.SetUserPassword(r.Context(), userID, hash, true); err != nil {
		h.Logger.Error("reset user password failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if err := h.Store.DeleteUserSessions(r.Context(), userID, ""); err != nil {
		h.Logger.Error("delete user sessions after password reset failed", "error", err)
	}
	h.recordAudit(r, "user", p.Identity.UserID, "user.password.reset", "user", userID, "success", "")
	w.WriteHeader(http.StatusNoContent)
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		return code == 1555 || code == 2067
	}
	return false
}
