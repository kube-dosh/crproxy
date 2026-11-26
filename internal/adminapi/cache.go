package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"crproxy/internal/storage"
	"crproxy/internal/store"
)

func (h *Handler) handleCacheEntries(w http.ResponseWriter, r *http.Request, _ *principal) {
	limit, offset, ok := h.pagination(w, r)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "blob" && kind != "manifest" {
		writeAPIError(w, http.StatusBadRequest, "invalid_kind", "kind must be blob or manifest")
		return
	}
	entries, total, err := h.Store.ListContents(r.Context(), store.ContentFilter{
		UpstreamID: r.URL.Query().Get("upstream_id"),
		Kind:       kind,
		Query:      r.URL.Query().Get("q"),
		Sort:       r.URL.Query().Get("sort"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "total": total, "limit": limit, "offset": offset})
}

type cacheEvictionRequest struct {
	RepositoryID int64    `json:"repositoryId"`
	Digests      []string `json:"digests"`
}

func (h *Handler) handleCacheEviction(w http.ResponseWriter, r *http.Request, p *principal) {
	var body cacheEvictionRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
		return
	}
	if body.RepositoryID <= 0 && len(body.Digests) == 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "repositoryId or digests is required")
		return
	}
	if body.RepositoryID > 0 && len(body.Digests) > 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "provide either repositoryId or digests, not both")
		return
	}
	job, err := h.jobs.Start(r.Context(), "cache.eviction", p.Identity.UserID, 30*time.Minute, func(ctx context.Context, progress func(string)) (any, error) {
		total := storage.GCResult{}
		if body.RepositoryID > 0 {
			progress("evicting cache repository")
			result, err := h.storage.EvictRepository(ctx, body.RepositoryID)
			if err != nil {
				return nil, err
			}
			total = result
		} else {
			for index, digest := range body.Digests {
				progress(fmt.Sprintf("evicting digest %d/%d", index+1, len(body.Digests)))
				result, err := h.storage.EvictRepositoriesForDigest(ctx, digest)
				if err != nil {
					return nil, err
				}
				total.EvictedRepos += result.EvictedRepos
				total.EvictedBytes += result.EvictedBytes
				total.OrphanContents += result.OrphanContents
				total.OrphanBytes += result.OrphanBytes
			}
		}
		return total, nil
	})
	if err != nil {
		h.Logger.Error("start cache eviction job failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "cache.evict", "cache", strconv.FormatInt(body.RepositoryID, 10), "accepted", job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

func (h *Handler) handleJobGet(w http.ResponseWriter, r *http.Request, _ *principal) {
	job, err := h.Store.JobByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "job_not_found", "job not found")
		return
	}
	if err != nil {
		h.Logger.Error("read job failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, job)
}
