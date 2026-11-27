package adminapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kube-dosh/crproxy/internal/auth"
	"github.com/kube-dosh/crproxy/internal/cas"
	"github.com/kube-dosh/crproxy/internal/jobs"
	"github.com/kube-dosh/crproxy/internal/requestinfo"
	"github.com/kube-dosh/crproxy/internal/runtimeconfig"
	"github.com/kube-dosh/crproxy/internal/storage"
	"github.com/kube-dosh/crproxy/internal/store"
	"github.com/kube-dosh/crproxy/internal/upstreammgr"
)

type Config struct {
	CSRFSecret      []byte
	SessionTTL      time.Duration
	SessionIdleTTL  time.Duration
	SecureCookies   bool
	Version         string
	StartedAt       time.Time
	Upstreams       *upstreammgr.Manager
	Storage         *storage.Manager
	Jobs            *jobs.Runner
	CAS             *cas.Store
	AuditRetention  time.Duration
	RequestInfo     *requestinfo.Resolver
	PublicURL       string
	RegistryURL     string
	DeploymentMode  string
	RegistryListen  string
	AdminListen     string
	ProxyPathPrefix string
	AllowAnonymous  bool
	Runtime         *runtimeconfig.Manager
}

type Handler struct {
	Store             *store.Store
	Logger            *slog.Logger
	csrfSecret        []byte
	sessionTTL        time.Duration
	sessionIdle       time.Duration
	secureCookie      bool
	dummyPasswordHash string
	version           string
	startedAt         time.Time
	limiter           *loginLimiter
	upstreams         *upstreammgr.Manager
	storage           *storage.Manager
	jobs              *jobs.Runner
	cas               *cas.Store
	auditRetention    time.Duration
	requestInfo       *requestinfo.Resolver
	publicURL         string
	registryURL       string
	deploymentMode    string
	registryListen    string
	adminListen       string
	proxyPrefix       string
	allowAnonymous    bool
	runtime           *runtimeconfig.Manager
	mux               *http.ServeMux
}

