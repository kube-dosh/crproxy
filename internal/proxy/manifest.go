package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"crproxy/internal/oci"
	"crproxy/internal/store"
)

func (h *Handler) serveManifest(w http.ResponseWriter, r *http.Request, upstream *Upstream, route parsedRoute) {
	acceptHash := manifestAcceptHash(r.Header.Values("Accept"))
	repo, err := h.Store.EnsureCacheRepository(r.Context(), upstream.ID, route.repository)
	if err != nil {
		h.Logger.Error("ensure cache repository failed", "error", err)
		writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "internal server error", nil)
		return
	}
	if h.serveCachedManifest(w, r, repo, route, acceptHash) {
		return
	}

	ctx, cancel := h.requestContext(r, upstream, true)
	defer cancel()
	headers := http.Header{}
	if accept := r.Header.Values("Accept"); len(accept) > 0 {
		headers["Accept"] = accept
	}
	conditionalTag := route.digest.Algorithm == ""
	if conditionalTag {
		if etag, err := h.Store.CachedTagETagForAccept(r.Context(), repo.ID, route.reference, acceptHash); err == nil && etag != "" {
			headers.Set("If-None-Match", etag)
		}
	}
	upstreamRepo := upstream.MapRepository(route.repository)
	path := "/v2/" + upstreamRepo + "/manifests/" + route.reference
	resp, err := upstream.Do(ctx, http.MethodGet, path, upstreamRepo, headers)
	if err != nil {
		h.Logger.Warn("upstream manifest request failed", "upstream", upstream.ID, "error", err)
		writeUpstreamError(w)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && conditionalTag {
		if err := h.Store.TouchTagForAccept(r.Context(), repo.ID, route.reference, acceptHash); err != nil {
			h.Logger.Error("refresh cached tag failed", "error", err)
			writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "internal server error", nil)
			return
		}
		if !h.serveCachedManifest(w, r, repo, route, acceptHash) {
			h.Logger.Error("conditional manifest hit without cached content", "upstream", upstream.ID, "repository", route.repository, "tag", route.reference)
			writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "cached manifest unavailable", nil)
		}
		return
	}
	if resp.StatusCode == http.StatusNotFound {
		writeNotFound(w, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeUpstreamError(w)
		return
	}
	manifestLimit := h.MaxManifestBytes
	if manifestLimit <= 0 {
		manifestLimit = 4 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, manifestLimit+1))
	if err != nil || int64(len(raw)) > manifestLimit {
		writeUpstreamError(w)
		return
	}
	mediaType := normalizeMediaType(resp.Header.Get("Content-Type"))
	manifest, err := oci.ParseManifest(raw, mediaType)
	if err != nil {
		h.Logger.Warn("invalid upstream manifest", "upstream", upstream.ID, "error", err)
		writeUpstreamError(w)
		return
	}
	mediaType = manifest.MediaType
	algorithm := "sha256"
	if route.digest.Algorithm != "" {
		algorithm = route.digest.Algorithm
	}
	digest, err := oci.FromBytes(algorithm, raw)
	if err != nil {
		writeUpstreamError(w)
		return
	}
	if h.Storage != nil {
		decision, decisionErr := h.Storage.CanCache(r.Context(), int64(len(raw)))
		if decisionErr != nil {
			h.Logger.Error("check cache capacity failed", "error", decisionErr)
			writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "internal server error", nil)
			return
		}
		if !decision.Cache {
			if !decision.Bypass {
				writeRegistryError(w, http.StatusInsufficientStorage, "UNKNOWN", "insufficient storage", nil)
				return
			}
			h.recordPull(r.Context(), "manifest.pull", repo, upstream.ID, digest.String(), int64(len(raw)), false, "success", "cache bypass")
			writeManifestResponse(w, r, mediaType, digest, int64(len(raw)), resp.Header.Get("ETag"), raw)
			return
		}
	}
	if err := h.CAS.PutBytes(digest, raw); err != nil {
		h.Logger.Error("cache manifest content failed", "error", err)
		writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "internal server error", nil)
		return
	}
	edges := manifestEdges(manifest)
	tag := ""
	if route.digest.Algorithm == "" {
		tag = route.reference
	}
	artifactType := ""
	if manifest.Subject != nil {
		artifactType = manifest.EffectiveArtifactType()
	}
	input := store.ManifestInput{
		Digest:       digest,
		MediaType:    mediaType,
		Size:         int64(len(raw)),
		Kind:         manifest.ContentKind(),
		Tag:          tag,
		AcceptHash:   acceptHash,
		ETag:         resp.Header.Get("ETag"),
		Subject:      manifest.Subject,
		ArtifactType: artifactType,
		Annotations:  manifest.Annotations,
		Edges:        edges,
	}
	if err := h.Store.PutManifest(r.Context(), repo.ID, input); err != nil {
		h.Logger.Error("cache manifest metadata failed", "error", err)
		writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "internal server error", nil)
		return
	}
	h.recordPull(r.Context(), "manifest.pull", repo, upstream.ID, digest.String(), int64(len(raw)), false, "success", "")
	h.recordCache(r.Context(), "manifest.cache", repo, upstream.ID, digest.String(), int64(len(raw)), "success", "")
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Vary", "Accept")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.Header().Set("Docker-Content-Digest", digest.String())
	if etag := resp.Header.Get("ETag"); etag != "" {
		w.Header().Set("ETag", etag)
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (h *Handler) serveCachedManifest(w http.ResponseWriter, r *http.Request, repo store.Repository, route parsedRoute, acceptHash string) bool {
	content, err := h.Store.ManifestByReferenceForAccept(r.Context(), repo.ID, route.reference, acceptHash)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			h.Logger.Warn("read cached manifest metadata failed", "error", err)
		}
		return false
	}
	if route.digest.Algorithm == "" {
		fresh, err := h.Store.CacheTagFreshForAccept(r.Context(), repo.ID, route.reference, acceptHash, int64(defaultTagTTL(h.upstreamByID(repo.Namespace)).Seconds()))
		if err != nil || !fresh {
			return false
		}
	}
	digest, err := oci.ParseDigest(content.Digest)
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
	h.recordPull(r.Context(), "manifest.pull", repo, repo.Namespace, digest.String(), content.Size, true, "success", "")
	_ = h.Store.TouchContent(context.WithoutCancel(r.Context()), digest.String())
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.Header().Set("Content-Type", content.MediaType)
	w.Header().Set("Vary", "Accept")
	w.Header().Set("Docker-Content-Digest", digest.String())
	w.Header().Set("Content-Length", strconv.FormatInt(content.Size, 10))
	if route.digest.Algorithm == "" {
		if etag, err := h.Store.CachedTagETagForAccept(r.Context(), repo.ID, route.reference, acceptHash); err == nil && etag != "" {
			w.Header().Set("ETag", etag)
		}
	}
	http.ServeContent(w, r, digest.Encoded, info.ModTime(), file)
	return true
}

