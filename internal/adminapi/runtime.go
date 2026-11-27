package adminapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"crproxy/internal/proxy"
	"crproxy/internal/runtimeconfig"
)

type runtimeUpdate struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

type registryAuthUpdate struct {
	ExpectedVersion int64 `json:"expectedVersion"`
	runtimeconfig.RegistryAuth
}

type webSessionUpdate struct {
	ExpectedVersion int64 `json:"expectedVersion"`
	runtimeconfig.WebSession
}

type storageUpdate struct {
	ExpectedVersion int64 `json:"expectedVersion"`
	runtimeconfig.Storage
}

type uploadsUpdate struct {
	ExpectedVersion int64 `json:"expectedVersion"`
	runtimeconfig.Uploads
}

type auditUpdate struct {
	ExpectedVersion int64 `json:"expectedVersion"`
	runtimeconfig.Audit
}

type proxyUpdate struct {
	ExpectedVersion int64 `json:"expectedVersion"`
	runtimeconfig.Proxy
}

type outboundProxyUpdate struct {
	ExpectedVersion int64 `json:"expectedVersion"`
	runtimeconfig.OutboundProxyInput
}

type securityUpdate struct {
	ExpectedVersion int64 `json:"expectedVersion"`
	runtimeconfig.Security
}

func (h *Handler) handleRuntimeSettingsGet(w http.ResponseWriter, r *http.Request, _ *principal) {
	if h.runtime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_config_unavailable", "runtime configuration is unavailable")
		return
	}
	if h.runtime.Current() == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_config_unavailable", "runtime configuration is not initialized")
		return
	}
	writeJSON(w, http.StatusOK, h.runtime.View())
}

func (h *Handler) handleRuntimeSettingsPut(w http.ResponseWriter, r *http.Request, p *principal) {
	if h.runtime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_config_unavailable", "runtime configuration is unavailable")
		return
	}
	section := r.PathValue("section")
	if !runtimeSectionWriteAllowed(p, section) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "administrator permission is required for this configuration section")
		return
	}
	actor := p.Identity.Username
	if actor == "" {
		actor = p.Identity.UserID
	}
	var (
		view runtimeconfig.View
		err  error
	)
	switch section {
	case runtimeconfig.SectionRegistryAuth:
		var body registryAuthUpdate
		if decodeJSON(w, r, &body) != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		view, err = h.runtime.UpdateRegistryAuth(r.Context(), body.ExpectedVersion, actor, body.RegistryAuth)
	case runtimeconfig.SectionWebSession:
		var body webSessionUpdate
		if decodeJSON(w, r, &body) != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		view, err = h.runtime.UpdateWebSession(r.Context(), body.ExpectedVersion, actor, body.WebSession)
	case runtimeconfig.SectionStorage:
		var body storageUpdate
		if decodeJSON(w, r, &body) != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		view, err = h.runtime.UpdateStorage(r.Context(), body.ExpectedVersion, actor, body.Storage)
	case runtimeconfig.SectionUploads:
		var body uploadsUpdate
		if decodeJSON(w, r, &body) != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		view, err = h.runtime.UpdateUploads(r.Context(), body.ExpectedVersion, actor, body.Uploads)
	case runtimeconfig.SectionAudit:
		var body auditUpdate
		if decodeJSON(w, r, &body) != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		view, err = h.runtime.UpdateAudit(r.Context(), body.ExpectedVersion, actor, body.Audit)
	case runtimeconfig.SectionProxy:
		var body proxyUpdate
		if decodeJSON(w, r, &body) != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		view, err = h.runtime.UpdateProxy(r.Context(), body.ExpectedVersion, actor, body.Proxy)
	case runtimeconfig.SectionOutbound:
		var body outboundProxyUpdate
		if decodeJSON(w, r, &body) != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		view, err = h.runtime.UpdateOutboundProxy(r.Context(), body.ExpectedVersion, actor, body.OutboundProxyInput)
	case runtimeconfig.SectionSecurity:
		var body securityUpdate
		if decodeJSON(w, r, &body) != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		view, err = h.runtime.UpdateSecurity(r.Context(), body.ExpectedVersion, actor, body.Security)
	default:
		writeAPIError(w, http.StatusNotFound, "unknown_config_section", "unknown runtime configuration section")
		return
	}
	if err != nil {
		h.writeRuntimeConfigError(w, err)
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "config.runtime.update", "config", section, "success", "")
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) handleRuntimeSettingsReset(w http.ResponseWriter, r *http.Request, p *principal) {
	if h.runtime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_config_unavailable", "runtime configuration is unavailable")
		return
	}
	section := r.PathValue("section")
	if !runtimeSectionWriteAllowed(p, section) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "administrator permission is required for this configuration section")
		return
	}
	var body runtimeUpdate
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	actor := p.Identity.Username
	if actor == "" {
		actor = p.Identity.UserID
	}
	view, err := h.runtime.ResetSection(r.Context(), section, body.ExpectedVersion, actor)
	if err != nil {
		h.writeRuntimeConfigError(w, err)
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "config.runtime.reset", "config", section, "success", "")
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) writeRuntimeConfigError(w http.ResponseWriter, err error) {
	var conflict *runtimeconfig.ConflictError
	if errors.As(err, &conflict) {
		writeJSON(w, http.StatusConflict, apiErrorBody{
			Code:    "config_version_conflict",
			Message: "configuration was changed by another administrator",
			Details: map[string]any{
				"section":        conflict.Section,
				"currentVersion": conflict.CurrentVersion,
				"updatedAt":      conflict.UpdatedAt,
				"updatedBy":      conflict.UpdatedBy,
			},
		})
		return
	}
	if errors.Is(err, runtimeconfig.ErrUnknownSection) {
		writeAPIError(w, http.StatusNotFound, "unknown_config_section", "unknown runtime configuration section")
		return
	}
	if errors.Is(err, runtimeconfig.ErrInvalid) {
		writeAPIError(w, http.StatusBadRequest, "invalid_runtime_config", err.Error())
		return
	}
	h.Logger.Error("update runtime configuration failed", "error", err)
	writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
}

