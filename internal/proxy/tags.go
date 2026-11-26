package proxy

import (
	"encoding/json"
	"io"
	"net/http"
)

func (h *Handler) serveTags(w http.ResponseWriter, r *http.Request, upstream *Upstream, route parsedRoute) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, "GET")
		return
	}
	ctx, cancel := h.requestContext(r, upstream, true)
	defer cancel()
	upstreamRepo := upstream.MapRepository(route.repository)
	path := "/v2/" + upstreamRepo + "/tags/list"
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	headers := http.Header{"Accept": []string{"application/json"}}
	resp, err := upstream.Do(ctx, http.MethodGet, path, upstreamRepo, headers)
	if err != nil {
		h.Logger.Warn("upstream tags request failed", "upstream", upstream.ID, "error", err)
		writeUpstreamError(w)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		writeNotFound(w, "NAME_UNKNOWN", "repository unknown")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeUpstreamError(w)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		writeUpstreamError(w)
		return
	}
	var payload struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeUpstreamError(w)
		return
	}
	if payload.Tags == nil {
		payload.Tags = []string{}
	}
	payload.Name = route.clientRepo
	if link := rewriteNextLink(resp.Header.Get("Link"), "/v2/"+route.clientRepo+"/tags/list"); link != "" {
		w.Header().Set("Link", link)
	}
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
