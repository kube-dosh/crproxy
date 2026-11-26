package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "meta", "crproxy.db"), []byte("sqlite-data"), 0o640)
	writeFile(t, filepath.Join(root, "meta", "master.key"), []byte("key-material"), 0o600)
	writeFile(t, filepath.Join(root, "cas", "sha256", "aa", "blob"), []byte("blob-content"), 0o640)
	writeFile(t, filepath.Join(root, "uploads", "upl_1", "part-0"), []byte("upload"), 0o600)
	writeFile(t, filepath.Join(root, "trash", "sha256", "discard"), []byte("discard"), 0o640)
	writeFile(t, filepath.Join(root, "LOCK"), []byte(""), 0o600)
	output := filepath.Join(t.TempDir(), "backup.tar.gz")
	result, err := Create(root, output, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 4 {
		t.Fatalf("unexpected backup file count %d", result.Files)
	}
	if _, err := os.Stat(output + ".sha256"); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "data")
	if err := Restore(output, restored, false); err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string]string{
		"meta/crproxy.db":      "sqlite-data",
		"meta/master.key":      "key-material",
		"cas/sha256/aa/blob":   "blob-content",
		"uploads/upl_1/part-0": "upload",
	} {
		raw, err := os.ReadFile(filepath.Join(restored, path))
		if err != nil {
			t.Fatalf("read restored %s: %v", path, err)
		}
		if string(raw) != expected {
			t.Fatalf("unexpected content for %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(restored, "trash")); !os.IsNotExist(err) {
		t.Fatal("trash must not be included in backup")
	}
	if err := Restore(output, restored, false); err == nil {
		t.Fatal("restore into non-empty directory must fail without force")
	}
	if err := Restore(output, restored, true); err != nil {
		t.Fatalf("force restore failed: %v", err)
	}
}

func TestBackupRejectsChecksumMismatchAndUnsafeArchive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "meta", "crproxy.db"), []byte("db"), 0o640)
	output := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := Create(root, output, "test"); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if err := os.WriteFile(output+".sha256", []byte("0000000000000000000000000000000000000000000000000000000000000000  backup.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(output, filepath.Join(t.TempDir(), "data"), false); err == nil {
		t.Fatal("checksum mismatch must be rejected")
	}
	unsafe := filepath.Join(t.TempDir(), "unsafe.tar.gz")
	writeArchive(t, unsafe, "../escape", []byte("bad"))
	if err := Restore(unsafe, filepath.Join(t.TempDir(), "data2"), false); err == nil {
		t.Fatal("path traversal archive must be rejected")
	}
}

func writeFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func writeArchive(t *testing.T, path, name string, content []byte) {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	manifest := []byte(`{"format":"crproxy-offline-backup-v1"}`)
	if err := tarWriter.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifest)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
