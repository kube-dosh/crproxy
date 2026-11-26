package adminapi

import (
	"errors"
	"net/http"
	"time"

	"crproxy/internal/store"
	"crproxy/internal/upstreammgr"
)

type upstreamCredentialsRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	BearerToken string `json:"bearerToken"`
}

type upstreamWriteRequest struct {
	ID                 string                      `json:"id"`
	Name               string                      `json:"name"`
	Endpoint           string                      `json:"endpoint"`
	RouteHost          string                      `json:"routeHost"`
	RoutePrefix        string                      `json:"routePrefix"`
	RepositoryPrefix   string                      `json:"repositoryPrefix"`
	AuthType           string                      `json:"authType"`
	CAPEM              *string                     `json:"caPem"`
	InsecureSkipVerify *bool                       `json:"insecureSkipVerify"`
	TagTTLSeconds      *int64                      `json:"tagTtlSeconds"`
	RequestTimeoutSecs *int64                      `json:"requestTimeoutSeconds"`
	MaxBlobBytes       *int64                      `json:"maxBlobBytes"`
	ClearMaxBlobBytes  bool                        `json:"clearMaxBlobBytes"`
	Enabled            *bool                       `json:"enabled"`
	ReplaceCredentials bool                        `json:"replaceCredentials"`
	Credentials        *upstreamCredentialsRequest `json:"credentials"`
}

type upstreamResponse struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Endpoint           string     `json:"endpoint"`
	RouteHost          string     `json:"routeHost,omitempty"`
	RoutePrefix        string     `json:"routePrefix"`
	RepositoryPrefix   string     `json:"repositoryPrefix,omitempty"`
	AuthType           string     `json:"authType"`
	HasCredentials     bool       `json:"hasCredentials"`
	CAPEM              string     `json:"caPem,omitempty"`
	InsecureSkipVerify bool       `json:"insecureSkipVerify"`
	TagTTLSeconds      int64      `json:"tagTtlSeconds"`
	RequestTimeoutSecs int64      `json:"requestTimeoutSeconds"`
	MaxBlobBytes       *int64     `json:"maxBlobBytes,omitempty"`
	Enabled            bool       `json:"enabled"`
	HealthState        string     `json:"healthState"`
	HealthMessage      string     `json:"healthMessage,omitempty"`
	HealthCheckedAt    *time.Time `json:"healthCheckedAt,omitempty"`
}

func toUpstreamResponse(upstream store.Upstream) upstreamResponse {
	var checkedAt *time.Time
	if upstream.HealthCheckedAt != nil {
		value := time.Unix(*upstream.HealthCheckedAt, 0).UTC()
		checkedAt = &value
	}
	return upstreamResponse{
		ID:                 upstream.ID,
		Name:               upstream.Name,
		Endpoint:           upstream.Endpoint,
		RouteHost:          upstream.RouteHost,
		RoutePrefix:        upstream.RoutePrefix,
		RepositoryPrefix:   upstream.RepositoryPrefix,
		AuthType:           upstream.AuthType,
		HasCredentials:     len(upstream.Credentials) > 0,
		CAPEM:              upstream.CAPEM,
		InsecureSkipVerify: upstream.InsecureSkipVerify,
		TagTTLSeconds:      upstream.TagTTLSeconds,
		RequestTimeoutSecs: upstream.RequestTimeoutSecs,
		MaxBlobBytes:       upstream.MaxBlobBytes,
		Enabled:            upstream.Enabled,
		HealthState:        upstream.HealthState,
		HealthMessage:      upstream.HealthMessage,
		HealthCheckedAt:    checkedAt,
	}
}

func (h *Handler) handleUpstreamList(w http.ResponseWriter, r *http.Request, _ *principal) {
	upstreams, err := h.upstreams.List(r.Context())
	if err != nil {
		h.Logger.Error("list upstreams failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	result := make([]upstreamResponse, 0, len(upstreams))
	for _, upstream := range upstreams {
		result = append(result, toUpstreamResponse(upstream))
	}
	writeJSON(w, http.StatusOK, map[string]any{"upstreams": result})
}

func (h *Handler) handleUpstreamGet(w http.ResponseWriter, r *http.Request, _ *principal) {
	upstream, err := h.upstreams.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "upstream_not_found", "upstream not found")
		return
	}
	if err != nil {
		h.Logger.Error("read upstream failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, toUpstreamResponse(upstream))
}

func (h *Handler) handleUpstreamCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var body upstreamWriteRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	input := upstreamInputFromRequest(body, nil, true)
	upstream, err := h.upstreams.Create(r.Context(), input)
	if err != nil {
		h.writeUpstreamError(w, err)
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "upstream.create", "upstream", upstream.ID, "success", "")
	writeJSON(w, http.StatusCreated, toUpstreamResponse(upstream))
}

func (h *Handler) handleUpstreamUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	upstreamID := r.PathValue("id")
	existing, err := h.upstreams.Get(r.Context(), upstreamID)
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "upstream_not_found", "upstream not found")
		return
	}
	if err != nil {
		h.Logger.Error("read upstream failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	var body upstreamWriteRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	if body.ID != "" && body.ID != upstreamID {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "upstream id cannot be changed")
		return
	}
	input := upstreamInputFromRequest(body, &existing, false)
	input.ID = upstreamID
	updated, err := h.upstreams.Upsert(r.Context(), input)
	if err != nil {
		h.writeUpstreamError(w, err)
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "upstream.update", "upstream", upstreamID, "success", "")
	writeJSON(w, http.StatusOK, toUpstreamResponse(updated))
}

