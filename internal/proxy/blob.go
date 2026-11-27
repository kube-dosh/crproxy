package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/kube-dosh/crproxy/internal/cas"
	"github.com/kube-dosh/crproxy/internal/oci"
	"github.com/kube-dosh/crproxy/internal/store"
)

var (
	errUpstreamNotFound = errors.New("upstream content not found")
	errBlobTooLarge     = errors.New("blob exceeds size limit")
	errCacheBypass      = errors.New("cache storage bypass")
	errCacheRejected    = errors.New("cache storage rejected")
)

func (h *Handler) serveBlob(w http.ResponseWriter, r *http.Request, upstream *Upstream, route parsedRoute) {
	release, err := h.acquireDownload(r.Context())
	if err != nil {
		return
	}
	defer release()
	repo, err := h.Store.EnsureCacheRepository(r.Context(), upstream.ID, route.repository)
	if err != nil {
		h.Logger.Error("ensure cache repository failed", "error", err)
		writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "internal server error", nil)
		return
	}
	if h.serveCachedBlob(w, r, repo, route.digest) {
		h.recordPull(r.Context(), "blob.pull", repo, upstream.ID, route.digest.String(), 0, true, "success", "")
		_ = h.Store.TouchContent(context.WithoutCancel(r.Context()), route.digest.String())
		return
	}
	upstreamRepo := upstream.MapRepository(route.repository)
	path := "/v2/" + upstreamRepo + "/blobs/" + route.digest.String()
	if r.Method == http.MethodHead {
		h.headUpstreamBlob(w, r, upstream, upstreamRepo, path, route.digest)
		return
	}
	if r.Header.Get("Range") != "" {
		h.streamUpstreamBlob(w, r, upstream, upstreamRepo, path, route.digest)
		h.recordPull(r.Context(), "blob.pull", repo, upstream.ID, route.digest.String(), 0, false, "success", "range request streamed without caching")
		return
	}
	key := upstream.ID + "|" + route.digest.String()
	err = h.flights.Do(key, func() error {
		if h.cachedBlobExists(r.Context(), repo, route.digest) {
			return nil
		}
		return h.downloadBlob(r.Context(), upstream, repo, upstreamRepo, path, route.digest)
	})
	if errors.Is(err, errUpstreamNotFound) {
		writeRegistryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry", map[string]any{"digest": route.digest.String()})
		return
	}
	if errors.Is(err, errCacheRejected) {
		writeRegistryError(w, http.StatusInsufficientStorage, "UNKNOWN", "insufficient storage", nil)
		return
	}
	if errors.Is(err, errCacheBypass) {
		h.streamUpstreamBlob(w, r, upstream, upstreamRepo, path, route.digest)
		h.recordPull(r.Context(), "blob.pull", repo, upstream.ID, route.digest.String(), 0, false, "success", "cache bypass")
		return
	}
	if err != nil {
		h.Logger.Warn("upstream blob download failed", "upstream", upstream.ID, "digest", route.digest, "error", err)
		writeUpstreamError(w)
		return
	}
	if err := h.ensureBlobRef(r.Context(), repo, route.digest); err != nil {
		h.Logger.Warn("record cached blob reference failed", "error", err)
		writeUpstreamError(w)
		return
	}
	if !h.serveCachedBlob(w, r, repo, route.digest) {
		writeUpstreamError(w)
		return
	}
	if content, err := h.Store.BlobContent(r.Context(), repo.ID, route.digest); err == nil {
		h.recordPull(r.Context(), "blob.pull", repo, upstream.ID, route.digest.String(), content.Size, false, "success", "")
	}
}

func (h *Handler) serveCachedBlob(w http.ResponseWriter, r *http.Request, repo store.Repository, digest oci.Digest) bool {
	content, err := h.Store.BlobContent(r.Context(), repo.ID, digest)
	if err != nil {
		return false
	}
	file, info, err := h.CAS.OpenBlob(digest)
	if err != nil || info.Size() != content.Size {
		if file != nil {
			_ = file.Close()
		}
		return false
	}
	defer file.Close()
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest.String())
	w.Header().Set("Content-Length", strconv.FormatInt(content.Size, 10))
	http.ServeContent(w, r, digest.Encoded, info.ModTime(), file)
	return true
}

