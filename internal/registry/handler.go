package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"crproxy/internal/auth"
	"crproxy/internal/cas"
	"crproxy/internal/oci"
	"crproxy/internal/storage"
	"crproxy/internal/store"
)

const distributionVersion = "registry/2.0"

type Handler struct {
	Store            *store.Store
	CAS              *cas.Store
	Auth             *auth.Authenticator
	MaxManifestBytes int64
	MaxBlobBytes     int64
	UploadTTL        time.Duration
	Storage          *storage.Manager
	Logger           *slog.Logger
	runtimeLimits    atomic.Pointer[RuntimeLimits]
}

type RuntimeLimits struct {
	MaxManifestBytes int64
	MaxBlobBytes     int64
	UploadTTL        time.Duration
}

func (h *Handler) ApplyRuntimeLimits(limits RuntimeLimits) {
	h.runtimeLimits.Store(&limits)
}

func (h *Handler) limits() RuntimeLimits {
	if dynamic := h.runtimeLimits.Load(); dynamic != nil {
		return *dynamic
	}
	return RuntimeLimits{MaxManifestBytes: h.MaxManifestBytes, MaxBlobBytes: h.MaxBlobBytes, UploadTTL: h.UploadTTL}
}

type parsedRoute struct {
	kind     string
	repo     string
	ref      string
	uploadID string
	digest   oci.Digest
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Logger == nil {
		h.Logger = slog.Default()
	}
	if r.URL.Path == "/v2" || r.URL.Path == "/v2/" {
		h.handleBase(w, r)
		return
	}
	route, err := parseRoute(r.URL.Path)
	if err != nil {
		writeNotFound(w, "NAME_INVALID", "invalid repository or endpoint")
		return
	}
	switch route.kind {
	case "catalog":
		h.handleCatalog(w, r)
	case "tags":
		h.handleTags(w, r, route.repo)
	case "manifest":
		h.handleManifest(w, r, route)
	case "blob":
		h.handleBlob(w, r, route)
	case "upload":
		h.handleUpload(w, r, route)
	case "referrers":
		h.handleReferrers(w, r, route)
	default:
		writeNotFound(w, "NAME_UNKNOWN", "unknown registry endpoint")
	}
}

func (h *Handler) handleBase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeMethodNotAllowed(w, "GET, HEAD")
		return
	}
	if _, err := h.Auth.AuthenticateBase(r.Context(), r); err != nil {
		w.Header().Set("WWW-Authenticate", h.Auth.Challenge("", nil))
		writeUnauthorized(w, h.Auth.Challenge("", nil))
		return
	}
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, repository, action string) (*auth.Identity, bool) {
	identity, err := h.Auth.Authorize(r.Context(), r, repository, action)
	if err == nil {
		return identity, true
	}
	if errors.Is(err, auth.ErrForbidden) {
		writeDenied(w)
		return nil, false
	}
	actions := []string{action}
	if action == "catalog" {
		actions = nil
	}
	writeUnauthorized(w, h.Auth.Challenge(repository, actions))
	return nil, false
}

func (h *Handler) authorizeAny(w http.ResponseWriter, r *http.Request, repository string, actions []string) (*auth.Identity, bool) {
	unauthenticated := false
	for _, action := range actions {
		identity, err := h.Auth.Authorize(r.Context(), r, repository, action)
		if err == nil {
			return identity, true
		}
		if !errors.Is(err, auth.ErrForbidden) {
			unauthenticated = true
		}
	}
	if unauthenticated {
		writeUnauthorized(w, h.Auth.Challenge(repository, actions))
		return nil, false
	}
	writeDenied(w)
	return nil, false
}

