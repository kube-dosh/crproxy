package registry

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kube-dosh/crproxy/internal/auth"
	"github.com/kube-dosh/crproxy/internal/cas"
	"github.com/kube-dosh/crproxy/internal/oci"
	"github.com/kube-dosh/crproxy/internal/store"
)

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request, route parsedRoute) {
	if route.uploadID == "" {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, "POST")
			return
		}
		identity, ok := h.authorize(w, r, route.repo, "push")
		if !ok {
			return
		}
		h.startUpload(w, r, route, identity)
		return
	}

	var identity *auth.Identity
	var ok bool
	switch r.Method {
	case http.MethodGet:
		identity, ok = h.authorizeAny(w, r, route.repo, []string{"pull", "push"})
		if !ok {
			return
		}
		h.uploadStatus(w, r, route, identity)
	case http.MethodPatch:
		identity, ok = h.authorize(w, r, route.repo, "push")
		if !ok {
			return
		}
		h.patchUpload(w, r, route, identity)
	case http.MethodPut:
		identity, ok = h.authorize(w, r, route.repo, "push")
		if !ok {
			return
		}
		h.completeUpload(w, r, route, identity)
	case http.MethodDelete:
		identity, ok = h.authorizeAny(w, r, route.repo, []string{"push", "delete"})
		if !ok {
			return
		}
		h.cancelUpload(w, r, route, identity)
	default:
		writeMethodNotAllowed(w, "GET, PATCH, PUT, DELETE")
	}
}

func (h *Handler) startUpload(w http.ResponseWriter, r *http.Request, route parsedRoute, identity *auth.Identity) {
	repo, err := h.Store.EnsureLocalRepository(r.Context(), route.repo)
	if err != nil {
		h.internalError(w, err)
		return
	}

	mount := r.URL.Query().Get("mount")
	from := r.URL.Query().Get("from")
	if mount != "" && from != "" {
		digest, err := oci.ParseDigest(mount)
		if err != nil {
			writeRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid mount digest", nil)
			return
		}
		if h.tryMount(r, repo, from, digest) {
			h.writeBlobCreated(w, route.repo, digest)
			return
		}
	}

	uploadID, err := h.createUpload(r, repo.ID, identity.UserID)
	if err != nil {
		h.internalError(w, err)
		return
	}
	digestValue := r.URL.Query().Get("digest")
	if digestValue == "" {
		h.writeUploadAccepted(w, route.repo, uploadID, 0)
		return
	}
	digest, err := oci.ParseDigest(digestValue)
	if err != nil {
		h.discardUpload(r, uploadID)
		writeRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest", nil)
		return
	}
	if !h.appendRequestBody(w, r, route.repo, uploadID, 0, -1, true) {
		h.discardUpload(r, uploadID)
		return
	}
	if h.finalizeUpload(w, r, route.repo, repo.ID, uploadID, digest) {
		h.writeBlobCreated(w, route.repo, digest)
	}
}

func (h *Handler) tryMount(r *http.Request, target store.Repository, sourceName string, digest oci.Digest) bool {
	if err := oci.ValidateRepository(sourceName); err != nil {
		return false
	}
	if _, err := h.Auth.Authorize(r.Context(), r, sourceName, "pull"); err != nil {
		return false
	}
	source, err := h.Store.RepositoryByName(r.Context(), "local", sourceName)
	if err != nil {
		return false
	}
	if _, err := h.Store.BlobContent(r.Context(), source.ID, digest); err != nil {
		return false
	}
	exists, err := h.CAS.BlobExists(digest)
	if err != nil || !exists {
		return false
	}
	return h.Store.MountBlob(r.Context(), source.ID, target.ID, digest) == nil
}

func (h *Handler) createUpload(r *http.Request, repositoryID int64, userID string) (string, error) {
	uploadID, err := cas.NewUploadID()
	if err != nil {
		return "", err
	}
	file, err := h.CAS.CreateUpload(uploadID)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = h.CAS.DeleteUpload(uploadID)
		return "", err
	}
	ttl := h.limits().UploadTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if err := h.Store.CreateUpload(r.Context(), uploadID, repositoryID, userID, time.Now().UTC().Add(ttl)); err != nil {
		_ = h.CAS.DeleteUpload(uploadID)
		return "", err
	}
	return uploadID, nil
}