func (h *Handler) cachedBlobExists(ctx context.Context, repo store.Repository, digest oci.Digest) bool {
	content, err := h.Store.BlobContent(ctx, repo.ID, digest)
	if err != nil {
		return false
	}
	file, info, err := h.CAS.OpenBlob(digest)
	if err != nil {
		return false
	}
	_ = file.Close()
	return info.Size() == content.Size
}

func (h *Handler) headUpstreamBlob(w http.ResponseWriter, r *http.Request, upstream *Upstream, upstreamRepo, path string, digest oci.Digest) {
	ctx, cancel := h.requestContext(r, upstream, true)
	defer cancel()
	resp, err := upstream.Do(ctx, http.MethodHead, path, upstreamRepo, nil)
	if err != nil {
		writeUpstreamError(w)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		writeRegistryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry", map[string]any{"digest": digest.String()})
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeUpstreamError(w)
		return
	}
	copyBlobResponseHeaders(w.Header(), resp.Header, digest)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) streamUpstreamBlob(w http.ResponseWriter, r *http.Request, upstream *Upstream, upstreamRepo, path string, digest oci.Digest) {
	ctx, cancel := h.requestContext(r, upstream, false)
	defer cancel()
	headers := http.Header{}
	if value := r.Header.Get("Range"); value != "" {
		headers.Set("Range", value)
	}
	resp, err := upstream.Do(ctx, http.MethodGet, path, upstreamRepo, headers)
	if err != nil {
		writeUpstreamError(w)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		writeRegistryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry", map[string]any{"digest": digest.String()})
		return
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		writeUpstreamError(w)
		return
	}
	copyBlobResponseHeaders(w.Header(), resp.Header, digest)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (h *Handler) downloadBlob(ctx context.Context, upstream *Upstream, repo store.Repository, upstreamRepo, path string, digest oci.Digest) error {
	resp, err := upstream.Do(ctx, http.MethodGet, path, upstreamRepo, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errUpstreamNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	limit := upstream.MaxBlobBytes
	if limit <= 0 {
		limit = 20 << 30
	}
	if resp.ContentLength > limit {
		return errBlobTooLarge
	}
	if h.Storage != nil {
		decision, err := h.Storage.CanCache(ctx, resp.ContentLength)
		if err != nil {
			return err
		}
		if !decision.Cache {
			if decision.Bypass {
				return errCacheBypass
			}
			return errCacheRejected
		}
	}
	uploadID, err := cas.NewUploadID()
	if err != nil {
		return err
	}
	file, err := h.CAS.CreateUpload(uploadID)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		_ = h.CAS.DeleteUpload(uploadID)
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = h.CAS.DeleteUpload(uploadID)
		}
	}()
	_, tooLarge, err := h.CAS.AppendUploadLimited(uploadID, resp.Body, limit)
	if err != nil {
		return err
	}
	if tooLarge {
		return errBlobTooLarge
	}
	upload, err := h.CAS.OpenUpload(uploadID, false)
	if err != nil {
		return err
	}
	actual, size, err := oci.FromReader(digest.Algorithm, upload)
	_ = upload.Close()
	if err != nil {
		return err
	}
	if actual != digest {
		return fmt.Errorf("upstream blob digest mismatch: expected %s got %s", digest, actual)
	}
	if err := h.CAS.CommitUpload(uploadID, digest); err != nil {
		return err
	}
	cleanup = false
	if err := h.Store.PutBlobRef(ctx, repo.ID, digest, size, "application/octet-stream"); err != nil {
		return err
	}
	h.recordCache(ctx, "blob.cache", repo, upstream.ID, digest.String(), size, "success", "")
	return nil
}

func (h *Handler) ensureBlobRef(ctx context.Context, repo store.Repository, digest oci.Digest) error {
	if _, err := h.Store.BlobContent(ctx, repo.ID, digest); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	file, info, err := h.CAS.OpenBlob(digest)
	if err != nil {
		return err
	}
	_ = file.Close()
	return h.Store.PutBlobRef(ctx, repo.ID, digest, info.Size(), "application/octet-stream")
}

func copyBlobResponseHeaders(target, source http.Header, digest oci.Digest) {
	target.Set("Docker-Distribution-Api-Version", "registry/2.0")
	target.Set("Content-Type", "application/octet-stream")
	target.Set("Docker-Content-Digest", digest.String())
	for _, key := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if value := source.Get(key); value != "" {
			target.Set(key, value)
		}
	}
}