func writeManifestResponse(w http.ResponseWriter, r *http.Request, mediaType string, digest oci.Digest, size int64, etag string, raw []byte) {
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Vary", "Accept")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Docker-Content-Digest", digest.String())
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func manifestAcceptHash(values []string) string {
	raw := strings.ToLower(strings.TrimSpace(strings.Join(values, ",")))
	if raw == "" {
		return "*"
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) upstreamByID(id string) *Upstream {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, upstream := range h.upstreams {
		if upstream.ID == id {
			return upstream
		}
	}
	return nil
}

func manifestEdges(manifest *oci.Manifest) []store.EdgeInput {
	var edges []store.EdgeInput
	if manifest.IsIndex() {
		for _, descriptor := range manifest.Manifests {
			digest, err := oci.ParseDigest(descriptor.Digest)
			if err == nil {
				edges = append(edges, store.EdgeInput{Digest: digest, Kind: "manifest"})
			}
		}
		return edges
	}
	for _, descriptor := range manifest.ReferencedDescriptors() {
		digest, err := oci.ParseDigest(descriptor.Digest)
		if err == nil {
			edges = append(edges, store.EdgeInput{Digest: digest, Kind: "blob"})
		}
	}
	return edges
}

func normalizeMediaType(value string) string {
	if value == "" {
		return ""
	}
	if mediaType, _, err := mime.ParseMediaType(value); err == nil {
		return mediaType
	}
	return strings.TrimSpace(value)
}
