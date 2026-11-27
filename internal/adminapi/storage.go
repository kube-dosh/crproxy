package adminapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kube-dosh/crproxy/internal/runtimeconfig"
	"github.com/kube-dosh/crproxy/internal/storage"
)

func (h *Handler) handleStorageSummary(w http.ResponseWriter, r *http.Request, _ *principal) {
	summary, err := h.storage.Summary(r.Context())
	if err != nil {
		h.Logger.Error("read storage summary failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) handleStorageBreakdown(w http.ResponseWriter, r *http.Request, _ *principal) {
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "upstream"
	}
	switch groupBy {
	case "upstream":
		items, err := h.Store.BreakdownByUpstream(r.Context())
		if err != nil {
			h.Logger.Error("read upstream storage breakdown failed", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"groupBy": groupBy, "items": items})
	case "repository":
		items, err := h.Store.BreakdownByRepository(r.Context(), r.URL.Query().Get("kind"))
		if err != nil {
			h.Logger.Error("read repository storage breakdown failed", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"groupBy": groupBy, "items": items})
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_group_by", "group_by must be upstream or repository")
	}
}

func (h *Handler) handleStorageSettingsGet(w http.ResponseWriter, r *http.Request, _ *principal) {
	writeJSON(w, http.StatusOK, h.storage.Settings())
}

func (h *Handler) handleStorageSettingsPut(w http.ResponseWriter, r *http.Request, p *principal) {
	var body storage.Settings
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	normalized := storage.NormalizeSettings(body)
	settings := normalized
	var err error
	if h.runtime != nil {
		current := h.runtime.Current()
		if current == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "runtime_config_unavailable", "runtime configuration is unavailable")
			return
		}
		next := current.Storage
		next.MaxBytes = normalized.MaxBytes
		next.CacheMaxBytes = normalized.CacheMaxBytes
		next.MinFreeBytes = normalized.MinFreeBytes
		next.HighWatermark = normalized.HighWatermark
		next.LowWatermark = normalized.LowWatermark
		next.OnFull = normalized.OnFull
		actor := p.Identity.Username
		if actor == "" {
			actor = p.Identity.UserID
		}
		_, err = h.runtime.UpdateStorage(r.Context(), current.Sections[runtimeconfig.SectionStorage].Version, actor, next)
	} else {
		settings, err = h.storage.UpdateSettings(r.Context(), normalized)
	}
	if errors.Is(err, storage.ErrInvalidSettings) {
		writeAPIError(w, http.StatusBadRequest, "invalid_storage_settings", err.Error())
		return
	}
	if err != nil {
		h.Logger.Error("update storage settings failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "storage.settings.update", "storage", "settings", "success", "")
	writeJSON(w, http.StatusOK, settings)
}

func (h *Handler) handleStorageGC(w http.ResponseWriter, r *http.Request, p *principal) {
	job, err := h.jobs.Start(r.Context(), "storage.gc", p.Identity.UserID, 2*time.Hour, func(ctx context.Context, progress func(string)) (any, error) {
		progress("collecting orphan content and cache eviction candidates")
		result, err := h.storage.GC(ctx, true)
		if err != nil {
			return nil, err
		}
		progress("garbage collection completed")
		return result, nil
	})
	if err != nil {
		h.Logger.Error("start storage GC failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "storage.gc", "storage", "gc", "accepted", job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

func (h *Handler) handleStorageFsck(w http.ResponseWriter, r *http.Request, p *principal) {
	var body struct {
		Mode string `json:"mode"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
	}
	if body.Mode == "" {
		body.Mode = "quick"
	}
	if body.Mode != "quick" && body.Mode != "full" {
		writeAPIError(w, http.StatusBadRequest, "invalid_mode", "mode must be quick or full")
		return
	}
	job, err := h.jobs.Start(r.Context(), "storage.fsck", p.Identity.UserID, 12*time.Hour, func(ctx context.Context, progress func(string)) (any, error) {
		progress("checking CAS and metadata integrity")
		result, err := h.storage.Fsck(ctx, body.Mode)
		if err != nil {
			return nil, err
		}
		progress("fsck completed")
		return result, nil
	})
	if err != nil {
		h.Logger.Error("start storage fsck failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "storage.fsck", "storage", body.Mode, "accepted", job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

func (h *Handler) handleStorageJobs(w http.ResponseWriter, r *http.Request, _ *principal) {
	limit, err := parseLimit(r.URL.Query().Get("limit"), 50)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	jobs, err := h.Store.ListJobs(r.Context(), limit)
	if err != nil {
		h.Logger.Error("list jobs failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}
