package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"crproxy/internal/auth"
	"crproxy/internal/cas"
	"crproxy/internal/oci"
	"crproxy/internal/storage"
	"crproxy/internal/store"
)

type Handler struct {
	Local            http.Handler
	Store            *store.Store
	CAS              *cas.Store
	Auth             *auth.Authenticator
	PathPrefix       string
	MaxManifestBytes int64
	Logger           *slog.Logger
	Storage          *storage.Manager

	mu        sync.RWMutex
	upstreams []*Upstream
	byPrefix  map[string]*Upstream
	byHost    map[string]*Upstream
	flights   flightGroup
}

type parsedRoute struct {
	kind       string
	repository string
	clientRepo string
	reference  string
	digest     oci.Digest
}

func NewHandler(local http.Handler, metadata *store.Store, casStore *cas.Store, authenticator *auth.Authenticator, upstreams []*Upstream, pathPrefix string, maxManifestBytes int64, logger *slog.Logger) (*Handler, error) {
	if pathPrefix == "" {
		pathPrefix = "r"
	}
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{
		Local:            local,
		Store:            metadata,
		CAS:              casStore,
		Auth:             authenticator,
		PathPrefix:       pathPrefix,
		MaxManifestBytes: maxManifestBytes,
		Logger:           logger,
	}
	if err := h.ReplaceUpstreams(upstreams); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Handler) ReplaceUpstreams(upstreams []*Upstream) error {
	byPrefix := map[string]*Upstream{}
	byHost := map[string]*Upstream{}
	for _, upstream := range upstreams {
		if upstream == nil {
			continue
		}
		if upstream.RoutePrefix == "" {
			upstream.RoutePrefix = upstream.ID
		}
		if byPrefix[upstream.RoutePrefix] != nil {
			return fmt.Errorf("duplicate proxy route prefix %q", upstream.RoutePrefix)
		}
		byPrefix[upstream.RoutePrefix] = upstream
		if upstream.RouteHost != "" {
			host := normalizeHost(upstream.RouteHost)
			if byHost[host] != nil {
				return fmt.Errorf("duplicate proxy route host %q", upstream.RouteHost)
			}
			byHost[host] = upstream
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.upstreams = append([]*Upstream(nil), upstreams...)
	h.byPrefix = byPrefix
	h.byHost = byHost
	return nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v2" || r.URL.Path == "/v2/" {
		h.serveBase(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v2/") {
		h.Local.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v2/"+h.PathPrefix+"/") {
		rest := strings.TrimPrefix(r.URL.Path, "/v2/"+h.PathPrefix+"/")
		slug, tail, ok := strings.Cut(rest, "/")
		if !ok {
			if !isReadMethod(r.Method) {
				writeMethodNotAllowed(w, "GET, HEAD")
				return
			}
			writeNotFound(w, "NAME_UNKNOWN", "proxy endpoint unknown")
			return
		}
		upstream := h.matchPrefix(slug)
		if upstream == nil {
			if !isReadMethod(r.Method) {
				writeMethodNotAllowed(w, "GET, HEAD")
				return
			}
			writeNotFound(w, "NAME_UNKNOWN", "upstream unknown")
			return
		}
		if !isReadMethod(r.Method) {
			writeMethodNotAllowed(w, "GET, HEAD")
			return
		}
		h.serveProxy(w, r, upstream, h.PathPrefix+"/"+slug+"/", tail)
		return
	}
	if upstream := h.matchHost(r.Host); upstream != nil {
		if !isReadMethod(r.Method) {
			writeMethodNotAllowed(w, "GET, HEAD")
			return
		}
		h.serveProxy(w, r, upstream, "", strings.TrimPrefix(r.URL.Path, "/v2/"))
		return
	}
	h.Local.ServeHTTP(w, r)
}

func (h *Handler) serveBase(w http.ResponseWriter, r *http.Request) {
	if !isReadMethod(r.Method) {
		writeMethodNotAllowed(w, "GET, HEAD")
		return
	}
	if _, err := h.Auth.AuthenticateBase(r.Context(), r); err != nil {
		writeUnauthorized(w, h.Auth.Challenge("", nil))
		return
	}
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) matchHost(host string) *Upstream {
	h.mu.RLock()
	defer h.mu.RUnlock()
	host = normalizeHost(host)
	if upstream := h.byHost[host]; upstream != nil {
		return upstream
	}
	if hostname := hostnameOnly(host); hostname != host {
		return h.byHost[hostname]
	}
	return nil
}

func (h *Handler) matchPrefix(prefix string) *Upstream {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byPrefix[prefix]
}

func (h *Handler) serveProxy(w http.ResponseWriter, r *http.Request, upstream *Upstream, clientPrefix, rest string) {
	route, err := parseProxyRoute(rest, clientPrefix)
	if err != nil {
		writeNotFound(w, "NAME_INVALID", "invalid repository or endpoint")
		return
	}
	if route.kind == "catalog" {
		writeNotFound(w, "NAME_UNKNOWN", "catalog is not available on proxy routes")
		return
	}
	if route.kind == "unsupported" {
		writeNotFound(w, "NAME_UNKNOWN", "endpoint is not available on proxy routes")
		return
	}
	if _, ok := h.authorizePull(w, r, route.clientRepo); !ok {
		return
	}
	switch route.kind {
	case "manifest":
		h.serveManifest(w, r, upstream, route)
	case "blob":
		h.serveBlob(w, r, upstream, route)
	case "tags":
		h.serveTags(w, r, upstream, route)
	case "referrers":
		h.serveReferrers(w, r, upstream, route)
	default:
		writeNotFound(w, "NAME_UNKNOWN", "proxy endpoint unknown")
	}
}

func parseProxyRoute(rest, clientPrefix string) (parsedRoute, error) {
	if rest == "" || rest == "/" {
		return parsedRoute{}, fmt.Errorf("empty repository")
	}
	rest = strings.TrimPrefix(rest, "/")
	if rest == "_catalog" {
		return parsedRoute{kind: "catalog"}, nil
	}
	if strings.HasSuffix(rest, "/tags/list") {
		repository := strings.TrimSuffix(rest, "/tags/list")
		if err := oci.ValidateRepository(repository); err != nil {
			return parsedRoute{}, err
		}
		return parsedRoute{kind: "tags", repository: repository, clientRepo: clientPrefix + repository}, nil
	}
	if idx := strings.LastIndex(rest, "/manifests/"); idx > 0 {
		repository := rest[:idx]
		reference := rest[idx+len("/manifests/"):]
		if err := oci.ValidateRepository(repository); err != nil || reference == "" || strings.Contains(reference, "/") {
			return parsedRoute{}, fmt.Errorf("invalid manifest path")
		}
		route := parsedRoute{kind: "manifest", repository: repository, clientRepo: clientPrefix + repository, reference: reference}
		if oci.IsDigestReference(reference) {
			digest, err := oci.ParseDigest(reference)
			if err != nil {
				return parsedRoute{}, err
			}
			route.digest = digest
		} else if err := oci.ValidateTag(reference); err != nil {
			return parsedRoute{}, err
		}
		return route, nil
	}
	if idx := strings.LastIndex(rest, "/referrers/"); idx > 0 {
		repository := rest[:idx]
		digest, err := oci.ParseDigest(rest[idx+len("/referrers/"):])
		if err != nil || oci.ValidateRepository(repository) != nil {
			return parsedRoute{}, fmt.Errorf("invalid referrers path")
		}
		return parsedRoute{kind: "referrers", repository: repository, clientRepo: clientPrefix + repository, digest: digest}, nil
	}
	if strings.Contains(rest, "/blobs/uploads") {
		return parsedRoute{kind: "unsupported"}, nil
	}
	if idx := strings.LastIndex(rest, "/blobs/"); idx > 0 {
		repository := rest[:idx]
		digest, err := oci.ParseDigest(rest[idx+len("/blobs/"):])
		if err != nil || oci.ValidateRepository(repository) != nil {
			return parsedRoute{}, fmt.Errorf("invalid blob path")
		}
		return parsedRoute{kind: "blob", repository: repository, clientRepo: clientPrefix + repository, digest: digest}, nil
	}
	return parsedRoute{}, fmt.Errorf("unknown route")
}

func (h *Handler) authorizePull(w http.ResponseWriter, r *http.Request, repository string) (*auth.Identity, bool) {
	identity, err := h.Auth.Authorize(r.Context(), r, repository, "pull")
	if err == nil {
		return identity, true
	}
	if errors.Is(err, auth.ErrForbidden) {
		writeDenied(w)
		return nil, false
	}
	writeUnauthorized(w, h.Auth.Challenge(repository, []string{"pull"}))
	return nil, false
}

func (h *Handler) requestContext(r *http.Request, upstream *Upstream, timeout bool) (context.Context, context.CancelFunc) {
	if !timeout || upstream.RequestTimeout <= 0 {
		return r.Context(), func() {}
	}
	return context.WithTimeout(r.Context(), upstream.RequestTimeout)
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func hostnameOnly(host string) string {
	if value, _, err := net.SplitHostPort(host); err == nil {
		return value
	}
	return host
}

func defaultTagTTL(upstream *Upstream) time.Duration {
	if upstream == nil || upstream.TagTTL <= 0 {
		return time.Minute
	}
	return upstream.TagTTL
}