func (h *Handler) loadUpload(w http.ResponseWriter, r *http.Request, route parsedRoute, identity *auth.Identity) (store.Upload, bool) {
	upload, err := h.Store.UploadByID(r.Context(), route.uploadID)
	if errors.Is(err, store.ErrNotFound) {
		writeUploadUnknown(w)
		return store.Upload{}, false
	}
	if err != nil {
		h.internalError(w, err)
		return store.Upload{}, false
	}
	repo, err := h.Store.RepositoryByName(r.Context(), "local", route.repo)
	if err != nil || repo.ID != upload.RepoID || upload.UserID != identity.UserID {
		writeUploadUnknown(w)
		return store.Upload{}, false
	}
	if !upload.ExpiresAt.After(time.Now().UTC()) {
		h.discardUpload(r, upload.ID)
		writeUploadUnknown(w)
		return store.Upload{}, false
	}
	return upload, true
}

func (h *Handler) uploadStatus(w http.ResponseWriter, r *http.Request, route parsedRoute, identity *auth.Identity) {
	upload, ok := h.loadUpload(w, r, route, identity)
	if !ok {
		return
	}
	size, err := h.CAS.UploadSize(upload.ID)
	if err != nil {
		h.internalError(w, err)
		return
	}
	h.writeUploadRange(w, route.repo, upload.ID, size)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) patchUpload(w http.ResponseWriter, r *http.Request, route parsedRoute, identity *auth.Identity) {
	upload, ok := h.loadUpload(w, r, route, identity)
	if !ok {
		return
	}
	current, err := h.CAS.UploadSize(upload.ID)
	if err != nil {
		h.internalError(w, err)
		return
	}
	expectedEnd, ok := h.validateContentRange(w, route.repo, upload.ID, r.Header.Get("Content-Range"), current, r.ContentLength)
	if !ok {
		return
	}
	if !h.appendRequestBody(w, r, route.repo, upload.ID, current, expectedEnd, false) {
		return
	}
	size, err := h.CAS.UploadSize(upload.ID)
	if err != nil {
		h.internalError(w, err)
		return
	}
	if err := h.Store.UpdateUploadSize(r.Context(), upload.ID, size); err != nil {
		h.internalError(w, err)
		return
	}
	h.writeUploadAccepted(w, route.repo, upload.ID, size)
}

func (h *Handler) completeUpload(w http.ResponseWriter, r *http.Request, route parsedRoute, identity *auth.Identity) {
	upload, ok := h.loadUpload(w, r, route, identity)
	if !ok {
		return
	}
	digestValue := r.URL.Query().Get("digest")
	if digestValue == "" {
		writeRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest query parameter is required", nil)
		return
	}
	digest, err := oci.ParseDigest(digestValue)
	if err != nil {
		writeRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest", nil)
		return
	}
	current, err := h.CAS.UploadSize(upload.ID)
	if err != nil {
		h.internalError(w, err)
		return
	}
	expectedEnd, ok := h.validateContentRange(w, route.repo, upload.ID, r.Header.Get("Content-Range"), current, r.ContentLength)
	if !ok {
		return
	}
	if r.ContentLength != 0 && !h.appendRequestBody(w, r, route.repo, upload.ID, current, expectedEnd, false) {
		return
	}
	if h.finalizeUpload(w, r, route.repo, upload.RepoID, upload.ID, digest) {
		h.writeBlobCreated(w, route.repo, digest)
	}
}

func (h *Handler) finalizeUpload(w http.ResponseWriter, r *http.Request, repositoryName string, repositoryID int64, uploadID string, digest oci.Digest) bool {
	file, err := h.CAS.OpenUpload(uploadID, false)
	if err != nil {
		h.internalError(w, err)
		return false
	}
	actual, size, err := oci.FromReader(digest.Algorithm, file)
	_ = file.Close()
	if err != nil {
		h.internalError(w, err)
		return false
	}
	if actual != digest {
		h.writeUploadRange(w, repositoryName, uploadID, size)
		writeRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "uploaded content does not match digest", nil)
		return false
	}
	if h.Storage != nil {
		if err := h.Storage.CheckLocalWriteForContent(r.Context(), size, digest.String()); err != nil {
			writeRegistryError(w, http.StatusInsufficientStorage, "UNKNOWN", "insufficient storage", nil)
			return false
		}
	}
	if err := h.CAS.CommitUpload(uploadID, digest); err != nil {
		h.internalError(w, err)
		return false
	}
	if err := h.Store.CommitUploadedBlob(r.Context(), uploadID, repositoryID, digest, size, "application/octet-stream"); err != nil {
		h.internalError(w, err)
		return false
	}
	return true
}

