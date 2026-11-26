package upstreammgr

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"crproxy/internal/secretbox"
	"crproxy/internal/store"
)

func TestRotateMasterKeyRewrapsCredentialsAndResumes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "meta", "crproxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	metadata := store.New(db)
	keyring, err := secretbox.LoadOrCreateKeyring(filepath.Join(root, "meta", "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.UpsertUpstream(ctx, store.Upstream{ID: "ghcr", Name: "ghcr", Endpoint: "https://ghcr.io", RoutePrefix: "ghcr", AuthType: "basic", TagTTLSeconds: 60, RequestTimeoutSecs: 30, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := Credentials{Username: "robot", Password: "secret-password"}
	encoded, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	record, err := metadata.UpstreamByID(ctx, "ghcr")
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := keyring.Sealer(1)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := sealer.Seal(encoded, credentialsAAD("ghcr", record.CredentialsVersion+1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.SetUpstreamCredentials(ctx, "ghcr", ciphertext, 1); err != nil {
		t.Fatal(err)
	}
	result, err := RotateMasterKey(ctx, metadata, keyring)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GeneratedKey || result.CurrentVersion != 2 || result.Rewrapped != 1 {
		t.Fatalf("unexpected rotation result %+v", result)
	}
	assertCredentials(t, ctx, metadata, keyring, "ghcr", "robot", "secret-password", 2)
	// A second invocation with an interrupted record must reuse the current key.
	record, err = metadata.UpstreamByID(ctx, "ghcr")
	if err != nil {
		t.Fatal(err)
	}
	oldSealer, err := keyring.Sealer(1)
	if err != nil {
		t.Fatal(err)
	}
	oldCipher, err := oldSealer.Seal(encoded, credentialsAAD("ghcr", record.CredentialsVersion+1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.SetUpstreamCredentials(ctx, "ghcr", oldCipher, 1); err != nil {
		t.Fatal(err)
	}
	resumed, err := RotateMasterKey(ctx, metadata, keyring)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.GeneratedKey || resumed.CurrentVersion != 2 {
		t.Fatalf("resume must not generate a second key: %+v", resumed)
	}
	assertCredentials(t, ctx, metadata, keyring, "ghcr", "robot", "secret-password", 2)
}

func assertCredentials(t *testing.T, ctx context.Context, metadata *store.Store, keyring *secretbox.Keyring, id, username, password string, expectedVersion int64) {
	t.Helper()
	record, err := metadata.UpstreamByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if record.CredentialsKeyVer != expectedVersion {
		t.Fatalf("unexpected key version %d", record.CredentialsKeyVer)
	}
	sealer, err := keyring.Sealer(record.CredentialsKeyVer)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := sealer.Open(record.Credentials, credentialsAAD(record.ID, record.CredentialsVersion))
	if err != nil {
		t.Fatal(err)
	}
	var credentials Credentials
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials.Username != username || credentials.Password != password {
		t.Fatalf("unexpected credentials %+v", credentials)
	}
}