func NewHandler(metadata *store.Store, cfg Config, logger *slog.Logger) (*Handler, error) {
	if metadata == nil {
		return nil, errors.New("admin API store is required")
	}
	if cfg.Upstreams == nil {
		return nil, errors.New("admin API upstream manager is required")
	}
	if cfg.Storage == nil || cfg.Jobs == nil || cfg.CAS == nil {
		return nil, errors.New("admin API storage manager, job runner and CAS are required")
	}
	if len(cfg.CSRFSecret) < 32 {
		return nil, errors.New("admin API CSRF secret must be at least 32 bytes")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	if cfg.SessionIdleTTL <= 0 {
		cfg.SessionIdleTTL = 30 * time.Minute
	}
	if cfg.SessionIdleTTL > cfg.SessionTTL {
		cfg.SessionIdleTTL = cfg.SessionTTL
	}
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = time.Now().UTC()
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.RequestInfo == nil {
		resolver, err := requestinfo.New(nil, "http")
		if err != nil {
			return nil, err
		}
		cfg.RequestInfo = resolver
	}
	h := &Handler{
		Store:             metadata,
		Logger:            logger,
		csrfSecret:        append([]byte(nil), cfg.CSRFSecret...),
		sessionTTL:        cfg.SessionTTL,
		sessionIdle:       cfg.SessionIdleTTL,
		secureCookie:      cfg.SecureCookies,
		dummyPasswordHash: auth.DummyPasswordHash(),
		version:           cfg.Version,
		startedAt:         cfg.StartedAt.UTC(),
		limiter:           newLoginLimiter(),
		upstreams:         cfg.Upstreams,
		storage:           cfg.Storage,
		jobs:              cfg.Jobs,
		cas:               cfg.CAS,
		auditRetention:    cfg.AuditRetention,
		requestInfo:       cfg.RequestInfo,
		publicURL:         strings.TrimRight(cfg.PublicURL, "/"),
		registryURL:       strings.TrimRight(cfg.RegistryURL, "/"),
		deploymentMode:    cfg.DeploymentMode,
		registryListen:    cfg.RegistryListen,
		adminListen:       cfg.AdminListen,
		proxyPrefix:       cfg.ProxyPathPrefix,
		allowAnonymous:    cfg.AllowAnonymous,
		runtime:           cfg.Runtime,
	}
	h.mux = h.routes()
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	requestID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
	if requestID == "" || len(requestID) > 128 {
		requestID = newRequestID()
	}
	w.Header().Set("X-Request-Id", requestID)
	ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
	r = r.WithContext(ctx)

	// The management plane is cookie-session-only. Authenticate at the routing
	// boundary so future /api/v1 handlers cannot accidentally accept registry
	// Basic/Bearer credentials or bypass session expiry and logout.
	if r.URL.Path != "/api/v1/auth/login" {
		p, err := h.authenticateSession(r)
		if err != nil {
			h.clearSessionCookie(w)
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		if !safeMethod(r.Method) {
			if !h.originAllowed(r) {
				writeAPIError(w, http.StatusForbidden, "origin_invalid", "request origin is not allowed")
				return
			}
			provided := r.Header.Get("X-CSRF-Token")
			actual := hashToken(provided)
			if provided == "" || subtle.ConstantTimeCompare([]byte(actual), []byte(p.SessionIDHashCSRF())) != 1 {
				writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF token is missing or invalid")
				return
			}
		}
		if p.MustChange && !passwordChangeAllowedPath(r.URL.Path) {
			writeAPIError(w, http.StatusForbidden, "password_change_required", "password change is required before using the management API")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), sessionPrincipalContextKey{}, p))
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", h.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", h.authenticated("", h.handleLogout))
	mux.HandleFunc("GET /api/v1/auth/session", h.authenticated("", h.handleSession))
	mux.HandleFunc("POST /api/v1/auth/password", h.authenticated("", h.handlePasswordChange))

	mux.HandleFunc("GET /api/v1/system/info", h.authenticated("", h.handleSystemInfo))
	mux.HandleFunc("GET /api/v1/settings/runtime", h.authenticated("manage", h.handleRuntimeSettingsGet))
	mux.HandleFunc("PUT /api/v1/settings/runtime/{section}", h.authenticated("manage", h.handleRuntimeSettingsPut))
	mux.HandleFunc("POST /api/v1/settings/runtime/{section}/reset", h.authenticated("manage", h.handleRuntimeSettingsReset))
	mux.HandleFunc("POST /api/v1/settings/runtime/outbound-proxy/test", h.authenticated("admin", h.handleOutboundProxyTest))
	mux.HandleFunc("GET /api/v1/overview", h.authenticated("", h.handleOverview))
	mux.HandleFunc("GET /api/v1/events/timeline", h.authenticated("", h.handleEventTimeline))
	mux.HandleFunc("GET /api/v1/registry/summary", h.authenticated("", h.handleRegistrySummary))
	mux.HandleFunc("GET /api/v1/audit", h.authenticated("manage", h.handleAuditList))

	mux.HandleFunc("GET /api/v1/repositories", h.authenticated("", h.handleRepositoryList))
	mux.HandleFunc("GET /api/v1/repositories/{id}", h.authenticated("", h.handleRepositoryGet))
	mux.HandleFunc("DELETE /api/v1/repositories/{id}", h.authenticated("manage", h.handleRepositoryDelete))
	mux.HandleFunc("GET /api/v1/repositories/{id}/tags", h.authenticated("", h.handleRepositoryTags))
	mux.HandleFunc("GET /api/v1/repositories/{id}/manifests", h.authenticated("", h.handleRepositoryManifests))
	mux.HandleFunc("GET /api/v1/repositories/{id}/blobs", h.authenticated("", h.handleRepositoryBlobs))
	mux.HandleFunc("GET /api/v1/repositories/{id}/referrers", h.authenticated("", h.handleRepositoryReferrers))
	mux.HandleFunc("DELETE /api/v1/repositories/{id}/manifests/{digest}", h.authenticated("manage", h.handleRepositoryManifestDelete))
	mux.HandleFunc("DELETE /api/v1/repositories/{id}/tags/{tag}", h.authenticated("manage", h.handleRepositoryTagDelete))

	mux.HandleFunc("GET /api/v1/cache/entries", h.authenticated("", h.handleCacheEntries))
	mux.HandleFunc("POST /api/v1/cache/evictions", h.authenticated("manage", h.handleCacheEviction))
	mux.HandleFunc("GET /api/v1/cache/jobs/{id}", h.authenticated("", h.handleJobGet))

	mux.HandleFunc("GET /api/v1/uploads/active", h.authenticated("", h.handleUploadsActive))
	mux.HandleFunc("GET /api/v1/uploads", h.authenticated("", h.handleUploadsList))
	mux.HandleFunc("DELETE /api/v1/uploads/{id}", h.authenticated("manage", h.handleUploadDelete))

	mux.HandleFunc("GET /api/v1/storage/summary", h.authenticated("", h.handleStorageSummary))
	mux.HandleFunc("GET /api/v1/storage/breakdown", h.authenticated("", h.handleStorageBreakdown))
	mux.HandleFunc("GET /api/v1/storage/settings", h.authenticated("", h.handleStorageSettingsGet))
	mux.HandleFunc("PUT /api/v1/storage/settings", h.authenticated("manage", h.handleStorageSettingsPut))
	mux.HandleFunc("POST /api/v1/storage/gc", h.authenticated("manage", h.handleStorageGC))
	mux.HandleFunc("POST /api/v1/storage/fsck", h.authenticated("manage", h.handleStorageFsck))
	mux.HandleFunc("GET /api/v1/storage/jobs", h.authenticated("", h.handleStorageJobs))
	mux.HandleFunc("GET /api/v1/storage/jobs/{id}", h.authenticated("", h.handleJobGet))

	mux.HandleFunc("GET /api/v1/upstreams", h.authenticated("", h.handleUpstreamList))
	mux.HandleFunc("POST /api/v1/upstreams", h.authenticated("manage", h.handleUpstreamCreate))
	mux.HandleFunc("GET /api/v1/upstreams/{id}", h.authenticated("", h.handleUpstreamGet))
	mux.HandleFunc("PUT /api/v1/upstreams/{id}", h.authenticated("manage", h.handleUpstreamUpdate))
	mux.HandleFunc("DELETE /api/v1/upstreams/{id}", h.authenticated("manage", h.handleUpstreamDelete))
	mux.HandleFunc("POST /api/v1/upstreams/{id}/test", h.authenticated("manage", h.handleUpstreamTest))

	mux.HandleFunc("GET /api/v1/users", h.authenticated("admin", h.handleUserList))
	mux.HandleFunc("POST /api/v1/users", h.authenticated("admin", h.handleUserCreate))
	mux.HandleFunc("PATCH /api/v1/users/{id}", h.authenticated("admin", h.handleUserUpdate))
	mux.HandleFunc("DELETE /api/v1/users/{id}", h.authenticated("admin", h.handleUserDelete))
	mux.HandleFunc("POST /api/v1/users/{id}/password", h.authenticated("admin", h.handleUserPasswordReset))

	mux.HandleFunc("GET /api/v1/tokens", h.authenticated("", h.handleTokenList))
	mux.HandleFunc("POST /api/v1/tokens", h.authenticated("", h.handleTokenCreate))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", h.authenticated("", h.handleTokenRevoke))
	return mux
}

func (h *Handler) recordAudit(r *http.Request, actorType, actorID, action, targetType, targetID, result, message string) {
	event := store.AuditEvent{
		ActorType:  actorType,
		ActorID:    actorID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		IP:         h.requestInfo.ClientIP(r),
		UserAgent:  r.UserAgent(),
		Result:     result,
		Message:    message,
		RequestID:  requestID(r.Context()),
	}
	if err := h.Store.AddAuditEvent(r.Context(), event); err != nil {
		h.Logger.Error("write audit event failed", "action", action, "error", err)
	}
}

func (h *Handler) sessionCookieName() string {
	if h.secureCookie {
		return "__Host-crp_session"
	}
	return "crp_session"
}