func (h *Handler) cancelUpload(w http.ResponseWriter, r *http.Request, route parsedRoute, identity *auth.Identity) {
	upload, ok := h.loadUpload(w, r, route, identity)
	if !ok {
		return
	}
	h.discardUpload(r, upload.ID)
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) discardUpload(r *http.Request, uploadID string) {
	_ = h.Store.DeleteUpload(r.Context(), uploadID)
	_ = h.CAS.DeleteUpload(uploadID)
}

func (h *Handler) appendRequestBody(w http.ResponseWriter, r *http.Request, repository, uploadID string, current, expectedEnd int64, monolithic bool) bool {
	if h.Storage != nil && r.ContentLength > 0 {
		if err := h.Storage.CheckLocalWrite(r.Context(), r.ContentLength); err != nil {
			writeRegistryError(w, http.StatusInsufficientStorage, "UNKNOWN", "insufficient storage", nil)
			return false
		}
	}
	limit := h.limits().MaxBlobBytes
	if limit <= 0 {
		limit = 20 << 30
	}
	n, tooLarge, err := h.CAS.AppendUploadLimited(uploadID, r.Body, limit)
	if err != nil {
		h.internalError(w, err)
		return false
	}
	if tooLarge {
		if monolithic {
			h.discardUpload(r, uploadID)
		}
		writeRegistryError(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", "blob exceeds configured size limit", nil)
		return false
	}
	end := current + n - 1
	if current+n == 0 {
		end = 0
	}
	if expectedEnd >= current && expectedEnd != end {
		_ = h.CAS.TruncateUpload(uploadID, current)
		h.writeUploadRange(w, repository, uploadID, current)
		writeRegistryError(w, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID", "content range does not match request body", nil)
		return false
	}
	if !monolithic {
		if err := h.Store.UpdateUploadSize(r.Context(), uploadID, current+n); err != nil {
			h.internalError(w, err)
			return false
		}
	}
	return true
}

func (h *Handler) validateContentRange(w http.ResponseWriter, repository, uploadID, raw string, current, contentLength int64) (int64, bool) {
	if raw == "" {
		return -1, true
	}
	start, end, err := parseContentRange(raw)
	if err != nil || start != current || end < start {
		h.writeUploadRange(w, repository, uploadID, current)
		writeRegistryError(w, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID", "invalid upload content range", map[string]any{"offset": current})
		return 0, false
	}
	if contentLength >= 0 && contentLength != end-start+1 {
		h.writeUploadRange(w, repository, uploadID, current)
		writeRegistryError(w, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID", "content range does not match content length", map[string]any{"offset": current})
		return 0, false
	}
	return end, true
}

func parseContentRange(raw string) (int64, int64, error) {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "bytes=")
	value = strings.TrimPrefix(value, "bytes ")
	startValue, endValue, ok := strings.Cut(value, "-")
	if !ok {
		return 0, 0, fmt.Errorf("invalid content range")
	}
	if slash := strings.IndexByte(endValue, '/'); slash >= 0 {
		endValue = endValue[:slash]
	}
	start, err := strconv.ParseInt(startValue, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid content range start")
	}
	end, err := strconv.ParseInt(endValue, 10, 64)
	if err != nil || end < start {
		return 0, 0, fmt.Errorf("invalid content range end")
	}
	return start, end, nil
}

func (h *Handler) writeUploadAccepted(w http.ResponseWriter, repository, uploadID string, size int64) {
	h.writeUploadRange(w, repository, uploadID, size)
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) writeUploadRange(w http.ResponseWriter, repository, uploadID string, size int64) {
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	if repository != "" && uploadID != "" {
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repository, uploadID))
		w.Header().Set("Docker-Upload-UUID", uploadID)
	}
	w.Header().Set("Range", uploadRange(size))
}

func (h *Handler) writeBlobCreated(w http.ResponseWriter, repository string, digest oci.Digest) {
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repository, digest.String()))
	w.Header().Set("Docker-Content-Digest", digest.String())
	w.WriteHeader(http.StatusCreated)
}

func uploadRange(size int64) string {
	if size <= 0 {
		return "0-0"
	}
	return fmt.Sprintf("0-%d", size-1)
}

func writeUploadUnknown(w http.ResponseWriter) {
	writeRegistryError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "blob upload unknown to registry", nil)
}
