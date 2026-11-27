package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kube-dosh/crproxy/internal/cas"
	"github.com/kube-dosh/crproxy/internal/oci"
	"github.com/kube-dosh/crproxy/internal/store"
)

func newTestManager(t *testing.T, maxBytes, cacheMaxBytes int64) (*Manager, *store.Store) {
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
	manager, err := New(metadata, casStore, Config{Root: root, Settings: Settings{MaxBytes: maxBytes, CacheMaxBytes: cacheMaxBytes, MinFreeBytes: 0, HighWatermark: 0.85, LowWatermark: 0.70, OnFull: "bypass"}, DownloadConcurrency: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager, metadata
}

func TestQuotaChecksAndBypassDecision(t *testing.T) {
	manager, _ := newTestManager(t, 100, 50)
	if err := manager.CheckLocalWrite(t.Context(), 101); !errors.Is(err, ErrInsufficientStorage) {
		t.Fatalf("expected quota error, got %v", err)
	}
	if err := manager.CheckLocalWrite(t.Context(), 100); err != nil {
		t.Fatalf("write at exact quota should be allowed: %v", err)
	}
	decision, err := manager.CanCache(t.Context(), 51)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Cache || !decision.Bypass {
		t.Fatalf("expected bypass decision, got %+v", decision)
	}
	settings := manager.Settings()
	settings.OnFull = "reject"
	if _, err := manager.UpdateSettings(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	decision, err = manager.CanCache(t.Context(), 51)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Cache || decision.Bypass || decision.HTTPCode != 507 {
		t.Fatalf("expected reject decision, got %+v", decision)
	}
}

func TestEvictCachePreservesSharedLocalContent(t *testing.T) {
	manager, metadata := newTestManager(t, 1<<30, 1<<29)
	ctx := context.Background()
	if err := metadata.UpsertUpstream(ctx, store.Upstream{ID: "quay", Name: "quay", Endpoint: "https://quay.io", RoutePrefix: "quay", AuthType: "anonymous", TagTTLSeconds: 60, RequestTimeoutSecs: 30, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	local, err := metadata.EnsureLocalRepository(ctx, "shared/app")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := metadata.EnsureCacheRepository(ctx, "quay", "shared/app")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := oci.ParseDigest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.PutBlobRef(ctx, local.ID, digest, 42, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	if err := metadata.PutBlobRef(ctx, cache.ID, digest, 42, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	result, err := manager.EvictRepository(ctx, cache.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.EvictedRepos != 1 {
		t.Fatalf("unexpected eviction result %+v", result)
	}
	exists, err := metadata.BlobRefExists(ctx, local.ID, digest)
	if err != nil || !exists {
		t.Fatalf("local reference must survive cache eviction: %v", err)
	}
	exists, err = metadata.BlobRefExists(ctx, cache.ID, digest)
	if err != nil || exists {
		t.Fatalf("cache reference must be removed: %v", err)
	}
	status := manager.GCStatus()
	if status.Running {
		t.Fatal("GC should not remain marked running")
	}
}
