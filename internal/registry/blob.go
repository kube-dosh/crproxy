package registry

import (
	"errors"
	"fmt"
	"net/http"

	"crproxy/internal/store"
)

func (h *Handler) handleBlob(w http.ResponseWriter, r *http.Request, route parsedRoute) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if _, ok := h.authorize(w, r, route.repo, "pull"); !ok {
			return
		}
		h.getBlob(w, r, route)
	case http.MethodDelete:
		if _, ok := h.authorize(w, r, route.repo, "delete"); !ok {
			return
		}
		h.deleteBlob(w, r, route)
	default:
		writeMethodNotAllowed(w, "GET, HEAD, DELETE")
	}
}

func (h *Handler) getBlob(w http.ResponseWriter, r *http.Request, route parsedRoute) {
	repo, err := h.repositoryForRead(r.Context(), route.repo)
	if errors.Is(err, store.ErrNotFound) {
		writeNotFound(w, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	content, err := h.Store.BlobContent(r.Context(), repo.ID, route.digest)
	if errors.Is(err, store.ErrNotFound) {
		writeRegistryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry", map[string]any{"digest": route.digest.String()})
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	file, info, err := h.CAS.OpenBlob(route.digest)
	if err != nil {
		h.internalError(w, fmt.Errorf("blob content missing for %s: %w", route.digest, err))
		return
	}
	defer file.Close()
	if info.Size() != content.Size {
		h.internalError(w, fmt.Errorf("blob size mismatch for %s: metadata=%d file=%d", route.digest, content.Size, info.Size()))
		return
	}
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	w.Header().Set("Docker-Content-Digest", route.digest.String())
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", content.Size))
	http.ServeContent(w, r, route.digest.Encoded, info.ModTime(), file)
}

func (h *Handler) deleteBlob(w http.ResponseWriter, r *http.Request, route parsedRoute) {
	repo, err := h.repositoryForRead(r.Context(), route.repo)
	if errors.Is(err, store.ErrNotFound) {
		writeNotFound(w, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	err = h.Store.DeleteBlob(r.Context(), repo.ID, route.digest)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeRegistryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry", map[string]any{"digest": route.digest.String()})
		return
	case errors.Is(err, store.ErrBlobReferenced):
		writeRegistryError(w, http.StatusConflict, "BLOB_REFERENCED", "blob is referenced by a manifest", map[string]any{"digest": route.digest.String()})
		return
	case err != nil:
		h.internalError(w, err)
		return
	}
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	w.WriteHeader(http.StatusAccepted)
}