func runtimeSectionWriteAllowed(p *principal, section string) bool {
	if p == nil || p.Identity == nil {
		return false
	}
	switch section {
	case runtimeconfig.SectionStorage, runtimeconfig.SectionUploads:
		return p.Identity.HasAnyScope("manage", "admin")
	default:
		return p.Identity.HasScope("admin")
	}
}

func (h *Handler) handleOutboundProxyTest(w http.ResponseWriter, r *http.Request, p *principal) {
	if h.runtime == nil || h.runtime.Current() == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_config_unavailable", "runtime configuration is unavailable")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
	}
	target := strings.TrimSpace(body.URL)
	if target == "" {
		target = "https://registry-1.docker.io/v2/"
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeAPIError(w, http.StatusBadRequest, "invalid_test_url", "url must be an absolute HTTP(S) URL")
		return
	}
	effective := h.runtime.Current().Outbound
	outbound := &proxy.OutboundProxy{
		HTTPProxy:  effective.HTTPProxy,
		HTTPSProxy: effective.HTTPSProxy,
		NoProxy:    effective.NoProxy,
		Username:   effective.Username,
		Password:   effective.Password,
	}
	proxyFunc, err := outbound.ProxyFunc()
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "proxy_config_invalid", err.Error())
		return
	}
	transport := &http.Transport{
		Proxy:                 proxyFunc,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	started := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_test_url", "invalid test URL")
		return
	}
	req.Header.Set("User-Agent", "crproxy-outbound-proxy-test/1")
	resp, err := client.Do(req)
	if err != nil {
		h.recordAudit(r, "user", p.Identity.UserID, "config.runtime.proxy_test", "config", "outbound-proxy", "failure", err.Error())
		writeAPIError(w, http.StatusBadGateway, "proxy_test_failed", err.Error())
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	h.recordAudit(r, "user", p.Identity.UserID, "config.runtime.proxy_test", "config", "outbound-proxy", "success", fmt.Sprintf("status=%d", resp.StatusCode))
	writeJSON(w, http.StatusOK, map[string]any{
		"url":        target,
		"status":     resp.StatusCode,
		"durationMs": time.Since(started).Milliseconds(),
	})
}
