package runtimeconfig

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"crproxy/internal/secretbox"
	"crproxy/internal/store"
)

type testKeys struct {
	sealer *secretbox.Sealer
}

func newTestKeys(t *testing.T) *testKeys {
	t.Helper()
	sealer, err := secretbox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return &testKeys{sealer: sealer}
}

func (k *testKeys) CurrentVersion() int64 { return 1 }
func (k *testKeys) Sealer(version int64) (*secretbox.Sealer, error) {
	if version != 1 {
		return nil, ErrInvalid
	}
	return k.sealer, nil
}

func testBase() Base {
	return Base{
		RegistryAuth: RegistryAuth{
			Challenge: "bearer",
			Service:   "crproxy",
			Realm:     "http://registry.test",
			TokenTTL:  "5m",
		},
		WebSession: WebSession{SessionTTL: "12h", IdleTimeout: "30m"},
		Storage: Storage{
			MaxBytes:            100 << 30,
			CacheMaxBytes:       60 << 30,
			MinFreeBytes:        5 << 30,
			HighWatermark:       0.85,
			LowWatermark:        0.70,
			OnFull:              "bypass",
			MaxBlobBytes:        20 << 30,
			MaxManifestBytes:    4 << 20,
			DownloadConcurrency: 8,
			TmpTTL:              "1h",
			TrashTTL:            "24h",
		},
		Uploads:  Uploads{UploadsTTL: "24h"},
		Audit:    Audit{AuditRetention: "2160h"},
		Proxy:    Proxy{},
		Outbound: OutboundProxy{},
		Security: Security{},
		Startup: Startup{
			Listen:          ":5000",
			PublicHost:      "registry.test",
			PublicScheme:    "http",
			ProxyPathPrefix: "r",
		},
	}
}

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(root, "meta", "crproxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	metadata := store.New(db)
	manager, err := New(t.Context(), metadata, testBase(), newTestKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	return manager, metadata
}

func TestManagerUpdatesValidatesAndResetsSections(t *testing.T) {
	manager, metadata := newTestManager(t)
	current := manager.Current()
	if current == nil || current.Sections[SectionRegistryAuth].Version != 0 || current.Sections[SectionRegistryAuth].Source != "startup" {
		t.Fatalf("unexpected initial snapshot: %+v", current)
	}

	view, err := manager.UpdateRegistryAuth(t.Context(), 0, "admin", RegistryAuth{
		Challenge:        "bearer",
		Service:          "crproxy-edge",
		Realm:            "https://registry.test",
		AllowAnonymous:   true,
		AnonymousActions: []string{"push", "pull", "push"},
		TokenTTL:         "10m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Sections[SectionRegistryAuth].Version != 1 || !view.RegistryAuth.AllowAnonymous || view.RegistryAuth.Service != "crproxy-edge" || strings.Join(view.RegistryAuth.AnonymousActions, ",") != "pull,push" {
		t.Fatalf("unexpected auth update: %+v", view.RegistryAuth)
	}
	if _, err := manager.UpdateRegistryAuth(t.Context(), 0, "admin", RegistryAuth{
		Challenge: "bearer",
		Service:   "crproxy",
		Realm:     "http://registry.test",
		TokenTTL:  "5m",
	}); err == nil {
		t.Fatal("stale expectedVersion must conflict")
	} else if _, ok := err.(*ConflictError); !ok {
		t.Fatalf("expected conflict, got %T %v", err, err)
	}

	if _, err := manager.UpdateWebSession(t.Context(), 0, "admin", WebSession{SessionTTL: "30m", IdleTimeout: "1h"}); err == nil {
		t.Fatal("idle timeout above session ttl must be rejected")
	}

	view, err = manager.UpdateStorage(t.Context(), 0, "operator", Storage{
		MaxBytes:            50 << 30,
		CacheMaxBytes:       20 << 30,
		MinFreeBytes:        1 << 30,
		HighWatermark:       0.9,
		LowWatermark:        0.5,
		OnFull:              "reject",
		MaxBlobBytes:        10 << 30,
		MaxManifestBytes:    8 << 20,
		DownloadConcurrency: 4,
		TmpTTL:              "2h",
		TrashTTL:            "48h",
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Storage.DownloadConcurrency != 4 || view.Sections[SectionStorage].UpdatedBy != "operator" {
		t.Fatalf("unexpected storage update: %+v", view.Storage)
	}

	if _, err := manager.UpdateUploads(t.Context(), 0, "operator", Uploads{UploadsTTL: "6h"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateAudit(t.Context(), 0, "admin", Audit{AuditRetention: "720h"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateProxy(t.Context(), 0, "admin", Proxy{Disabled: true}); err != nil {
		t.Fatal(err)
	}

	view, err = manager.UpdateOutboundProxy(t.Context(), 0, "admin", OutboundProxyInput{
		HTTPProxy:  "http://127.0.0.1:1080",
		HTTPSProxy: "http://127.0.0.1:1080",
		NoProxy:    "localhost,127.0.0.1",
		Username:   "proxy-user",
		Password:   "proxy-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.OutboundProxy.PasswordConfigured || view.OutboundProxy.PasswordSource != "runtime" {
		t.Fatalf("unexpected outbound proxy view: %+v", view.OutboundProxy)
	}
	raw, err := metadata.Setting(t.Context(), settingKey(SectionOutbound))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "proxy-password") {
		t.Fatal("outbound proxy password must not be stored in plaintext")
	}
	reloaded, err := New(t.Context(), metadata, testBase(), newTestKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Current().Outbound.Password != "proxy-password" {
		t.Fatal("outbound proxy password was not decrypted after reload")
	}

	view, err = manager.ResetSection(t.Context(), SectionRegistryAuth, 1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if view.RegistryAuth.Service != "crproxy" || view.Sections[SectionRegistryAuth].Version != 0 || view.Sections[SectionRegistryAuth].Source != "startup" {
		t.Fatalf("reset did not restore startup baseline: %+v", view.RegistryAuth)
	}
	if _, err := metadata.Setting(t.Context(), settingKey(SectionRegistryAuth)); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("reset should delete the runtime section key")
	}
}

func TestManagerMigratesLegacyStorageSettings(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(root, "meta", "crproxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	metadata := store.New(db)
	legacy := legacyStorageSettings{
		MaxBytes:      32 << 30,
		CacheMaxBytes: 16 << 30,
		MinFreeBytes:  2 << 30,
		HighWatermark: 0.80,
		LowWatermark:  0.40,
		OnFull:        "reject",
	}
	raw, _ := json.Marshal(legacy)
	if err := metadata.SetSetting(t.Context(), "storage.settings", string(raw)); err != nil {
		t.Fatal(err)
	}
	manager, err := New(t.Context(), metadata, testBase(), newTestKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	current := manager.Current()
	if current.Storage.MaxBytes != legacy.MaxBytes || current.Storage.OnFull != "reject" {
		t.Fatalf("legacy storage settings were not migrated: %+v", current.Storage)
	}
	meta := current.Sections[SectionStorage]
	if meta.Source != "runtime" || meta.Version != 1 || meta.UpdatedBy != "migration" {
		t.Fatalf("unexpected migration metadata: %+v", meta)
	}
	if _, err := metadata.Setting(t.Context(), "storage.settings"); err == nil {
		t.Fatal("legacy storage key should be removed after migration")
	}
}

func TestManagerRejectsInvalidOutboundPasswordState(t *testing.T) {
	manager, _ := newTestManager(t)
	if _, err := manager.UpdateOutboundProxy(t.Context(), 0, "admin", OutboundProxyInput{
		HTTPProxy: "http://127.0.0.1:1080",
		Username:  "proxy-user",
	}); err == nil {
		t.Fatal("username without password must be rejected")
	}
	if _, err := manager.UpdateOutboundProxy(t.Context(), 0, "admin", OutboundProxyInput{
		HTTPSProxy: "http://127.0.0.1:1080",
		Password:   "orphan-password",
	}); err == nil {
		t.Fatal("password without username must be rejected")
	}
}

func TestViewSerializesEmptyTrustedProxiesAsArray(t *testing.T) {
	manager, _ := newTestManager(t)
	raw, err := json.Marshal(manager.View())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"trustedProxies":null`) {
		t.Fatalf("trustedProxies must serialize as an array: %s", raw)
	}
	if !strings.Contains(string(raw), `"trustedProxies":[]`) {
		t.Fatalf("unexpected trustedProxies serialization: %s", raw)
	}
}
