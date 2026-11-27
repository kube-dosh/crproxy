package adminapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/kube-dosh/crproxy/internal/store"
)

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request, _ *principal) {
	ctx := r.Context()
	storageSummary, err := h.storage.Summary(ctx)
	if err != nil {
		h.Logger.Error("read storage summary failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	localRepos, localCount, err := h.Store.ListRepositories(ctx, store.RepositoryFilter{Kind: "local", Limit: 1})
	_ = localRepos
	if err != nil {
		h.Logger.Error("count local repositories failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	_, cacheCount, err := h.Store.ListRepositories(ctx, store.RepositoryFilter{Kind: "cache", Limit: 1})
	if err != nil {
		h.Logger.Error("count cache repositories failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	upstreams, err := h.upstreams.List(ctx)
	if err != nil {
		h.Logger.Error("list upstreams failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	healthy := 0
	upstreamRows := make([]upstreamResponse, 0, len(upstreams))
	for _, upstream := range upstreams {
		if upstream.HealthState == "healthy" {
			healthy++
		}
		upstreamRows = append(upstreamRows, toUpstreamResponse(upstream))
	}
	events, err := h.Store.EventSummarySince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		h.Logger.Error("read event summary failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	activity, err := h.Store.ListRegistryEvents(ctx, 30)
	if err != nil {
		h.Logger.Error("read recent registry activity failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	hitRate := 0.0
	if events.CacheHits+events.CacheMisses > 0 {
		hitRate = float64(events.CacheHits) / float64(events.CacheHits+events.CacheMisses)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"upstreams": map[string]any{
			"total":   len(upstreams),
			"healthy": healthy,
			"items":   upstreamRows,
		},
		"repositories": map[string]any{
			"local": localCount,
			"cache": cacheCount,
		},
		"storage":      storageSummary,
		"requests24h":  events,
		"cacheHitRate": hitRate,
		"activity":     activity,
		"generatedAt":  time.Now().UTC(),
	})
}

func (h *Handler) handleEventTimeline(w http.ResponseWriter, r *http.Request, _ *principal) {
	hours := 24
	if raw := r.URL.Query().Get("hours"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 168 {
			writeAPIError(w, http.StatusBadRequest, "invalid_hours", "hours must be between 1 and 168")
			return
		}
		hours = value
	}
	now := time.Now().UTC().Truncate(time.Hour)
	since := now.Add(-time.Duration(hours-1) * time.Hour)
	buckets, err := h.Store.EventBucketsSince(r.Context(), since)
	if err != nil {
		h.Logger.Error("read event timeline failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	byHour := make(map[time.Time]store.EventBucket, len(buckets))
	for _, bucket := range buckets {
		byHour[bucket.At] = bucket
	}
	timeline := make([]store.EventBucket, 0, hours)
	for index := 0; index < hours; index++ {
		at := since.Add(time.Duration(index) * time.Hour)
		bucket := byHour[at]
		bucket.At = at
		timeline = append(timeline, bucket)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": hours, "since": since, "buckets": timeline})
}

func (h *Handler) handleRegistrySummary(w http.ResponseWriter, r *http.Request, _ *principal) {
	ctx := r.Context()
	_, localCount, err := h.Store.ListRepositories(ctx, store.RepositoryFilter{Kind: "local", Limit: 1})
	if err != nil {
		h.Logger.Error("count local repositories failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	_, cacheCount, err := h.Store.ListRepositories(ctx, store.RepositoryFilter{Kind: "cache", Limit: 1})
	if err != nil {
		h.Logger.Error("count cache repositories failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	usage, err := h.Store.StorageUsage(ctx)
	if err != nil {
		h.Logger.Error("read storage usage failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"localRepositories": localCount,
		"cacheRepositories": cacheCount,
		"contents":          usage.ContentCount,
		"localBytes":        usage.LocalBytes,
		"cacheBytes":        usage.CacheBytes,
		"physicalBytes":     usage.PhysicalBytes,
	})
}
