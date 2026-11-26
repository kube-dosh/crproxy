package proxy

import (
	"context"
	"time"

	"crproxy/internal/store"
)

func (h *Handler) acquireDownload(ctx context.Context) (func(), error) {
	if h.Storage == nil {
		return func() {}, nil
	}
	return h.Storage.AcquireDownload(ctx)
}

func (h *Handler) recordPull(ctx context.Context, eventType string, repo store.Repository, upstreamID, digest string, size int64, cacheHit bool, result, message string) {
	eventCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	repositoryID := repo.ID
	upstream := upstreamID
	event := store.RegistryEvent{
		Type:         eventType,
		RepositoryID: &repositoryID,
		UpstreamID:   &upstream,
		Digest:       digest,
		Size:         size,
		CacheHit:     &cacheHit,
		Result:       result,
		Message:      message,
	}
	if err := h.Store.AddRegistryEvent(eventCtx, event); err != nil {
		h.Logger.Warn("record registry event failed", "type", eventType, "error", err)
	}
}

func (h *Handler) recordCache(ctx context.Context, eventType string, repo store.Repository, upstreamID, digest string, size int64, result, message string) {
	eventCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	repositoryID := repo.ID
	upstream := upstreamID
	if err := h.Store.AddRegistryEvent(eventCtx, store.RegistryEvent{
		Type:         eventType,
		RepositoryID: &repositoryID,
		UpstreamID:   &upstream,
		Digest:       digest,
		Size:         size,
		Result:       result,
		Message:      message,
	}); err != nil {
		h.Logger.Warn("record registry cache event failed", "type", eventType, "error", err)
	}
}
