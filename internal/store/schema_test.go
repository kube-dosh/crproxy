package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
