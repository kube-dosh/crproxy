package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestUpsertUpstreamWithCredentialsIsAtomic(t *testing.T) {
	ctx := context.Background()
	metadata := newUpstreamTestStore(t)
	base := Upstream{
		ID: "ghcr", Name: "ghcr", Endpoint: "https://ghcr.io", RoutePrefix: "ghcr",
		AuthType: "basic", TagTTLSeconds: 60, RequestTimeoutSecs: 30, Enabled: true,
	}
	version, err := metadata.UpsertUpstreamWithCredentials(ctx, base, []byte("credential-v1"), 1, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("expected initial credential version 1, got %d", version)
	}

	updated := base
	updated.Name = "github-container-registry"
	version, err = metadata.UpsertUpstreamWithCredentials(ctx, updated, []byte("credential-v2"), 2, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("expected credential version 2, got %d", version)
	}
	record, err := metadata.UpstreamByID(ctx, "ghcr")
	if err != nil {
		t.Fatal(err)
	}
	if record.Name != updated.Name || string(record.Credentials) != "credential-v2" || record.CredentialsVersion != 2 || record.CredentialsKeyVer != 2 {
		t.Fatalf("updated upstream is inconsistent: %+v", record)
	}

	conflicting := base
	conflicting.Name = "must-not-persist"
	if _, err := metadata.UpsertUpstreamWithCredentials(ctx, conflicting, []byte("credential-v3"), 2, true, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	record, err = metadata.UpstreamByID(ctx, "ghcr")
	if err != nil {
		t.Fatal(err)
	}
	if record.Name != updated.Name || string(record.Credentials) != "credential-v2" || record.CredentialsVersion != 2 {
		t.Fatalf("conflicting update must be rolled back: %+v", record)
	}
}

func TestUpsertUpstreamWithCredentialsRollsBackBaseDefinition(t *testing.T) {
	ctx := context.Background()
	metadata := newUpstreamTestStore(t)
	first := Upstream{ID: "one", Name: "shared-name", Endpoint: "https://one.example.com", RoutePrefix: "one", AuthType: "anonymous", Enabled: true}
	if err := metadata.UpsertUpstream(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := Upstream{ID: "two", Name: "shared-name", Endpoint: "https://two.example.com", RoutePrefix: "two", AuthType: "anonymous", Enabled: true}
	if _, err := metadata.UpsertUpstreamWithCredentials(ctx, second, []byte("credential"), 1, true, 0); err == nil {
		t.Fatal("unique name conflict must fail the transaction")
	}
	if _, err := metadata.UpstreamByID(ctx, "two"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed upsert must not leave a partial upstream, got %v", err)
	}
	if _, err := metadata.UpsertUpstreamWithCredentials(ctx, second, []byte("credential"), 1, true, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("new rows require expected version zero, got %v", err)
	}
	if _, err := metadata.UpstreamByID(ctx, "two"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("version guard must not create an upstream, got %v", err)
	}
}

func newUpstreamTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "meta", "crproxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}
