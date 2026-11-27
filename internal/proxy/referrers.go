package proxy

import (
	"io"
	"net/http"

	"crproxy/internal/oci"
)

func (h *Handler) serveReferrers(w http.ResponseWriter, r *http.Request, upstream *Upstream, route parsedRoute) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, "GET")
		return
	}
	ctx, cancel := h.requestContext(r, upstream, true)
	defer cancel()
	upstreamRepo := upstream.MapRepository(route.repository)
	path := "/v2/" + upstreamRepo + "/referrers/" + route.digest.String()
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	headers := http.Header{"Accept": []string{oci.MediaTypeOCIIndex}}
	resp, err := upstream.Do(ctx, http.MethodGet, path, upstreamRepo, headers)
	if err != nil {
		h.Logger.Warn("upstream referrers request failed", "upstream", upstream.ID, "error", err)
		writeUpstreamError(w)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		writeNotFound(w, "MANIFEST_UNKNOWN", "referrers unknown")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeUpstreamError(w)
		return
	}
	limit := h.maxManifestBytes()
	if limit <= 0 {
		limit = 4 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		writeUpstreamError(w)
		return
	}
	if link := rewriteNextLink(resp.Header.Get("Link"), "/v2/"+route.clientRepo+"/referrers/"+route.digest.String()); link != "" {
		w.Header().Set("Link", link)
	}
	if filter := resp.Header.Get("OCI-Filters-Applied"); filter != "" {
		w.Header().Set("OCI-Filters-Applied", filter)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = oci.MediaTypeOCIIndex
	}
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
