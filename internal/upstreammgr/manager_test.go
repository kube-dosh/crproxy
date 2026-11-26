package upstreammgr

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"crproxy/internal/cas"
	"crproxy/internal/proxy"
	"crproxy/internal/secretbox"
	"crproxy/internal/store"
)

type managerTestEnv struct {
	manager *Manager
	store   *store.Store
}

func newManagerTestEnv(t *testing.T) managerTestEnv {
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
	keyring, err := secretbox.LoadOrCreateKeyring(filepath.Join(root, "meta", "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewWithKeyProvider(metadata, proxyHandler, keyring, nil)
	if err != nil {
		t.Fatal(err)
	}
	return managerTestEnv{manager: manager, store: metadata}
}

func TestManagerCreateUpdateAndPreserveCredentials(t *testing.T) {
	ctx := context.Background()
	env := newManagerTestEnv(t)
	created, err := env.manager.Create(ctx, UpsertInput{
		ID:                 "ghcr",
		Name:               "ghcr",
		Endpoint:           "https://ghcr.io",
		RoutePrefix:        "ghcr",
		RouteHost:          "GHCR.EXAMPLE.COM",
		RepositoryPrefix:   "team/",
		AuthType:           "basic",
		TagTTLSeconds:      90,
		RequestTimeoutSecs: 45,
		Enabled:            true,
		ReplaceCredentials: true,
		Credentials:        Credentials{Username: "robot", Password: "super-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "ghcr" || created.RouteHost != "ghcr.example.com" || created.RepositoryPrefix != "team" || created.CredentialsVersion != 1 {
		t.Fatalf("unexpected created upstream: %+v", created)
	}
	credentials, err := env.manager.Credentials(ctx, "ghcr")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Username != "robot" || credentials.Password != "super-secret" {
		t.Fatalf("credentials were not sealed and decrypted correctly: %+v", credentials)
	}
	if _, err := env.manager.Create(ctx, UpsertInput{ID: "ghcr", Endpoint: "https://other.example.com", AuthType: "anonymous", Enabled: true}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate create must conflict, got %v", err)
	}
	record, err := env.store.UpstreamByID(ctx, "ghcr")
	if err != nil {
		t.Fatal(err)
	}
	if record.Endpoint != "https://ghcr.io" {
		t.Fatalf("duplicate create changed the existing upstream: %+v", record)
	}

	updated, err := env.manager.Upsert(ctx, UpsertInput{
		ID:                 "ghcr",
		Name:               "github-container-registry",
		Endpoint:           "https://ghcr.io",
		RoutePrefix:        "ghcr",
		RepositoryPrefix:   "team",
		AuthType:           "basic",
		TagTTLSeconds:      120,
		RequestTimeoutSecs: 30,
		Enabled:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "github-container-registry" || updated.CredentialsVersion != created.CredentialsVersion {
		t.Fatalf("credential-preserving update is inconsistent: before=%+v after=%+v", created, updated)
	}
	credentials, err = env.manager.Credentials(ctx, "ghcr")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Username != "robot" || credentials.Password != "super-secret" {
		t.Fatalf("credentials changed during a preserve update: %+v", credentials)
	}

	if _, err := env.manager.Upsert(ctx, UpsertInput{
		ID: "ghcr", Name: "github-container-registry", Endpoint: "https://ghcr.io", RoutePrefix: "ghcr",
		AuthType: "basic", Enabled: true, ReplaceCredentials: true,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing basic credentials must be rejected, got %v", err)
	}
	record, err = env.store.UpstreamByID(ctx, "ghcr")
	if err != nil {
		t.Fatal(err)
	}
	if record.Name != updated.Name || string(record.Credentials) == "" {
		t.Fatalf("invalid update changed persisted state: %+v", record)
	}
}

func TestManagerRejectsInvalidUpstreamBeforePersisting(t *testing.T) {
	ctx := context.Background()
	env := newManagerTestEnv(t)
	tests := []struct {
		name  string
		input UpsertInput
	}{
		{"http endpoint", UpsertInput{ID: "one", Endpoint: "http://registry.example.com", AuthType: "anonymous", Enabled: true}},
		{"invalid route host", UpsertInput{ID: "two", Endpoint: "https://registry.example.com", RouteHost: "https://registry.example.com", AuthType: "anonymous", Enabled: true}},
		{"invalid repository prefix", UpsertInput{ID: "three", Endpoint: "https://registry.example.com", RepositoryPrefix: "Bad Prefix", AuthType: "anonymous", Enabled: true}},
		{"invalid CA", UpsertInput{ID: "four", Endpoint: "https://registry.example.com", CAPEM: "not a certificate", AuthType: "anonymous", Enabled: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := env.manager.Create(ctx, test.input); err == nil {
				t.Fatal("invalid upstream must be rejected")
			}
			if _, err := env.store.UpstreamByID(ctx, test.input.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("invalid upstream was partially persisted: %v", err)
			}
		})
	}
}
