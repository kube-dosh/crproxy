package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kube-dosh/crproxy/internal/config"
	"github.com/kube-dosh/crproxy/internal/requestinfo"
)

func TestAcquireLockRejectsSecondProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "LOCK")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(path); err == nil {
		t.Fatal("second lock acquisition must fail")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireLock(path)
	if err != nil {
		t.Fatalf("lock should be reusable after close: %v", err)
	}
	_ = third.Close()
}

func TestLoadJWTSecretPersistsAndReuses(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Auth.JWTSecretFile = ""
	first, err := loadJWTSecret(cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Fatalf("unexpected secret length %d", len(first))
	}
	info, err := os.Stat(filepath.Join(root, "meta", "jwt.secret"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected secret permissions %o", info.Mode().Perm())
	}
	second, err := loadJWTSecret(cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("JWT secret should be stable across restarts")
	}
}

func TestHealthcheckSucceedsOnlyOn2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/readyz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	defer server.Close()
	if err := runHealthcheck([]string{"-url", server.URL + "/api/v1/readyz"}); err != nil {
		t.Fatalf("healthcheck should succeed: %v", err)
	}
	if err := runHealthcheck([]string{"-url", server.URL + "/missing"}); err == nil {
		t.Fatal("healthcheck must reject a non-2xx response")
	}
}

func TestHealthcheckRejectsInvalidTimeout(t *testing.T) {
	if err := runHealthcheck([]string{"-url", "http://127.0.0.1", "-timeout", "0s"}); err == nil {
		t.Fatal("non-positive timeout must fail")
	}
}

func TestSecurityHeadersReadHSTSDynamically(t *testing.T) {
	resolver, err := requestinfo.New(nil, "https")
	if err != nil {
		t.Fatal(err)
	}
	enabled := false
	handler := securityHeadersMiddleware(resolver, func() bool { return enabled }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "https://registry.test/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS must be absent while disabled")
	}
	enabled = true
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("HSTS must be emitted immediately after enabling")
	}
}
