package registry

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"crproxy/internal/oci"
	"crproxy/internal/store"
)

func (h *Handler) handleReferrers(w http.ResponseWriter, r *http.Request, route parsedRoute) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, "GET")
		return
	}
	if _, ok := h.authorize(w, r, route.repo, "pull"); !ok {
		return
	}
	repo, err := h.repositoryForRead(r.Context(), route.repo)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.internalError(w, err)
		return
	}
	limit, err := pageLimit(r.URL.Query().Get("n"))
	if err != nil {
		writeRegistryError(w, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID", "invalid page size", nil)
		return
	}
	artifactType := r.URL.Query().Get("artifactType")
	last := r.URL.Query().Get("last")
	var referrers []store.Referrer
	if err == nil {
		referrers, err = h.Store.ListReferrers(r.Context(), repo.ID, route.digest.String(), artifactType, last, limit+1)
		if err != nil {
			h.internalError(w, err)
			return
		}
	}
	descriptors := make([]oci.Descriptor, 0, len(referrers))
	for _, referrer := range referrers {
		descriptors = append(descriptors, oci.Descriptor{
			MediaType:    referrer.MediaType,
			Digest:       referrer.Digest,
			Size:         referrer.Size,
			ArtifactType: referrer.ArtifactType,
			Annotations:  referrer.Annotations,
		})
	}
	if len(descriptors) > limit {
		descriptors = descriptors[:limit]
		lastDescriptor := descriptors[len(descriptors)-1]
		link := fmt.Sprintf(`</v2/%s/referrers/%s?n=%d&last=%s`, route.repo, route.digest.String(), limit, url.QueryEscape(lastDescriptor.Digest))
		if artifactType != "" {
			link += "&artifactType=" + url.QueryEscape(artifactType)
		}
		link += `>; rel="next"`
		w.Header().Set("Link", link)
	}
	if artifactType != "" {
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	w.Header().Set("Content-Type", oci.MediaTypeOCIIndex)
	_ = writeJSON(w, map[string]any{
		"schemaVersion": 2,
		"mediaType":     oci.MediaTypeOCIIndex,
		"manifests":     descriptors,
	})
}
