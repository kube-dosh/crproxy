package observability

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kube-dosh/crproxy/internal/cas"
	"github.com/kube-dosh/crproxy/internal/proxy"
	"github.com/kube-dosh/crproxy/internal/secretbox"
	"github.com/kube-dosh/crproxy/internal/storage"
	"github.com/kube-dosh/crproxy/internal/store"
	"github.com/kube-dosh/crproxy/internal/upstreammgr"
)

func newTestCollector(t *testing.T) *Collector {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(root, "meta", "crproxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	metadata := store.New(db)
	casStore, err := cas.New(root)
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler, err := proxy.NewHandler(http.NotFoundHandler(), metadata, casStore, nil, nil, "r", 4<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secretbox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	upstreams, err := upstreammgr.New(metadata, proxyHandler, sealer, 1, nil)
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
	collector, err := New(metadata, storageManager, upstreams, "test-version", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return collector
}

func TestCollectorUsesBoundedRouteLabelsAndLiveGauges(t *testing.T) {
	collector := newTestCollector(t)
	handler := collector.Instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			http.Error(w, "missing", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/repositories/42/manifests?secret=value", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v2/library/alpine/manifests/missing", nil))

	recorder := httptest.NewRecorder()
	collector.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"crproxy_build_info{version=\"test-version\"} 1",
		"crproxy_http_requests_total{method=\"GET\",route=\"api_repositories\",status=\"200\"} 1",
		"crproxy_http_requests_total{method=\"GET\",route=\"registry_manifests\",status=\"503\"} 1",
		"crproxy_http_request_duration_seconds_count{method=\"GET\",route=\"api_repositories\"} 1",
		"crproxy_storage_max_bytes 1073741824",
		"crproxy_cache_max_bytes 536870912",
		"crproxy_scrape_error 0",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics output missing %q", expected)
		}
	}
	if strings.Contains(body, "repositories/42") || strings.Contains(body, "secret=value") {
		t.Fatal("metrics output must not expose unbounded request paths or query values")
	}
}

func TestClassifyRouteKeepsCardinalityBounded(t *testing.T) {
	tests := map[string]string{
		"/api/v1/readyz":                    "health",
		"/api/v1/repositories/1/tags":       "api_repositories",
		"/api/v1/cache/entries?page=2":      "api_cache",
		"/api/v1/users/abc":                 "api_identity",
		"/v2/":                              "registry_ping",
		"/v2/token":                         "registry_token",
		"/v2/team/image/blobs/sha256:abc":   "registry_blobs",
		"/v2/team/image/blobs/uploads/uuid": "registry_uploads",
		"/v2/team/image/manifests/latest":   "registry_manifests",
		"/v2/team/image/tags/list":          "registry_tags",
		"/v2/_catalog":                      "registry_catalog",
		"/assets/index-abc.js":              "ui_assets",
		"/repositories/team/image":          "ui",
		"/unexpected/unbounded/value/that/must/not/be/a/label": "ui",
	}
	for path, expected := range tests {
		if got := classifyRoute(path); got != expected {
			t.Errorf("classifyRoute(%q) = %q, want %q", path, got, expected)
		}
	}
}

func TestEscapeLabel(t *testing.T) {
	got := escapeLabel("line\nquote\"slash\\")
	want := `line\nquote\"slash\\`
	if got != want {
		t.Fatalf("escapeLabel() = %q, want %q", got, want)
	}
}
