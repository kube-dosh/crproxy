package proxy

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crproxy/internal/auth"
	"crproxy/internal/cas"
	"crproxy/internal/oci"
	"crproxy/internal/store"
)

const proxyTestPassword = "proxy-admin-password"

type proxyTestEnv struct {
	server        *httptest.Server
	upstream      *httptest.Server
	store         *store.Store
	db            *sql.DB
	tokenCalls    *atomic.Int32
	blobCalls     *atomic.Int32
	writeCalls    *atomic.Int32
	conditional   *atomic.Int32
	manifestCalls *atomic.Int32
	delayedBlob   []byte
	delayedDigest oci.Digest
}

func newProxyTestEnv(t *testing.T) *proxyTestEnv {
	t.Helper()
	env := &proxyTestEnv{
		tokenCalls:    &atomic.Int32{},
		blobCalls:     &atomic.Int32{},
		writeCalls:    &atomic.Int32{},
		conditional:   &atomic.Int32{},
		manifestCalls: &atomic.Int32{},
	}
	root := t.TempDir()
	casStore, err := cas.New(root)
	if err != nil {
		t.Fatal(err)
	}
	env.db, err = store.Open(t.Context(), filepath.Join(root, "meta", "crproxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	env.store = store.New(env.db)
	adminHash, err := auth.HashPassword(proxyTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateUser(t.Context(), "admin", adminHash, "admin"); err != nil {
		t.Fatal(err)
	}
	maxBlob := int64(1 << 20)
	if err := env.store.UpsertUpstream(t.Context(), store.Upstream{
		ID:                 "mock",
		Name:               "mock",
		Endpoint:           "https://mock.registry.test",
		RouteHost:          "mock.registry.test",
		RoutePrefix:        "mock",
		AuthType:           "anonymous",
		TagTTLSeconds:      60,
		RequestTimeoutSecs: 5,
		MaxBlobBytes:       &maxBlob,
		Enabled:            true,
	}); err != nil {
		t.Fatal(err)
	}

	config := []byte("{}")
	layer := []byte("layer-data")
	env.delayedBlob = []byte("delayed-blob")
	configDigest := mustDigest(t, config)
	layerDigest := mustDigest(t, layer)
	env.delayedDigest = mustDigest(t, env.delayedBlob)
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + configDigest.String() + `","size":2},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"` + layerDigest.String() + `","size":10}]}`)
	manifestDigest := mustDigest(t, manifest)
	dockerManifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":"` + configDigest.String() + `","size":2},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"` + layerDigest.String() + `","size":10}]}`)

	env.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			env.writeCalls.Add(1)
			http.Error(w, "write reached upstream", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/token" {
			env.tokenCalls.Add(1)
			time.Sleep(25 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "mock-token", "expires_in": 300})
			return
		}
		if r.Header.Get("Authorization") != "Bearer mock-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+env.upstream.URL+`/token",service="mock",scope="repository:demo/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v2/demo/app/manifests/latest":
			env.manifestCalls.Add(1)
			body := manifest
			mediaType := oci.MediaTypeOCIManifest
			digest := manifestDigest
			etag := `"mock-etag"`
			if strings.Contains(r.Header.Get("Accept"), oci.MediaTypeDockerManifest) {
				body = dockerManifest
				mediaType = oci.MediaTypeDockerManifest
				digest = mustDigest(t, dockerManifest)
				etag = `"docker-etag"`
			}
			w.Header().Set("Content-Type", mediaType)
			w.Header().Set("Docker-Content-Digest", digest.String())
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				env.conditional.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(http.StatusOK)
				return
			}
			_, _ = w.Write(body)
		case r.URL.Path == "/v2/demo/app/blobs/"+configDigest.String():
			env.blobCalls.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(config)
		case r.URL.Path == "/v2/demo/app/blobs/"+layerDigest.String():
			env.blobCalls.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(layer)
		case r.URL.Path == "/v2/demo/app/blobs/"+env.delayedDigest.String():
			env.blobCalls.Add(1)
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write(env.delayedBlob)
		case r.URL.Path == "/v2/demo/app/tags/list":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Link", `<`+env.upstream.URL+`/v2/demo/app/tags/list?n=1&last=latest>; rel="next"`)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "demo/app", "tags": []string{"latest"}})
		case strings.HasPrefix(r.URL.Path, "/v2/demo/app/referrers/"):
			w.Header().Set("Content-Type", oci.MediaTypeOCIIndex)
			w.Header().Set("OCI-Filters-Applied", "artifactType")
			_, _ = w.Write([]byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(env.upstream.Close)

	upstream, err := NewUpstream(UpstreamConfig{
		ID:               "mock",
		Endpoint:         env.upstream.URL,
		RouteHost:        "mock.registry.test",
		RoutePrefix:      "mock",
		RepositoryPrefix: "",
		AuthType:         "anonymous",
		TagTTL:           time.Minute,
		RequestTimeout:   5 * time.Second,
		MaxBlobBytes:     1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner([]byte("0123456789abcdef0123456789abcdef"), "crproxy", "crproxy", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := &auth.Authenticator{
		Store:     env.store,
		Signer:    signer,
		Realm:     "http://registry.test",
		Service:   "crproxy",
		DummyHash: auth.DummyPasswordHash(),
	}
	handler, err := NewHandler(http.NotFoundHandler(), env.store, casStore, authenticator, []*Upstream{upstream}, "r", 4<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	env.server = httptest.NewServer(handler)
	t.Cleanup(func() {
		env.server.Close()
		_ = env.db.Close()
	})
	return env
}

func (e *proxyTestEnv) request(t *testing.T, method, path string, host string, headers map[string]string) testHTTPResponse {
	return e.requestWithAuth(t, method, path, host, headers, true)
}

func (e *proxyTestEnv) requestNoAuth(t *testing.T, method, path string, host string, headers map[string]string) testHTTPResponse {
	return e.requestWithAuth(t, method, path, host, headers, false)
}

func (e *proxyTestEnv) requestWithAuth(t *testing.T, method, path string, host string, headers map[string]string, withAuth bool) testHTTPResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, e.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if withAuth {
		req.SetBasicAuth("admin", proxyTestPassword)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return testHTTPResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: raw}
}

type testHTTPResponse struct {
	status int
	header http.Header
	body   []byte
}

func mustDigest(t *testing.T, data []byte) oci.Digest {
	t.Helper()
	digest, err := oci.FromBytes("sha256", data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestProxyManifestBlobDiscoveryAndBoundaries(t *testing.T) {
	env := newProxyTestEnv(t)
	manifest := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/manifests/latest", "", nil)
	if manifest.status != http.StatusOK || !strings.Contains(string(manifest.body), `"schemaVersion":2`) {
		t.Fatalf("manifest proxy failed: status=%d body=%s", manifest.status, manifest.body)
	}
	manifestDigest := manifest.header.Get("Docker-Content-Digest")
	if manifestDigest == "" || env.tokenCalls.Load() != 1 {
		t.Fatalf("unexpected manifest response/token calls: headers=%v tokens=%d", manifest.header, env.tokenCalls.Load())
	}
	second := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/manifests/"+manifestDigest, "", nil)
	if second.status != http.StatusOK || !bytes.Equal(second.body, manifest.body) || env.tokenCalls.Load() != 1 {
		t.Fatalf("manifest cache/digest hit failed: status=%d tokens=%d", second.status, env.tokenCalls.Load())
	}

	configDigest := mustDigest(t, []byte("{}"))
	blob := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/blobs/"+configDigest.String(), "", nil)
	if blob.status != http.StatusOK || string(blob.body) != "{}" || env.blobCalls.Load() != 1 {
		t.Fatalf("blob proxy failed: status=%d body=%q calls=%d", blob.status, blob.body, env.blobCalls.Load())
	}
	ranged := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/blobs/"+configDigest.String(), "", map[string]string{"Range": "bytes=0-0"})
	if ranged.status != http.StatusPartialContent || string(ranged.body) != "{" || env.blobCalls.Load() != 1 {
		t.Fatalf("cached range failed: status=%d body=%q calls=%d", ranged.status, ranged.body, env.blobCalls.Load())
	}
	head := env.request(t, http.MethodHead, "/v2/r/mock/demo/app/blobs/"+configDigest.String(), "", nil)
	if head.status != http.StatusOK || head.header.Get("Content-Length") != "2" {
		t.Fatalf("cached blob head failed: status=%d headers=%v", head.status, head.header)
	}

	repo, err := env.store.RepositoryByName(t.Context(), "mock", "demo/app")
	if err != nil || repo.Kind != "cache" {
		t.Fatalf("cache repository not recorded: repo=%+v err=%v", repo, err)
	}
	if _, err := env.store.BlobContent(t.Context(), repo.ID, configDigest); err != nil {
		t.Fatalf("cache blob reference missing: %v", err)
	}

	tags := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/tags/list?n=1", "", nil)
	if tags.status != http.StatusOK || !strings.Contains(string(tags.body), `"name":"r/mock/demo/app"`) {
		t.Fatalf("tags proxy failed: status=%d body=%s", tags.status, tags.body)
	}
	if !strings.Contains(tags.header.Get("Link"), "/v2/r/mock/demo/app/tags/list?n=1&last=latest") {
		t.Fatalf("tags link not rewritten: %q", tags.header.Get("Link"))
	}
	referrers := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/referrers/"+manifestDigest, "", nil)
	if referrers.status != http.StatusOK || referrers.header.Get("OCI-Filters-Applied") != "artifactType" || !strings.Contains(string(referrers.body), `"manifests":[]`) {
		t.Fatalf("referrers proxy failed: status=%d headers=%v body=%s", referrers.status, referrers.header, referrers.body)
	}

	write := env.request(t, http.MethodPost, "/v2/r/mock/demo/app/blobs/uploads/", "", nil)
	if write.status != http.StatusMethodNotAllowed {
		t.Fatalf("proxy write should be 405, got %d", write.status)
	}
	catalog := env.request(t, http.MethodGet, "/v2/r/mock/_catalog", "", nil)
	if catalog.status != http.StatusNotFound {
		t.Fatalf("proxy catalog should be 404, got %d", catalog.status)
	}
	if env.writeCalls.Load() != 0 {
		t.Fatal("proxy write reached upstream")
	}
	badDigest := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/manifests/sha256:baddigest", "", nil)
	if badDigest.status != http.StatusNotFound {
		t.Fatalf("invalid digest should be rejected without upstream lookup, got %d", badDigest.status)
	}

	baseNoAuth := env.requestNoAuth(t, http.MethodGet, "/v2/", "mock.registry.test", nil)
	if baseNoAuth.status != http.StatusUnauthorized || !strings.Contains(baseNoAuth.header.Get("WWW-Authenticate"), `realm="http://registry.test/v2/token"`) {
		t.Fatalf("host-mode unauthenticated ping failed: status=%d challenge=%q", baseNoAuth.status, baseNoAuth.header.Get("WWW-Authenticate"))
	}
	baseAuth := env.request(t, http.MethodGet, "/v2/", "mock.registry.test", nil)
	if baseAuth.status != http.StatusOK || baseAuth.header.Get("Docker-Distribution-Api-Version") != "registry/2.0" {
		t.Fatalf("host-mode ping failed: status=%d headers=%v", baseAuth.status, baseAuth.header)
	}

	hostManifest := env.request(t, http.MethodGet, "/v2/demo/app/manifests/latest", "mock.registry.test", nil)
	if hostManifest.status != http.StatusOK {
		t.Fatalf("host-mode proxy failed: status=%d body=%s", hostManifest.status, hostManifest.body)
	}

	if _, err := env.db.ExecContext(t.Context(), `UPDATE tags SET checked_at = checked_at - 120 WHERE tag = 'latest'`); err != nil {
		t.Fatal(err)
	}
	conditionalBefore := env.conditional.Load()
	conditional := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/manifests/latest", "", nil)
	if conditional.status != http.StatusOK || !bytes.Equal(conditional.body, manifest.body) || conditional.header.Get("ETag") != `"mock-etag"` {
		t.Fatalf("conditional manifest refresh failed: status=%d etag=%q body=%s", conditional.status, conditional.header.Get("ETag"), conditional.body)
	}
	if env.conditional.Load() != conditionalBefore+1 || env.tokenCalls.Load() != 1 {
		t.Fatalf("expected one conditional request and cached token, conditional=%d tokens=%d", env.conditional.Load(), env.tokenCalls.Load())
	}
	fresh := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/manifests/latest", "", nil)
	if fresh.status != http.StatusOK || env.conditional.Load() != conditionalBefore+1 {
		t.Fatalf("refreshed tag should be fresh: status=%d conditional=%d", fresh.status, env.conditional.Load())
	}
}

func TestProxyTokenSingleflight(t *testing.T) {
	env := newProxyTestEnv(t)
	const clients = 8
	var wg sync.WaitGroup
	failures := make(chan string, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := env.request(t, http.MethodGet, "/v2/r/mock/demo/app/manifests/latest", "", nil)
			if resp.status != http.StatusOK {
				failures <- "unexpected response"
			}
		}()
	}
	wg.Wait()
	close(failures)
	for message := range failures {
		t.Fatal(message)
	}
	if calls := env.tokenCalls.Load(); calls != 1 {
		t.Fatalf("token singleflight expected 1 upstream token call, got %d", calls)
	}
}

func TestProxyManifestCacheSeparatesAcceptVariants(t *testing.T) {
	env := newProxyTestEnv(t)
	path := "/v2/r/mock/demo/app/manifests/latest"
	dockerHeaders := map[string]string{"Accept": oci.MediaTypeDockerManifest}
	dockerFirst := env.request(t, http.MethodGet, path, "", dockerHeaders)
	if dockerFirst.status != http.StatusOK || !strings.Contains(string(dockerFirst.body), oci.MediaTypeDockerManifest) {
		t.Fatalf("docker representation failed: status=%d body=%s", dockerFirst.status, dockerFirst.body)
	}
	if dockerFirst.header.Get("Vary") != "Accept" || dockerFirst.header.Get("ETag") != `"docker-etag"` {
		t.Fatalf("docker representation headers are incomplete: %v", dockerFirst.header)
	}
	dockerSecond := env.request(t, http.MethodGet, path, "", dockerHeaders)
	if dockerSecond.status != http.StatusOK || !bytes.Equal(dockerFirst.body, dockerSecond.body) || env.manifestCalls.Load() != 1 {
		t.Fatalf("docker representation was not cached independently: status=%d calls=%d", dockerSecond.status, env.manifestCalls.Load())
	}

	ociHeaders := map[string]string{"Accept": oci.MediaTypeOCIManifest}
	ociFirst := env.request(t, http.MethodGet, path, "", ociHeaders)
	if ociFirst.status != http.StatusOK || !strings.Contains(string(ociFirst.body), oci.MediaTypeOCIManifest) || bytes.Equal(ociFirst.body, dockerFirst.body) {
		t.Fatalf("OCI representation was mixed with Docker: status=%d body=%s", ociFirst.status, ociFirst.body)
	}
	ociSecond := env.request(t, http.MethodGet, path, "", ociHeaders)
	if ociSecond.status != http.StatusOK || !bytes.Equal(ociFirst.body, ociSecond.body) || env.manifestCalls.Load() != 2 {
		t.Fatalf("OCI representation was not cached independently: status=%d calls=%d", ociSecond.status, env.manifestCalls.Load())
	}
}

func TestProxyBlobSingleflight(t *testing.T) {
	env := newProxyTestEnv(t)
	path := "/v2/r/mock/demo/app/blobs/" + env.delayedDigest.String()
	var wg sync.WaitGroup
	errorsCh := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := env.request(t, http.MethodGet, path, "", nil)
			if resp.status != http.StatusOK || !bytes.Equal(resp.body, env.delayedBlob) {
				errorsCh <- "unexpected response"
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for message := range errorsCh {
		t.Fatal(message)
	}
	if calls := env.blobCalls.Load(); calls != 1 {
		t.Fatalf("singleflight expected 1 upstream blob call, got %d", calls)
	}
}