func (h *Handler) handleUpstreamDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	upstreamID := r.PathValue("id")
	if err := h.upstreams.Delete(r.Context(), upstreamID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "upstream_not_found", "upstream not found")
			return
		}
		h.Logger.Error("delete upstream failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "upstream.delete", "upstream", upstreamID, "success", "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleUpstreamTest(w http.ResponseWriter, r *http.Request, p *principal) {
	upstreamID := r.PathValue("id")
	if err := h.upstreams.Test(r.Context(), upstreamID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeAPIError(w, http.StatusNotFound, "upstream_not_found", "upstream not found")
			return
		}
		h.recordAudit(r, "user", p.Identity.UserID, "upstream.test", "upstream", upstreamID, "failure", err.Error())
		writeAPIError(w, http.StatusBadGateway, "upstream_unreachable", err.Error())
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "upstream.test", "upstream", upstreamID, "success", "")
	writeJSON(w, http.StatusOK, map[string]any{"status": "healthy"})
}

func upstreamInputFromRequest(body upstreamWriteRequest, existing *store.Upstream, create bool) upstreammgr.UpsertInput {
	input := upstreammgr.UpsertInput{
		ID:                 body.ID,
		Name:               body.Name,
		Endpoint:           body.Endpoint,
		RouteHost:          body.RouteHost,
		RoutePrefix:        body.RoutePrefix,
		RepositoryPrefix:   body.RepositoryPrefix,
		AuthType:           body.AuthType,
		Enabled:            true,
		TagTTLSeconds:      60,
		RequestTimeoutSecs: 30,
		ReplaceCredentials: create || body.ReplaceCredentials,
	}
	if body.Credentials != nil {
		input.Credentials = upstreammgr.Credentials{
			Username:    body.Credentials.Username,
			Password:    body.Credentials.Password,
			BearerToken: body.Credentials.BearerToken,
		}
	}
	if body.CAPEM != nil {
		input.CAPEM = *body.CAPEM
	}
	if body.InsecureSkipVerify != nil {
		input.InsecureSkipVerify = *body.InsecureSkipVerify
	}
	if body.TagTTLSeconds != nil {
		input.TagTTLSeconds = *body.TagTTLSeconds
	}
	if body.RequestTimeoutSecs != nil {
		input.RequestTimeoutSecs = *body.RequestTimeoutSecs
	}
	if body.MaxBlobBytes != nil {
		input.MaxBlobBytes = body.MaxBlobBytes
	}
	if body.Enabled != nil {
		input.Enabled = *body.Enabled
	}
	if existing != nil {
		if input.Name == "" {
			input.Name = existing.Name
		}
		if input.Endpoint == "" {
			input.Endpoint = existing.Endpoint
		}
		if input.RouteHost == "" {
			input.RouteHost = existing.RouteHost
		}
		if input.RoutePrefix == "" {
			input.RoutePrefix = existing.RoutePrefix
		}
		if body.RepositoryPrefix == "" {
			input.RepositoryPrefix = existing.RepositoryPrefix
		}
		if input.AuthType == "" {
			input.AuthType = existing.AuthType
		}
		if body.CAPEM == nil {
			input.CAPEM = existing.CAPEM
		}
		if body.InsecureSkipVerify == nil {
			input.InsecureSkipVerify = existing.InsecureSkipVerify
		}
		if body.TagTTLSeconds == nil {
			input.TagTTLSeconds = existing.TagTTLSeconds
		}
		if body.RequestTimeoutSecs == nil {
			input.RequestTimeoutSecs = existing.RequestTimeoutSecs
		}
		if body.Enabled == nil {
			input.Enabled = existing.Enabled
		}
		if body.MaxBlobBytes == nil && !body.ClearMaxBlobBytes {
			input.MaxBlobBytes = existing.MaxBlobBytes
		}
	}
	if body.ClearMaxBlobBytes {
		input.MaxBlobBytes = nil
	}
	return input
}

func (h *Handler) writeUpstreamError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, upstreammgr.ErrInvalid):
		writeAPIError(w, http.StatusBadRequest, "invalid_upstream", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "upstream_not_found", "upstream not found")
	case errors.Is(err, store.ErrConflict):
		writeAPIError(w, http.StatusConflict, "upstream_conflict", "upstream already exists or was changed concurrently")
	case isUniqueConstraint(err):
		writeAPIError(w, http.StatusConflict, "upstream_conflict", "upstream id, name, route host or route prefix already exists")
	default:
		h.Logger.Error("upstream operation failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
