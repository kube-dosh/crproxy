package cas

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"crproxy/internal/oci"
)

var uploadIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type Store struct {
	root string
}

func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("cas root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{
		abs,
		filepath.Join(abs, "cas"),
		filepath.Join(abs, "uploads"),
		filepath.Join(abs, "tmp"),
		filepath.Join(abs, "trash"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	return &Store{root: abs}, nil
}

func (s *Store) Root() string {
	return s.root
}

func NewUploadID() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func validUploadID(id string) bool {
	return uploadIDPattern.MatchString(id)
}

func (s *Store) blobPath(d oci.Digest) (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	hex := d.Encoded
	return filepath.Join(s.root, "cas", d.Algorithm, hex[0:2], hex[2:4], hex), nil
}

func (s *Store) uploadPath(id string) (string, error) {
	if !validUploadID(id) {
		return "", fmt.Errorf("invalid upload id")
	}
	return filepath.Join(s.root, "uploads", id+".part"), nil
}

func (s *Store) BlobExists(d oci.Digest) (bool, error) {
	path, err := s.blobPath(d)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().IsRegular(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (s *Store) OpenBlob(d oci.Digest) (*os.File, os.FileInfo, error) {
	path, err := s.blobPath(d)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("not a regular file")
	}
	return f, info, nil
}

func (s *Store) PutBytes(d oci.Digest, data []byte) error {
	path, err := s.blobPath(d)
	if err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "blob-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return fsyncDir(filepath.Dir(path))
}

func (s *Store) CreateUpload(id string) (*os.File, error) {
	path, err := s.uploadPath(id)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
}

func (s *Store) OpenUpload(id string, appendMode bool) (*os.File, error) {
	path, err := s.uploadPath(id)
	if err != nil {
		return nil, err
	}
	flags := os.O_RDONLY | syscall.O_NOFOLLOW
	if appendMode {
		flags = os.O_WRONLY | os.O_APPEND | syscall.O_NOFOLLOW
	}
	return os.OpenFile(path, flags, 0)
}

func (s *Store) UploadSize(id string) (int64, error) {
	path, err := s.uploadPath(id)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *Store) AppendUpload(id string, r io.Reader) (int64, error) {
	f, err := s.OpenUpload(id, true)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		return n, err
	}
	return n, f.Sync()
}

// AppendUploadLimited appends a request body while preserving the upload if it
// would exceed maxFileSize. It returns the number of bytes appended.
func (s *Store) AppendUploadLimited(id string, r io.Reader, maxFileSize int64) (int64, bool, error) {
	f, err := s.OpenUpload(id, true)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	originalSize := info.Size()
	remaining := maxFileSize - originalSize
	if remaining < 0 {
		return 0, true, nil
	}
	n, err := io.Copy(f, io.LimitReader(r, remaining+1))
	if err != nil {
		return n, false, err
	}
	tooLarge := n > remaining
	if tooLarge {
		if err := f.Truncate(originalSize); err != nil {
			return n, true, err
		}
	}
	if err := f.Sync(); err != nil {
		return n, tooLarge, err
	}
	if tooLarge {
		return n, true, nil
	}
	return n, false, nil
}

func (s *Store) TruncateUpload(id string, size int64) error {
	f, err := s.OpenUpload(id, true)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		return err
	}
	return f.Sync()
}

func (s *Store) CommitUpload(id string, d oci.Digest) error {
	if err := d.Validate(); err != nil {
		return err
	}
	src, err := s.uploadPath(id)
	if err != nil {
		return err
	}
	dest, err := s.blobPath(d)
	if err != nil {
		return err
	}
	if info, err := os.Stat(dest); err == nil && info.Mode().IsRegular() {
		return os.Remove(src)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(src, dest); err != nil {
		return err
	}
	return fsyncDir(filepath.Dir(dest))
}

func (s *Store) DeleteUpload(id string) error {
	path, err := s.uploadPath(id)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *Store) QuarantineBlob(d oci.Digest) error {
	path, err := s.blobPath(d)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	trashPath := filepath.Join(s.root, "trash", base64.RawURLEncoding.EncodeToString(raw[:])+".deleted")
	if err := os.Rename(path, trashPath); err != nil {
		return err
	}
	return fsyncDir(filepath.Dir(path))
}

func (s *Store) PurgeTrash(olderThan time.Time) (int64, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "trash"))
	if err != nil {
		return 0, err
	}
	var removed int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".deleted") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !olderThan.IsZero() && info.ModTime().After(olderThan) {
			continue
		}
		if err := os.Remove(filepath.Join(s.root, "trash", entry.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

func (s *Store) PurgeTmp(olderThan time.Time) (int64, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "tmp"))
	if err != nil {
		return 0, err
	}
	var removed int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !olderThan.IsZero() && info.ModTime().After(olderThan) {
			continue
		}
		if err := os.Remove(filepath.Join(s.root, "tmp", entry.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

func (s *Store) TempBytes() (int64, error) {
	var total int64
	for _, dir := range []string{"tmp", "uploads", "trash"} {
		entries, err := os.ReadDir(filepath.Join(s.root, dir))
		if err != nil {
			return 0, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err == nil {
				total += info.Size()
			}
		}
	}
	return total, nil
}

func (s *Store) WalkBlobs(fn func(oci.Digest, os.FileInfo) error) error {
	root := filepath.Join(s.root, "cas")
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) != 4 || entry.Name() != parts[3] || !strings.HasPrefix(parts[3], parts[2]) {
			return nil
		}
		digest, err := oci.ParseDigest(parts[0] + ":" + entry.Name())
		if err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return fn(digest, info)
	})
}

func (s *Store) ReadBlob(d oci.Digest) ([]byte, error) {
	f, _, err := s.OpenBlob(d)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func fsyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
