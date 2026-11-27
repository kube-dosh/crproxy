package adminapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"crproxy/internal/auth"
	"crproxy/internal/cas"
	"crproxy/internal/jobs"
	"crproxy/internal/proxy"
	"crproxy/internal/runtimeconfig"
	"crproxy/internal/secretbox"
	"crproxy/internal/storage"
	"crproxy/internal/store"
	"crproxy/internal/upstreammgr"
)

const testAdminPassword = "admin-password-123"

type testEnv struct {
	server  *httptest.Server
	client  *http.Client
	store   *store.Store
	handler *Handler
}

type testRuntimeKeys struct {
	sealer *secretbox.Sealer
}

func (k testRuntimeKeys) CurrentVersion() int64 { return 1 }
func (k testRuntimeKeys) Sealer(version int64) (*secretbox.Sealer, error) {
	if version != 1 {
		return nil, runtimeconfig.ErrInvalid
	}
	return k.sealer, nil
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(root, "meta", "crproxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	metadata := store.New(db)
	hash, err := auth.HashPassword(testAdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.CreateUser(t.Context(), "admin", hash, "admin"); err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "crproxy", "crproxy", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := &auth.Authenticator{
		Store:     metadata,
		Signer:    signer,
		Realm:     "http://registry.test",
		Service:   "crproxy",
		DummyHash: auth.DummyPasswordHash(),
	}
	casStore, err := cas.New(root)
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler, err := proxy.NewHandler(http.NotFoundHandler(), metadata, casStore, authenticator, nil, "r", 4<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secretbox.New(bytes.Repeat([]byte{9}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	upstreamManager, err := upstreammgr.New(metadata, proxyHandler, sealer, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	storageManager, err := storage.New(metadata, casStore, storage.Config{
		Root: root,
		Settings: storage.Settings{
			MaxBytes:      1 << 30,
			CacheMaxBytes: 512 << 20,
			MinFreeBytes:  1 << 20,
			HighWatermark: 0.85,
			LowWatermark:  0.70,
			OnFull:        "bypass",
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobRunner, err := jobs.New(metadata, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtimeManager, err := runtimeconfig.New(t.Context(), metadata, runtimeconfig.Base{
		RegistryAuth: runtimeconfig.RegistryAuth{
			Challenge: "bearer", Service: "crproxy", Realm: "http://registry.test", TokenTTL: "5m",
		},
		WebSession: runtimeconfig.WebSession{SessionTTL: "1h", IdleTimeout: "30m"},
		Storage: runtimeconfig.Storage{
			MaxBytes: 1 << 30, CacheMaxBytes: 512 << 20, MinFreeBytes: 1 << 20,
			HighWatermark: 0.85, LowWatermark: 0.70, OnFull: "bypass",
			MaxBlobBytes: 20 << 30, MaxManifestBytes: 4 << 20, DownloadConcurrency: 8,
			TmpTTL: "1h", TrashTTL: "24h",
		},
		Uploads: runtimeconfig.Uploads{UploadsTTL: "24h"},
		Audit:   runtimeconfig.Audit{AuditRetention: "2160h"},
		Startup: runtimeconfig.Startup{PublicScheme: "http", ProxyPathPrefix: "r"},
	}, testRuntimeKeys{sealer: sealer})
	if err != nil {
		t.Fatal(err)
	}
	runtimeManager.SetApply(func(current runtimeconfig.Config) {
		_ = authenticator.ApplyPolicy(auth.RegistryPolicy{
			Realm: current.RegistryAuth.Realm, Service: current.RegistryAuth.Service,
			ChallengeMode: current.RegistryAuth.Challenge, AllowAnonymous: current.RegistryAuth.AllowAnonymous,
			TokenTTL: current.TokenTTLDuration(),
		})
		tmpTTL, trashTTL := current.StorageDurations()
		_ = storageManager.ApplyPolicy(storage.Policy{
			Settings: storage.Settings{
				MaxBytes: current.Storage.MaxBytes, CacheMaxBytes: current.Storage.CacheMaxBytes,
				MinFreeBytes: current.Storage.MinFreeBytes, HighWatermark: current.Storage.HighWatermark,
				LowWatermark: current.Storage.LowWatermark, OnFull: current.Storage.OnFull,
			},
			TmpTTL: tmpTTL, TrashTTL: trashTTL, DownloadConcurrency: current.Storage.DownloadConcurrency,
		})
	})
	handler, err := NewHandler(metadata, Config{
		CSRFSecret:     []byte("abcdef0123456789abcdef0123456789"),
		SessionTTL:     time.Hour,
		SessionIdleTTL: 30 * time.Minute,
		PublicURL:      "http://admin.test",
		RegistryURL:    "http://registry.test",
		DeploymentMode: "split",
		RegistryListen: ":5000",
		AdminListen:    ":5443",
		Version:        "test",
		StartedAt:      time.Now().UTC(),
		Upstreams:      upstreamManager,
		Storage:        storageManager,
		Jobs:           jobRunner,
		CAS:            casStore,
		AuditRetention: 90 * 24 * time.Hour,
		Runtime:        runtimeManager,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{store: metadata, handler: handler, client: &http.Client{Jar: jar}}
	env.server = httptest.NewServer(handler)
	t.Cleanup(func() {
		env.server.Close()
		_ = db.Close()
	})
	return env
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (e *testEnv) request(t *testing.T, method, path string, body any, headers map[string]string) response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header.Clone(), body: raw}
}

func (e *testEnv) login(t *testing.T, username, password string) (string, response) {
	t.Helper()
	resp := e.request(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": username,
		"password": password,
	}, nil)
	if resp.status != http.StatusOK {
		return "", resp
	}
	var payload struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.CSRFToken, resp
}

func (e *testEnv) newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func TestSessionUsersTokensAndAudit(t *testing.T) {
	env := newTestEnv(t)
	csrf, login := env.login(t, "admin", testAdminPassword)
	if login.status != http.StatusOK || csrf == "" {
		t.Fatalf("login failed: status=%d body=%s", login.status, login.body)
	}
	session := env.request(t, http.MethodGet, "/api/v1/auth/session", nil, nil)
	if session.status != http.StatusOK || !strings.Contains(string(session.body), `"authMode":"session"`) {
		t.Fatalf("session restore failed: status=%d body=%s", session.status, session.body)
	}
	withoutCSRF := env.request(t, http.MethodPost, "/api/v1/users", map[string]string{
		"username": "viewer",
		"password": "viewer-password-123",
		"role":     "viewer",
	}, nil)
	if withoutCSRF.status != http.StatusForbidden || !strings.Contains(string(withoutCSRF.body), `"csrf_invalid"`) {
		t.Fatalf("CSRF must be enforced: status=%d body=%s", withoutCSRF.status, withoutCSRF.body)
	}
	created := env.request(t, http.MethodPost, "/api/v1/users", map[string]string{
		"username": "viewer",
		"password": "viewer-password-123",
		"role":     "viewer",
	}, map[string]string{"X-CSRF-Token": csrf})
	if created.status != http.StatusCreated || !strings.Contains(string(created.body), `"mustChangePassword":true`) {
		t.Fatalf("create user failed: status=%d body=%s", created.status, created.body)
	}
	users := env.request(t, http.MethodGet, "/api/v1/users", nil, nil)
	if users.status != http.StatusOK || !strings.Contains(string(users.body), `"viewer"`) {
		t.Fatalf("list users failed: status=%d body=%s", users.status, users.body)
	}
	tokenCreated := env.request(t, http.MethodPost, "/api/v1/tokens", map[string]any{
		"name":   "automation",
		"scopes": []string{"pull", "push"},
	}, map[string]string{"X-CSRF-Token": csrf})
	if tokenCreated.status != http.StatusCreated {
		t.Fatalf("create token failed: status=%d body=%s", tokenCreated.status, tokenCreated.body)
	}
	var tokenPayload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(tokenCreated.body, &tokenPayload); err != nil || tokenPayload.Token == "" {
		t.Fatalf("token response invalid: %v body=%s", err, tokenCreated.body)
	}
	sessionClient := env.client
	env.client = env.newClient()
	patManagement := env.request(t, http.MethodGet, "/api/v1/system/info", nil, map[string]string{
		"Authorization": "Bearer " + tokenPayload.Token,
	})
	if patManagement.status != http.StatusUnauthorized {
		t.Fatalf("management API must reject distribution PAT credentials: status=%d body=%s", patManagement.status, patManagement.body)
	}
	env.client = sessionClient
	var tokenMetadata struct {
		Metadata tokenResponse `json:"metadata"`
	}
	if err := json.Unmarshal(tokenCreated.body, &tokenMetadata); err != nil {
		t.Fatal(err)
	}
	revoked := env.request(t, http.MethodDelete, "/api/v1/tokens/"+tokenMetadata.Metadata.ID, nil, map[string]string{
		"X-CSRF-Token": csrf,
	})
	if revoked.status != http.StatusNoContent {
		t.Fatalf("revoke token failed: status=%d body=%s", revoked.status, revoked.body)
	}
	audit := env.request(t, http.MethodGet, "/api/v1/audit", nil, nil)
	if audit.status != http.StatusOK || !strings.Contains(string(audit.body), `"auth.token.revoke"`) || !strings.Contains(string(audit.body), `"user.create"`) {
		t.Fatalf("audit events missing: status=%d body=%s", audit.status, audit.body)
	}
}

func TestManagementAPIUnauthenticatedDoesNotTriggerBrowserBasicDialog(t *testing.T) {
	env := newTestEnv(t)
	resp := env.request(t, http.MethodGet, "/api/v1/auth/session", nil, nil)
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("unexpected status: %d body=%s", resp.status, resp.body)
	}
	if challenge := resp.header.Get("WWW-Authenticate"); challenge != "" {
		t.Fatalf("management API must not advertise a browser authentication challenge: %q", challenge)
	}
	if cacheControl := resp.header.Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("management authentication responses must not be cached: %q", cacheControl)
	}
}

func TestManagementAPIIgnoresCachedBrowserBasicAndLogoutClearsSession(t *testing.T) {
	env := newTestEnv(t)
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:"+testAdminPassword))
	csrf, login := env.login(t, "admin", testAdminPassword)
	if login.status != http.StatusOK || csrf == "" {
		t.Fatalf("login failed: status=%d body=%s", login.status, login.body)
	}
	sessionWithBasic := env.request(t, http.MethodGet, "/api/v1/auth/session", nil, map[string]string{
		"Authorization": basic,
	})
	if sessionWithBasic.status != http.StatusOK || !strings.Contains(string(sessionWithBasic.body), `"authMode":"session"`) || !strings.Contains(string(sessionWithBasic.body), `"csrfToken"`) {
		t.Fatalf("session cookie must take precedence over cached Basic: status=%d body=%s", sessionWithBasic.status, sessionWithBasic.body)
	}
	logout := env.request(t, http.MethodPost, "/api/v1/auth/logout", nil, map[string]string{
		"Authorization": basic,
		"X-CSRF-Token":  csrf,
	})
	if logout.status != http.StatusNoContent {
		t.Fatalf("logout failed: status=%d body=%s", logout.status, logout.body)
	}
	session := env.request(t, http.MethodGet, "/api/v1/auth/session", nil, map[string]string{
		"Authorization": basic,
	})
	if session.status != http.StatusUnauthorized {
		t.Fatalf("cached Basic must not restore a browser session: status=%d body=%s", session.status, session.body)
	}
	api := env.request(t, http.MethodGet, "/api/v1/system/info", nil, map[string]string{
		"Authorization": basic,
	})
	if api.status != http.StatusUnauthorized {
		t.Fatalf("management API must reject Basic credentials: status=%d body=%s", api.status, api.body)
	}
}

func TestAllManagementRoutesRejectDistributionCredentials(t *testing.T) {
	env := newTestEnv(t)
	csrf, login := env.login(t, "admin", testAdminPassword)
	if login.status != http.StatusOK || csrf == "" {
		t.Fatalf("login failed: status=%d body=%s", login.status, login.body)
	}
	tokenCreated := env.request(t, http.MethodPost, "/api/v1/tokens", map[string]any{
		"name":   "distribution-only",
		"scopes": []string{"pull", "push"},
	}, map[string]string{"X-CSRF-Token": csrf})
	if tokenCreated.status != http.StatusCreated {
		t.Fatalf("create PAT failed: status=%d body=%s", tokenCreated.status, tokenCreated.body)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(tokenCreated.body, &payload); err != nil || payload.Token == "" {
		t.Fatalf("invalid PAT response: %v body=%s", err, tokenCreated.body)
	}

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodGet, "/api/v1/auth/session"},
		{http.MethodPost, "/api/v1/auth/password"},
		{http.MethodGet, "/api/v1/system/info"},
		{http.MethodGet, "/api/v1/settings/runtime"},
		{http.MethodPut, "/api/v1/settings/runtime/storage"},
		{http.MethodPost, "/api/v1/settings/runtime/storage/reset"},
		{http.MethodPost, "/api/v1/settings/runtime/outbound-proxy/test"},
		{http.MethodGet, "/api/v1/overview"},
		{http.MethodGet, "/api/v1/events/timeline"},
		{http.MethodGet, "/api/v1/registry/summary"},
		{http.MethodGet, "/api/v1/audit"},
		{http.MethodGet, "/api/v1/repositories"},
		{http.MethodGet, "/api/v1/repositories/1"},
		{http.MethodDelete, "/api/v1/repositories/1"},
		{http.MethodGet, "/api/v1/repositories/1/tags"},
		{http.MethodGet, "/api/v1/repositories/1/manifests"},
		{http.MethodGet, "/api/v1/repositories/1/blobs"},
		{http.MethodGet, "/api/v1/repositories/1/referrers"},
		{http.MethodDelete, "/api/v1/repositories/1/manifests/sha256:abc"},
		{http.MethodDelete, "/api/v1/repositories/1/tags/latest"},
		{http.MethodGet, "/api/v1/cache/entries"},
		{http.MethodPost, "/api/v1/cache/evictions"},
		{http.MethodGet, "/api/v1/cache/jobs/job-1"},
		{http.MethodGet, "/api/v1/uploads/active"},
		{http.MethodGet, "/api/v1/uploads"},
		{http.MethodDelete, "/api/v1/uploads/upload-1"},
		{http.MethodGet, "/api/v1/storage/summary"},
		{http.MethodGet, "/api/v1/storage/breakdown"},
		{http.MethodGet, "/api/v1/storage/settings"},
		{http.MethodPut, "/api/v1/storage/settings"},
		{http.MethodPost, "/api/v1/storage/gc"},
		{http.MethodPost, "/api/v1/storage/fsck"},
		{http.MethodGet, "/api/v1/storage/jobs"},
		{http.MethodGet, "/api/v1/storage/jobs/job-1"},
		{http.MethodGet, "/api/v1/upstreams"},
		{http.MethodPost, "/api/v1/upstreams"},
		{http.MethodGet, "/api/v1/upstreams/dockerhub"},
		{http.MethodPut, "/api/v1/upstreams/dockerhub"},
		{http.MethodDelete, "/api/v1/upstreams/dockerhub"},
		{http.MethodPost, "/api/v1/upstreams/dockerhub/test"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/users"},
		{http.MethodPatch, "/api/v1/users/user-1"},
		{http.MethodDelete, "/api/v1/users/user-1"},
		{http.MethodPost, "/api/v1/users/user-1/password"},
		{http.MethodGet, "/api/v1/tokens"},
		{http.MethodPost, "/api/v1/tokens"},
		{http.MethodDelete, "/api/v1/tokens/token-1"},
	}

	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:"+testAdminPassword))
	credentials := []string{basic, "Bearer " + payload.Token}
	env.client = env.newClient()
	for _, route := range routes {
		for _, credential := range credentials {
			resp := env.request(t, route.method, route.path, nil, map[string]string{
				"Authorization": credential,
			})
			if resp.status != http.StatusUnauthorized {
				t.Fatalf("%s %s accepted distribution credentials %q: status=%d body=%s", route.method, route.path, strings.Fields(credential)[0], resp.status, resp.body)
			}
			if challenge := resp.header.Get("WWW-Authenticate"); challenge != "" {
				t.Fatalf("%s %s advertised a management authentication challenge: %q", route.method, route.path, challenge)
			}
		}
	}
}

func TestViewerCannotEscalateOrUseAdminAPI(t *testing.T) {
	env := newTestEnv(t)
	hash, _ := auth.HashPassword("viewer-password-123")
	if _, err := env.store.CreateUser(t.Context(), "viewer", hash, "viewer"); err != nil {
		t.Fatal(err)
	}
	viewerClient := env.newClient()
	env.client = viewerClient
	csrf, login := env.login(t, "viewer", "viewer-password-123")
	if login.status != http.StatusOK {
		t.Fatalf("viewer login failed: %s", login.body)
	}
	if users := env.request(t, http.MethodGet, "/api/v1/users", nil, nil); users.status != http.StatusForbidden {
		t.Fatalf("viewer must not list users: status=%d", users.status)
	}
	escalated := env.request(t, http.MethodPost, "/api/v1/tokens", map[string]any{
		"name":   "escalated",
		"scopes": []string{"push"},
	}, map[string]string{"X-CSRF-Token": csrf})
	if escalated.status != http.StatusForbidden {
		t.Fatalf("viewer must not create push token: status=%d body=%s", escalated.status, escalated.body)
	}
	allowed := env.request(t, http.MethodPost, "/api/v1/tokens", map[string]any{
		"name":   "pull-only",
		"scopes": []string{"pull"},
	}, map[string]string{"X-CSRF-Token": csrf})
	if allowed.status != http.StatusCreated {
		t.Fatalf("viewer pull token failed: status=%d body=%s", allowed.status, allowed.body)
	}
}

func TestOperatorCanUseOperationalSettingsButNotUserAdministration(t *testing.T) {
	env := newTestEnv(t)
	hash, _ := auth.HashPassword("operator-password-123")
	if _, err := env.store.CreateUser(t.Context(), "operator", hash, "operator"); err != nil {
		t.Fatal(err)
	}
	env.client = env.newClient()
	csrf, login := env.login(t, "operator", "operator-password-123")
	if login.status != http.StatusOK {
		t.Fatalf("operator login failed: status=%d body=%s", login.status, login.body)
	}

	for _, path := range []string{"/api/v1/system/info", "/api/v1/storage/settings", "/api/v1/settings/runtime", "/api/v1/audit"} {
		response := env.request(t, http.MethodGet, path, nil, nil)
		if response.status != http.StatusOK {
			t.Fatalf("operator must read %s: status=%d body=%s", path, response.status, response.body)
		}
	}

	settings := env.request(t, http.MethodPut, "/api/v1/storage/settings", map[string]any{
		"maxBytes":      1 << 30,
		"cacheMaxBytes": 512 << 20,
		"minFreeBytes":  1 << 20,
		"highWatermark": 0.85,
		"lowWatermark":  0.70,
		"onFull":        "reject",
	}, map[string]string{"X-CSRF-Token": csrf})
	if settings.status != http.StatusOK {
		t.Fatalf("operator must manage storage thresholds: status=%d body=%s", settings.status, settings.body)
	}
	authPolicy := env.request(t, http.MethodPut, "/api/v1/settings/runtime/registry-auth", map[string]any{
		"expectedVersion": 0,
		"challenge":       "bearer",
		"service":         "crproxy",
		"realm":           "http://registry.test",
		"allowAnonymous":  true,
		"tokenTtl":        "5m",
	}, map[string]string{"X-CSRF-Token": csrf})
	if authPolicy.status != http.StatusForbidden {
		t.Fatalf("operator must not change registry auth policy: status=%d body=%s", authPolicy.status, authPolicy.body)
	}

	if users := env.request(t, http.MethodGet, "/api/v1/users", nil, nil); users.status != http.StatusForbidden {
		t.Fatalf("operator must not list users: status=%d body=%s", users.status, users.body)
	}
	created := env.request(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "escalated",
		"password": "escalated-password-123",
		"role":     "admin",
	}, map[string]string{"X-CSRF-Token": csrf})
	if created.status != http.StatusForbidden {
		t.Fatalf("operator must not create users: status=%d body=%s", created.status, created.body)
	}
}

func TestRuntimeSettingsAPIHotUpdatesAndConflicts(t *testing.T) {
	env := newTestEnv(t)
	csrf, login := env.login(t, "admin", testAdminPassword)
	if login.status != http.StatusOK || csrf == "" {
		t.Fatalf("login failed: %s", login.body)
	}
	initial := env.request(t, http.MethodGet, "/api/v1/settings/runtime", nil, nil)
	if initial.status != http.StatusOK || !strings.Contains(string(initial.body), `"registryAuth"`) || strings.Contains(string(initial.body), "passwordCiphertext") {
		t.Fatalf("runtime settings read failed: status=%d body=%s", initial.status, initial.body)
	}
	updated := env.request(t, http.MethodPut, "/api/v1/settings/runtime/registry-auth", map[string]any{
		"expectedVersion":  0,
		"challenge":        "bearer",
		"service":          "crproxy",
		"realm":            "http://registry.test",
		"allowAnonymous":   true,
		"anonymousActions": []string{"pull", "push"},
		"tokenTtl":         "10m",
	}, map[string]string{"X-CSRF-Token": csrf})
	if updated.status != http.StatusOK || !strings.Contains(string(updated.body), `"allowAnonymous":true`) || !strings.Contains(string(updated.body), `"anonymousActions":["pull","push"]`) || !strings.Contains(string(updated.body), `"version":1`) {
		t.Fatalf("runtime settings update failed: status=%d body=%s", updated.status, updated.body)
	}
	systemInfo := env.request(t, http.MethodGet, "/api/v1/system/info", nil, nil)
	if systemInfo.status != http.StatusOK || !strings.Contains(string(systemInfo.body), `"allowAnonymous":true`) || !strings.Contains(string(systemInfo.body), `"anonymousActions":["pull","push"]`) || !strings.Contains(string(systemInfo.body), `"registryURL":"http://registry.test"`) || !strings.Contains(string(systemInfo.body), `"managementURL":"http://admin.test"`) || !strings.Contains(string(systemInfo.body), `"planeMode":"split"`) {
		t.Fatalf("runtime auth policy did not reach system info: status=%d body=%s", systemInfo.status, systemInfo.body)
	}
	conflict := env.request(t, http.MethodPut, "/api/v1/settings/runtime/registry-auth", map[string]any{
		"expectedVersion": 0,
		"challenge":       "basic",
		"service":         "crproxy",
		"realm":           "http://registry.test",
		"allowAnonymous":  false,
		"tokenTtl":        "5m",
	}, map[string]string{"X-CSRF-Token": csrf})
	if conflict.status != http.StatusConflict || !strings.Contains(string(conflict.body), `"config_version_conflict"`) {
		t.Fatalf("stale runtime update must conflict: status=%d body=%s", conflict.status, conflict.body)
	}
	outbound := env.request(t, http.MethodPut, "/api/v1/settings/runtime/outbound-proxy", map[string]any{
		"expectedVersion": 0,
		"httpProxy":       "http://127.0.0.1:1080",
		"httpsProxy":      "http://127.0.0.1:1080",
		"noProxy":         "localhost",
		"username":        "proxy-user",
		"password":        "super-secret-proxy-password",
	}, map[string]string{"X-CSRF-Token": csrf})
	if outbound.status != http.StatusOK || !strings.Contains(string(outbound.body), `"passwordConfigured":true`) || strings.Contains(string(outbound.body), "super-secret-proxy-password") {
		t.Fatalf("outbound proxy update/redaction failed: status=%d body=%s", outbound.status, outbound.body)
	}
	reset := env.request(t, http.MethodPost, "/api/v1/settings/runtime/registry-auth/reset", map[string]any{
		"expectedVersion": 1,
	}, map[string]string{"X-CSRF-Token": csrf})
	if reset.status != http.StatusOK || !strings.Contains(string(reset.body), `"allowAnonymous":false`) {
		t.Fatalf("runtime reset failed: status=%d body=%s", reset.status, reset.body)
	}
}

func TestPasswordChangeRevokesOtherSessionsAndEnforcesInitialChange(t *testing.T) {
	env := newTestEnv(t)
	if err := env.store.SetMustChangePassword(t.Context(), mustUserID(t, env, "admin"), true); err != nil {
		t.Fatal(err)
	}
	firstClient := env.client
	secondClient := env.newClient()
	csrf, login := env.login(t, "admin", testAdminPassword)
	if login.status != http.StatusOK {
		t.Fatalf("login failed: %s", login.body)
	}
	if users := env.request(t, http.MethodGet, "/api/v1/users", nil, nil); users.status != http.StatusForbidden || !strings.Contains(string(users.body), "password_change_required") {
		t.Fatalf("initial password change must be enforced: status=%d body=%s", users.status, users.body)
	}
	env.client = secondClient
	_, secondLogin := env.login(t, "admin", testAdminPassword)
	if secondLogin.status != http.StatusOK {
		t.Fatalf("second login failed: %s", secondLogin.body)
	}
	env.client = firstClient
	changed := env.request(t, http.MethodPost, "/api/v1/auth/password", map[string]string{
		"currentPassword": testAdminPassword,
		"newPassword":     "admin-new-password-456",
	}, map[string]string{"X-CSRF-Token": csrf})
	if changed.status != http.StatusNoContent {
		t.Fatalf("password change failed: status=%d body=%s", changed.status, changed.body)
	}
	if users := env.request(t, http.MethodGet, "/api/v1/users", nil, nil); users.status != http.StatusOK {
		t.Fatalf("current session should remain valid: status=%d body=%s", users.status, users.body)
	}
	env.client = secondClient
	if other := env.request(t, http.MethodGet, "/api/v1/auth/session", nil, nil); other.status != http.StatusUnauthorized {
		t.Fatalf("other sessions must be revoked: status=%d body=%s", other.status, other.body)
	}
}

func TestLastAdminAndSelfProtection(t *testing.T) {
	env := newTestEnv(t)
	csrf, _ := env.login(t, "admin", testAdminPassword)
	adminID := mustUserID(t, env, "admin")
	selfDisable := env.request(t, http.MethodPatch, "/api/v1/users/"+adminID, map[string]any{
		"enabled": false,
	}, map[string]string{"X-CSRF-Token": csrf})
	if selfDisable.status != http.StatusConflict {
		t.Fatalf("self disable must be rejected: status=%d body=%s", selfDisable.status, selfDisable.body)
	}
	selfDelete := env.request(t, http.MethodDelete, "/api/v1/users/"+adminID, nil, map[string]string{"X-CSRF-Token": csrf})
	if selfDelete.status != http.StatusConflict {
		t.Fatalf("self delete must be rejected: status=%d body=%s", selfDelete.status, selfDelete.body)
	}
}

func mustUserID(t *testing.T, env *testEnv, username string) string {
	t.Helper()
	user, err := env.store.UserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

func TestEventTimelineValidatesWindowAndReturnsDenseBuckets(t *testing.T) {
	env := newTestEnv(t)
	csrf, login := env.login(t, "admin", testAdminPassword)
	if login.status != http.StatusOK || csrf == "" {
		t.Fatalf("login failed: status=%d body=%s", login.status, login.body)
	}
	for _, query := range []string{"?hours=0", "?hours=169", "?hours=abc"} {
		response := env.request(t, http.MethodGet, "/api/v1/events/timeline"+query, nil, nil)
		if response.status != http.StatusBadRequest {
			t.Fatalf("timeline%s status = %d, body=%s", query, response.status, response.body)
		}
	}
	response := env.request(t, http.MethodGet, "/api/v1/events/timeline?hours=6", nil, nil)
	if response.status != http.StatusOK {
		t.Fatalf("timeline status = %d, body=%s", response.status, response.body)
	}
	var payload struct {
		Hours   int                 `json:"hours"`
		Since   time.Time           `json:"since"`
		Buckets []store.EventBucket `json:"buckets"`
	}
	if err := json.Unmarshal(response.body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Hours != 6 || len(payload.Buckets) != 6 {
		t.Fatalf("unexpected timeline dimensions: hours=%d buckets=%d", payload.Hours, len(payload.Buckets))
	}
	for index, bucket := range payload.Buckets {
		want := payload.Since.Add(time.Duration(index) * time.Hour)
		if !bucket.At.Equal(want) {
			t.Fatalf("bucket %d at %s, want %s", index, bucket.At, want)
		}
	}
}

func assertJSONArrayAt(t *testing.T, body []byte, keys ...string) {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, body)
	}
	for _, key := range keys {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("path %v: expected object before %q, got %T; body=%s", keys, key, value, body)
		}
		value = object[key]
	}
	if _, ok := value.([]any); !ok {
		t.Fatalf("path %v: expected JSON array, got %T (%v); body=%s", keys, value, value, body)
	}
}

func TestEmptyCollectionsEncodeAsArrays(t *testing.T) {
	env := newTestEnv(t)
	_, login := env.login(t, "admin", testAdminPassword)
	if login.status != http.StatusOK {
		t.Fatalf("login failed: status=%d body=%s", login.status, login.body)
	}

	checks := []struct {
		path string
		keys []string
	}{
		{"/api/v1/overview", []string{"activity"}},
		{"/api/v1/overview", []string{"upstreams", "items"}},
		{"/api/v1/events/timeline?hours=6", []string{"buckets"}},
		{"/api/v1/repositories", []string{"repositories"}},
		{"/api/v1/cache/entries", []string{"entries"}},
		{"/api/v1/uploads", []string{"uploads"}},
		{"/api/v1/uploads/active", []string{"uploads"}},
		{"/api/v1/storage/breakdown?group_by=upstream", []string{"items"}},
		{"/api/v1/storage/breakdown?group_by=repository", []string{"items"}},
		{"/api/v1/storage/jobs", []string{"jobs"}},
		{"/api/v1/upstreams", []string{"upstreams"}},
		{"/api/v1/tokens", []string{"tokens"}},
		{"/api/v1/audit?action=does.not.exist", []string{"events"}},
	}
	for _, check := range checks {
		response := env.request(t, http.MethodGet, check.path, nil, nil)
		if response.status != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", check.path, response.status, response.body)
		}
		assertJSONArrayAt(t, response.body, check.keys...)
	}

	repository, err := env.store.EnsureLocalRepository(t.Context(), "empty/repository")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/repositories/" + strconv.FormatInt(repository.ID, 10)
	for _, suffix := range []string{"/tags", "/manifests", "/blobs", "/referrers"} {
		response := env.request(t, http.MethodGet, base+suffix, nil, nil)
		if response.status != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", base+suffix, response.status, response.body)
		}
		key := strings.TrimPrefix(suffix, "/")
		if key == "manifests" || key == "blobs" {
			key = "entries"
		}
		assertJSONArrayAt(t, response.body, key)
	}
}