func (h *Handler) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, "GET")
		return
	}
	if _, ok := h.authorize(w, r, "", "catalog"); !ok {
		return
	}
	limit, err := pageLimit(r.URL.Query().Get("n"))
	if err != nil {
		writeRegistryError(w, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID", "invalid page size", nil)
		return
	}
	last := r.URL.Query().Get("last")
	names, err := h.Store.ListLocalRepositoryNames(r.Context(), last, limit+1)
	if err != nil {
		h.internalError(w, err)
		return
	}
	if names == nil {
		names = []string{}
	}
	link := ""
	if len(names) > limit {
		names = names[:limit]
		link = fmt.Sprintf(`</v2/_catalog?n=%d&last=%s>; rel="next"`, limit, url.QueryEscape(names[len(names)-1]))
	}
	if link != "" {
		w.Header().Set("Link", link)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	_ = writeJSON(w, map[string]any{"repositories": names})
}

func (h *Handler) handleTags(w http.ResponseWriter, r *http.Request, repository string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, "GET")
		return
	}
	if _, ok := h.authorize(w, r, repository, "pull"); !ok {
		return
	}
	repo, err := h.Store.RepositoryByName(r.Context(), "local", repository)
	if err != nil {
		writeNotFound(w, "NAME_UNKNOWN", "repository unknown")
		return
	}
	limit, err := pageLimit(r.URL.Query().Get("n"))
	if err != nil {
		writeRegistryError(w, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID", "invalid page size", nil)
		return
	}
	last := r.URL.Query().Get("last")
	tags, err := h.Store.ListTags(r.Context(), repo.ID, last, limit+1)
	if err != nil {
		h.internalError(w, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	if len(tags) > limit {
		tags = tags[:limit]
		w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?n=%d&last=%s>; rel="next"`, repository, limit, url.QueryEscape(tags[len(tags)-1])))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-Api-Version", distributionVersion)
	_ = writeJSON(w, map[string]any{"name": repository, "tags": tags})
}

func parseRoute(path string) (parsedRoute, error) {
	if !strings.HasPrefix(path, "/v2/") {
		return parsedRoute{}, fmt.Errorf("not a v2 path")
	}
	rest := strings.TrimPrefix(path, "/v2/")
	if rest == "_catalog" {
		return parsedRoute{kind: "catalog"}, nil
	}
	if strings.HasSuffix(rest, "/tags/list") {
		repository := strings.TrimSuffix(rest, "/tags/list")
		if err := oci.ValidateRepository(repository); err != nil {
			return parsedRoute{}, err
		}
		return parsedRoute{kind: "tags", repo: repository}, nil
	}
	if idx := strings.LastIndex(rest, "/manifests/"); idx > 0 {
		repository := rest[:idx]
		reference := rest[idx+len("/manifests/"):]
		if err := oci.ValidateRepository(repository); err != nil || reference == "" || strings.Contains(reference, "/") {
			return parsedRoute{}, fmt.Errorf("invalid manifest path")
		}
		if oci.IsDigestReference(reference) {
			digest, err := oci.ParseDigest(reference)
			if err != nil {
				return parsedRoute{kind: "manifest", repo: repository, ref: reference}, nil
			}
			return parsedRoute{kind: "manifest", repo: repository, ref: reference, digest: digest}, nil
		}
		if err := oci.ValidateTag(reference); err != nil {
			return parsedRoute{}, err
		}
		return parsedRoute{kind: "manifest", repo: repository, ref: reference}, nil
	}
	if idx := strings.LastIndex(rest, "/referrers/"); idx > 0 {
		repository := rest[:idx]
		digest, err := oci.ParseDigest(rest[idx+len("/referrers/"):])
		if err != nil || oci.ValidateRepository(repository) != nil {
			return parsedRoute{}, fmt.Errorf("invalid referrers path")
		}
		return parsedRoute{kind: "referrers", repo: repository, digest: digest}, nil
	}
	if idx := strings.LastIndex(rest, "/blobs/uploads/"); idx > 0 {
		repository := rest[:idx]
		uploadID := rest[idx+len("/blobs/uploads/"):]
		if err := oci.ValidateRepository(repository); err != nil || strings.Contains(uploadID, "/") {
			return parsedRoute{}, fmt.Errorf("invalid upload path")
		}
		return parsedRoute{kind: "upload", repo: repository, uploadID: uploadID}, nil
	}
	if strings.HasSuffix(rest, "/blobs/uploads") {
		repository := strings.TrimSuffix(rest, "/blobs/uploads")
		if err := oci.ValidateRepository(repository); err != nil {
			return parsedRoute{}, err
		}
		return parsedRoute{kind: "upload", repo: repository}, nil
	}
	if idx := strings.LastIndex(rest, "/blobs/"); idx > 0 {
		repository := rest[:idx]
		digest, err := oci.ParseDigest(rest[idx+len("/blobs/"):])
		if err != nil || oci.ValidateRepository(repository) != nil {
			return parsedRoute{}, fmt.Errorf("invalid blob path")
		}
		return parsedRoute{kind: "blob", repo: repository, digest: digest}, nil
	}
	return parsedRoute{}, fmt.Errorf("unknown route")
}

func pageLimit(raw string) (int, error) {
	if raw == "" {
		return 100, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid page size")
	}
	if n > 1000 {
		n = 1000
	}
	return n, nil
}

func (h *Handler) internalError(w http.ResponseWriter, err error) {
	if h.Storage != nil && errors.Is(err, syscall.ENOSPC) {
		writeRegistryError(w, http.StatusInsufficientStorage, "UNKNOWN", "insufficient storage", nil)
		return
	}
	h.Logger.Error("registry request failed", "error", err)
	writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "internal server error", nil)
}

func (h *Handler) repositoryForRead(ctx context.Context, name string) (store.Repository, error) {
	return h.Store.RepositoryByName(ctx, "local", name)
}
