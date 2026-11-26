package adminapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"crproxy/internal/oci"
	"crproxy/internal/store"
)

func (h *Handler) handleRepositoryList(w http.ResponseWriter, r *http.Request, _ *principal) {
	limit, err := parseLimit(r.URL.Query().Get("limit"), 100)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	offset, err := parseOffset(r.URL.Query().Get("offset"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_offset", err.Error())
		return
	}
	repositories, total, err := h.Store.ListRepositories(r.Context(), store.RepositoryFilter{
		Kind:       r.URL.Query().Get("kind"),
		UpstreamID: r.URL.Query().Get("upstream_id"),
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		if strings.Contains(err.Error(), "invalid repository kind") {
			writeAPIError(w, http.StatusBadRequest, "invalid_kind", err.Error())
			return
		}
		h.Logger.Error("list repositories failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repositories": repositories,
		"total":        total,
		"limit":        limit,
		"offset":       offset,
	})
}

func (h *Handler) handleRepositoryGet(w http.ResponseWriter, r *http.Request, _ *principal) {
	repository, ok := h.repositoryFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, repository)
}

func (h *Handler) handleRepositoryDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	repository, ok := h.repositoryFromPath(w, r)
	if !ok {
		return
	}
	if repository.Kind != "local" {
		writeAPIError(w, http.StatusConflict, "cache_repository", "cache repositories must be removed through cache eviction")
		return
	}
	if err := h.Store.DeleteRepositoryReferences(r.Context(), repository.ID); err != nil {
		h.Logger.Error("delete repository failed", "repository_id", repository.ID, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "repository.delete", "repository", strconv.FormatInt(repository.ID, 10), "success", repository.Name)
	writeJSON(w, http.StatusAccepted, map[string]any{"deleted": true, "repositoryId": repository.ID})
}

func (h *Handler) handleRepositoryTags(w http.ResponseWriter, r *http.Request, _ *principal) {
	repository, ok := h.repositoryFromPath(w, r)
	if !ok {
		return
	}
	var staleBefore time.Time
	if repository.Kind == "cache" && repository.UpstreamID != nil {
		if upstream, err := h.upstreams.Get(r.Context(), *repository.UpstreamID); err == nil && upstream.TagTTLSeconds > 0 {
			staleBefore = time.Now().Add(-time.Duration(upstream.TagTTLSeconds) * time.Second)
		}
	}
	tags, err := h.Store.RepositoryTags(r.Context(), repository.ID, staleBefore)
	if err != nil {
		h.Logger.Error("list repository tags failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags, "total": len(tags)})
}

func (h *Handler) handleRepositoryManifests(w http.ResponseWriter, r *http.Request, _ *principal) {
	h.listRepositoryContents(w, r, "manifest")
}

func (h *Handler) handleRepositoryBlobs(w http.ResponseWriter, r *http.Request, _ *principal) {
	h.listRepositoryContents(w, r, "blob")
}

func (h *Handler) listRepositoryContents(w http.ResponseWriter, r *http.Request, kind string) {
	repository, ok := h.repositoryFromPath(w, r)
	if !ok {
		return
	}
	limit, offset, ok := h.pagination(w, r)
	if !ok {
		return
	}
	contents, total, err := h.Store.ListContents(r.Context(), store.ContentFilter{
		RepositoryID: repository.ID,
		Kind:         kind,
		Query:        r.URL.Query().Get("q"),
		Sort:         r.URL.Query().Get("sort"),
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": contents, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) handleRepositoryReferrers(w http.ResponseWriter, r *http.Request, _ *principal) {
	repository, ok := h.repositoryFromPath(w, r)
	if !ok {
		return
	}
	referrers, err := h.Store.RepositoryReferrers(r.Context(), repository.ID)
	if err != nil {
		h.Logger.Error("list repository referrers failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"referrers": referrers, "total": len(referrers)})
}

func (h *Handler) handleRepositoryManifestDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	repository, ok := h.repositoryFromPath(w, r)
	if !ok {
		return
	}
	if repository.Kind != "local" {
		writeAPIError(w, http.StatusConflict, "cache_repository", "cache repositories must be removed through cache eviction")
		return
	}
	digest, err := oci.ParseDigest(r.PathValue("digest"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_digest", "digest is invalid")
		return
	}
	deleted, err := h.Store.DeleteManifest(r.Context(), repository.ID, digest)
	if err != nil {
		h.Logger.Error("delete manifest failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if !deleted {
		writeAPIError(w, http.StatusNotFound, "manifest_not_found", "manifest not found")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "manifest.delete", "repository", strconv.FormatInt(repository.ID, 10), "success", digest.String())
	writeJSON(w, http.StatusAccepted, map[string]any{"deleted": true})
}

func (h *Handler) handleRepositoryTagDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	repository, ok := h.repositoryFromPath(w, r)
	if !ok {
		return
	}
	if repository.Kind != "local" {
		writeAPIError(w, http.StatusConflict, "cache_repository", "cache repositories must be removed through cache eviction")
		return
	}
	tag := r.PathValue("tag")
	if err := oci.ValidateTag(tag); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_tag", "tag is invalid")
		return
	}
	deleted, err := h.Store.DeleteTag(r.Context(), repository.ID, tag)
	if err != nil {
		h.Logger.Error("delete tag failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if !deleted {
		writeAPIError(w, http.StatusNotFound, "tag_not_found", "tag not found")
		return
	}
	h.recordAudit(r, "user", p.Identity.UserID, "tag.delete", "repository", strconv.FormatInt(repository.ID, 10), "success", tag)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) repositoryFromPath(w http.ResponseWriter, r *http.Request) (store.RepositorySummary, bool) {
	repositoryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || repositoryID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_repository", "repository id is invalid")
		return store.RepositorySummary{}, false
	}
	repository, err := h.Store.RepositorySummaryByID(r.Context(), repositoryID)
	if errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "repository_not_found", "repository not found")
		return store.RepositorySummary{}, false
	}
	if err != nil {
		h.Logger.Error("read repository failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return store.RepositorySummary{}, false
	}
	return repository, true
}

func (h *Handler) pagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, err := parseLimit(r.URL.Query().Get("limit"), 100)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return 0, 0, false
	}
	offset, err := parseOffset(r.URL.Query().Get("offset"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_offset", err.Error())
		return 0, 0, false
	}
	return limit, offset, true
}

func parseOffset(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, errors.New("offset must be a non-negative integer")
	}
	return value, nil
}
