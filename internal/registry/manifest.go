package registry

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"crproxy/internal/oci"
	"crproxy/internal/store"
)

func (h *Handler) handleManifest(w http.ResponseWriter, r *http.Request, route parsedRoute) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if _, ok := h.authorize(w, r, route.repo, "pull"); !ok {
			return
		}
		h.getManifest(w, r, route)
	case http.MethodPut:
		if _, ok := h.authorize(w, r, route.repo, "push"); !ok {
			return
		}
		h.putManifest(w, r, route)
	case http.MethodDelete:
		if _, ok := h.authorize(w, r, route.repo, "delete"); !ok {
			return
		}
		h.deleteManifest(w, r, route)
	default:
		writeMethodNotAllowed(w, "GET, HEAD, PUT, DELETE")
	}
}

func (h *Handler) getManifest(w http.ResponseWriter, r *http.Request, route parsedRoute) {
	repo, err := h.repositoryForRead(r.Context(), route.repo)
	if errors.Is(err, store.ErrNotFound) {
		writeNotFound(w, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	content, err := h.Store.ManifestByReference(r.Context(), repo.ID, route.ref)
	if errors.Is(err, store.ErrNotFound) {
		writeNotFound(w, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	digest, err := oci.ParseDigest(content.Digest)
	if err != nil {
		h.internalError(w, err)
		return
	}
	file, info, err := h.CAS.OpenBlob(digest)
	if err != nil {
		h.internalError(w, fmt.Errorf("manifest content missing: %w", err))
		return
	}
	defer file.Close()
	if info.Size() != content.Size {
		h.internalError(w, fmt.Errorf("manifest size mismatch for %s: metadata=%d file=%d", digest, content.Size, info.Size()))
		return
	}
	mediaType := content.MediaType
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", digest.String())
	w.Header().Set("Vary", "Accept")
	http.ServeContent(w, r, digest.Encoded, info.ModTime(), file)
}

func (h *Handler) putManifest(w http.ResponseWriter, r *http.Request, route parsedRoute) {
	if strings.Contains(route.ref, ":") && route.digest.Algorithm == "" {
		writeRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest", nil)
		return
	}
	raw, tooLarge, err := readLimited(r.Body, h.limits().MaxManifestBytes)
	if err != nil {
		h.internalError(w, err)
		return
	}
	if tooLarge {
		writeRegistryError(w, http.StatusRequestEntityTooLarge, "MANIFEST_INVALID", "manifest too large", nil)
		return
	}
	manifest, err := oci.ParseManifest(raw, r.Header.Get("Content-Type"))
	if err != nil {
		writeRegistryError(w, http.StatusBadRequest, "MANIFEST_INVALID", err.Error(), nil)
		return
	}
	algorithm := "sha256"
	if route.digest.Algorithm != "" {
		algorithm = route.digest.Algorithm
	}
	digest, err := oci.FromBytes(algorithm, raw)
	if err != nil {
		h.internalError(w, err)
		return
	}
	if route.digest.Algorithm != "" && digest != route.digest {
		writeRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "manifest digest mismatch", nil)
		return
	}
	repo, err := h.Store.EnsureLocalRepository(r.Context(), route.repo)
	if err != nil {
		h.internalError(w, err)
		return
	}
	edges, missing, err := h.validateManifestReferences(r, repo.ID, manifest)
	if err != nil {
		h.internalError(w, err)
		return
	}
	if missing != "" {
		writeRegistryError(w, http.StatusBadRequest, "MANIFEST_BLOB_UNKNOWN", "manifest references unknown content", map[string]any{"digest": missing})
		return
	}
	if h.Storage != nil {
		if err := h.Storage.CheckLocalWrite(r.Context(), int64(len(raw))); err != nil {
			writeRegistryError(w, http.StatusInsufficientStorage, "UNKNOWN", "insufficient storage", nil)
			return
		}
	}
	if err := h.CAS.PutBytes(digest, raw); err != nil {
		h.internalError(w, err)
		return
	}
	tag := ""
	if route.digest.Algorithm == "" {
		tag = route.ref
	}
	artifactType := ""
	if manifest.Subject != nil {
		artifactType = manifest.EffectiveArtifactType()
	}
	input := store.ManifestInput{
		Digest:       digest,
		MediaType:    manifest.MediaType,
		Size:         int64(len(raw)),
		Kind:         manifest.ContentKind(),
		Tag:          tag,
		Subject:      manifest.Subject,
		ArtifactType: artifactType,
		Annotations:  manifest.Annotations,
		Edges:        edges,
	}
	if err := h.Store.PutManifest(r.Context(), repo.ID, input); err != nil {
		h.internalError(w, err)
		return
	}
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	w.Header().Set("Location", "/v2/"+route.repo+"/manifests/"+digest.String())
	w.Header().Set("Docker-Content-Digest", digest.String())
	if manifest.Subject != nil {
		w.Header().Set("OCI-Subject", manifest.Subject.Digest)
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) validateManifestReferences(r *http.Request, repositoryID int64, manifest *oci.Manifest) ([]store.EdgeInput, string, error) {
	var edges []store.EdgeInput
	if manifest.IsIndex() {
		for _, descriptor := range manifest.Manifests {
			digest, err := oci.ParseDigest(descriptor.Digest)
			if err != nil {
				return nil, "", err
			}
			exists, err := h.Store.ManifestRefExists(r.Context(), repositoryID, digest)
			if err != nil {
				return nil, "", err
			}
			if !exists {
				return nil, descriptor.Digest, nil
			}
			edges = append(edges, store.EdgeInput{Digest: digest, Kind: "manifest"})
		}
		return edges, "", nil
	}
	for _, descriptor := range manifest.ReferencedDescriptors() {
		digest, err := oci.ParseDigest(descriptor.Digest)
		if err != nil {
			return nil, "", err
		}
		if len(descriptor.URLs) > 0 {
			edges = append(edges, store.EdgeInput{Digest: digest, Kind: "blob"})
			continue
		}
		exists, err := h.Store.BlobRefExists(r.Context(), repositoryID, digest)
		if err != nil {
			return nil, "", err
		}
		if !exists {
			return nil, descriptor.Digest, nil
		}
		edges = append(edges, store.EdgeInput{Digest: digest, Kind: "blob"})
	}
	return edges, "", nil
}

func (h *Handler) deleteManifest(w http.ResponseWriter, r *http.Request, route parsedRoute) {
	repo, err := h.repositoryForRead(r.Context(), route.repo)
	if errors.Is(err, store.ErrNotFound) {
		writeNotFound(w, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	var found bool
	if strings.Contains(route.ref, ":") {
		digest, err := oci.ParseDigest(route.ref)
		if err != nil {
			writeRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest", nil)
			return
		}
		found, err = h.Store.DeleteManifest(r.Context(), repo.ID, digest)
		if err != nil {
			h.internalError(w, err)
			return
		}
	} else {
		found, err = h.Store.DeleteTag(r.Context(), repo.ID, route.ref)
		if err != nil {
			h.internalError(w, err)
			return
		}
	}
	if !found {
		writeNotFound(w, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	w.WriteHeader(http.StatusAccepted)
}
