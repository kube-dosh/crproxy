package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenUsesRollbackJournalAndForeignKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta", "crproxy.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var journalMode string
	if err := db.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "delete" {
		t.Fatalf("expected rollback journal mode delete, got %q", journalMode)
	}
	var foreignKeys int
	if err := db.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign keys must be enabled, got %d", foreignKeys)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("unexpected sqlite sidecar %s: %v", suffix, err)
		}
	}
}

func TestUploadsAllowAnonymousOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta", "crproxy.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	metadata := New(db)
	repository, err := metadata.EnsureLocalRepository(context.Background(), "anon/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.CreateUpload(context.Background(), "anon-upload", repository.ID, "", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	upload, err := metadata.UploadByID(context.Background(), "anon-upload")
	if err != nil {
		t.Fatal(err)
	}
	if upload.UserID != "" {
		t.Fatalf("anonymous upload owner should be empty, got %q", upload.UserID)
	}
	owner, err := metadata.UploadOwner(context.Background(), "anon-upload")
	if err != nil || owner != "" {
		t.Fatalf("unexpected anonymous upload owner %q: %v", owner, err)
	}
}
