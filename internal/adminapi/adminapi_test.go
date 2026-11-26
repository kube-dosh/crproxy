package adminapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crproxy/internal/auth"
	"crproxy/internal/cas"
	"crproxy/internal/jobs"
	"crproxy/internal/proxy"
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
	handler, err := NewHandler(metadata, authenticator, Config{
		CSRFSecret:     []byte("abcdef0123456789abcdef0123456789"),
		SessionTTL:     time.Hour,
		SessionIdleTTL: 30 * time.Minute,
		Version:        "test",
		StartedAt:      time.Now().UTC(),
		Upstreams:      upstreamManager,
		Storage:        storageManager,
		Jobs:           jobRunner,
		CAS:            casStore,
		AuditRetention: 90 * 24 * time.Hour,
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
	patSession := env.request(t, http.MethodGet, "/api/v1/auth/session", nil, map[string]string{
		"Authorization": "Bearer " + tokenPayload.Token,
	})
	if patSession.status != http.StatusOK || !strings.Contains(string(patSession.body), `"authMode":"pat"`) {
		t.Fatalf("PAT authentication failed: status=%d body=%s", patSession.status, patSession.body)
	}
	var tokenMetadata struct {
		Metadata tokenResponse `json:"metadata"`
	}
	if err := json.Unmarshal(tokenCreated.body, &tokenMetadata); err != nil {
		t.Fatal(err)
	}
	revoked := env.request(t, http.MethodDelete, "/api/v1/tokens/"+tokenMetadata.Metadata.ID, nil, map[string]string{
		"Authorization": "Bearer " + tokenPayload.Token,
	})
	if revoked.status != http.StatusNoContent {
		t.Fatalf("revoke token failed: status=%d body=%s", revoked.status, revoked.body)
	}
	revokedSession := env.request(t, http.MethodGet, "/api/v1/auth/session", nil, map[string]string{
		"Authorization": "Bearer " + tokenPayload.Token,
	})
	if revokedSession.status != http.StatusUnauthorized {
		t.Fatalf("revoked PAT should fail: status=%d body=%s", revokedSession.status, revokedSession.body)
	}
	audit := env.request(t, http.MethodGet, "/api/v1/audit", nil, nil)
	if audit.status != http.StatusOK || !strings.Contains(string(audit.body), `"auth.token.revoke"`) || !strings.Contains(string(audit.body), `"user.create"`) {
		t.Fatalf("audit events missing: status=%d body=%s", audit.status, audit.body)
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

	for _, path := range []string{"/api/v1/system/info", "/api/v1/storage/settings", "/api/v1/audit"} {
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
