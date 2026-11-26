package cas

import (
	"os"
	"strings"
	"testing"

	"crproxy/internal/oci"
)

func TestBlobStoreAndOpen(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := oci.FromBytes("sha256", []byte("blob-data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBytes(digest, []byte("blob-data")); err != nil {
		t.Fatal(err)
	}
	exists, err := store.BlobExists(digest)
	if err != nil || !exists {
		t.Fatalf("blob should exist: exists=%v err=%v", exists, err)
	}
	file, _, err := store.OpenBlob(digest)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	buf := make([]byte, 9)
	if _, err := file.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "blob-data" {
		t.Fatalf("unexpected content %q", buf)
	}
}

func TestUploadLifecycle(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewUploadID()
	if err != nil {
		t.Fatal(err)
	}
	file, err := store.CreateUpload(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, tooLarge, err := store.AppendUploadLimited(id, strings.NewReader("hello"), 5); err != nil || tooLarge {
		t.Fatalf("append failed: tooLarge=%v err=%v", tooLarge, err)
	}
	if _, tooLarge, err := store.AppendUploadLimited(id, strings.NewReader("!"), 5); err != nil || !tooLarge {
		t.Fatalf("expected size limit rejection: tooLarge=%v err=%v", tooLarge, err)
	}
	size, err := store.UploadSize(id)
	if err != nil || size != 5 {
		t.Fatalf("upload should remain at 5 bytes: size=%d err=%v", size, err)
	}
	digest, err := oci.FromBytes("sha256", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitUpload(id, digest); err != nil {
		t.Fatal(err)
	}
	data, err := store.ReadBlob(digest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected committed blob %q", data)
	}
	if _, err := os.Stat(file.Name()); !os.IsNotExist(err) {
		t.Fatalf("upload part should be removed, got err=%v", err)
	}
}

func TestUploadIDCannotEscapeDirectory(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUpload("../../escape"); err == nil {
		t.Fatal("expected invalid upload id to be rejected")
	}
}
