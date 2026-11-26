// Package backup creates and restores consistent offline archives of a
// crproxy data directory. The server must not be running while these
// operations execute.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	manifestName = "crproxy-backup.json"
	format       = "crproxy-offline-backup-v1"
)

type Manifest struct {
	Format    string    `json:"format"`
	Version   string    `json:"version,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type Result struct {
	Output     string `json:"output"`
	SHA256     string `json:"sha256"`
	Files      int    `json:"files"`
	TotalBytes int64  `json:"totalBytes"`
}

func Create(root, output, version string) (Result, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Result{}, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return Result{}, err
	}
	if rel, err := filepath.Rel(root, output); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Result{}, errors.New("backup output must not be inside the data directory")
	}
	if _, err := os.Stat(root); err != nil {
		return Result{}, fmt.Errorf("read data directory: %w", err)
	}
	if _, err := os.Stat(filepath.Join(root, "meta", "crproxy.db-journal")); err == nil {
		return Result{}, errors.New("SQLite journal file is present; start crproxy once to recover the database before backing up")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
		return Result{}, err
	}
	temp, err := os.CreateTemp(filepath.Dir(output), ".crproxy-backup-*")
	if err != nil {
		return Result{}, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return Result{}, err
	}
	gzipWriter := gzip.NewWriter(temp)
	tarWriter := tar.NewWriter(gzipWriter)
	manifest := Manifest{Format: format, Version: version, CreatedAt: time.Now().UTC()}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err == nil {
		manifestBytes = append(manifestBytes, '\n')
		err = tarWriter.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifestBytes)), ModTime: manifest.CreatedAt, Typeflag: tar.TypeReg})
	}
	if err == nil {
		_, err = tarWriter.Write(manifestBytes)
	}
	if err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = temp.Close()
		return Result{}, err
	}
	result := Result{Output: output}
	err = walkData(root, func(name string, info os.FileInfo) error {
		if !includePath(name) {
			return nil
		}
		file, err := os.Open(filepath.Join(root, name))
		if err != nil {
			return err
		}
		defer file.Close()
		header := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime().UTC(), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		written, err := io.Copy(tarWriter, file)
		if err != nil {
			return err
		}
		if written != info.Size() {
			return fmt.Errorf("backup %s: short read", name)
		}
		result.Files++
		result.TotalBytes += written
		return nil
	})
	if err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = temp.Close()
		return Result{}, err
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		_ = temp.Close()
		return Result{}, err
	}
	if err := gzipWriter.Close(); err != nil {
		_ = temp.Close()
		return Result{}, err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return Result{}, err
	}
	if err := temp.Close(); err != nil {
		return Result{}, err
	}
	result.SHA256, err = hashFile(tempName)
	if err != nil {
		return Result{}, err
	}
	if err := os.Rename(tempName, output); err != nil {
		return Result{}, err
	}
	if err := writeChecksum(output, result.SHA256); err != nil {
		return Result{}, err
	}
	return result, nil
}

func Restore(input, root string, force bool) error {
	input, err := filepath.Abs(input)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := verifyChecksum(input); err != nil {
		return err
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".crproxy-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := extract(input, staging); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(staging, "meta", "crproxy.db")); err != nil {
		return fmt.Errorf("backup does not contain meta/crproxy.db: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	meaningful := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() != "LOCK" {
			meaningful = append(meaningful, entry)
		}
	}
	if len(meaningful) > 0 {
		if !force {
			return fmt.Errorf("restore target %s is not empty; use --force to preserve and replace it", root)
		}
		legacy := root + ".pre-restore-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(root, legacy); err != nil {
			return fmt.Errorf("preserve existing data directory: %w", err)
		}
		if err := os.Rename(staging, root); err != nil {
			_ = os.Rename(legacy, root)
			return fmt.Errorf("activate restored data directory: %w", err)
		}
		fmt.Printf("previous data directory preserved at %s\n", legacy)
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, "LOCK")); err == nil {
		if err := os.Remove(filepath.Join(root, "LOCK")); err != nil {
			return fmt.Errorf("remove restore lock marker: %w", err)
		}
	}
	if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staging, root); err != nil {
		return fmt.Errorf("activate restored data directory: %w", err)
	}
	return nil
}

func walkData(root string, visit func(name string, info os.FileInfo) error) error {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("data directory contains unsupported symlink %s", rel)
		}
		if info.IsDir() {
			return nil
		}
		if includePath(rel) {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, name := range files {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if err := visit(name, info); err != nil {
			return err
		}
	}
	return nil
}

func includePath(name string) bool {
	if strings.HasPrefix(name, "meta/") {
		return !strings.HasSuffix(name, "-journal") && !strings.HasSuffix(name, "-wal") && !strings.HasSuffix(name, "-shm")
	}
	return strings.HasPrefix(name, "cas/") || strings.HasPrefix(name, "uploads/")
}

func extract(input, target string) error {
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open backup gzip: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	manifestSeen := false
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read backup archive: %w", err)
		}
		name := filepath.ToSlash(header.Name)
		if name == manifestName {
			if header.Size > 1<<20 {
				return errors.New("backup manifest is too large")
			}
			raw, err := io.ReadAll(io.LimitReader(tarReader, 1<<20))
			if err != nil {
				return err
			}
			var manifest Manifest
			if err := json.Unmarshal(raw, &manifest); err != nil || manifest.Format != format {
				return errors.New("backup manifest is invalid or unsupported")
			}
			manifestSeen = true
			continue
		}
		if !includePath(name) {
			return fmt.Errorf("backup contains unsupported path %q", name)
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("backup contains unsafe path %q", name)
		}
		destination := filepath.Join(target, clean)
		relative, err := filepath.Rel(target, destination)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("backup contains unsafe path %q", name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("backup contains unsupported entry type for %q", name)
		}
		if header.Size < 0 {
			return fmt.Errorf("backup contains invalid size for %q", name)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
			return err
		}
		mode := os.FileMode(header.Mode).Perm()
		if mode == 0 {
			mode = 0o640
		}
		if strings.HasPrefix(name, "meta/") && (strings.HasSuffix(name, ".key") || strings.HasSuffix(name, ".secret") || strings.Contains(name, "master-keyring")) {
			mode = 0o600
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(output, tarReader); err != nil {
			_ = output.Close()
			return err
		}
		if err := output.Sync(); err != nil {
			_ = output.Close()
			return err
		}
		if err := output.Close(); err != nil {
			return err
		}
	}
	if !manifestSeen {
		return errors.New("backup manifest is missing")
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeChecksum(path, checksum string) error {
	content := fmt.Sprintf("%s  %s\n", checksum, filepath.Base(path))
	temp, err := os.CreateTemp(filepath.Dir(path), ".crproxy-checksum-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.WriteString(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path+".sha256")
}

func verifyChecksum(path string) error {
	raw, err := os.ReadFile(path + ".sha256")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return errors.New("backup checksum file is invalid")
	}
	actual, err := hashFile(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(fields[0], actual) {
		return errors.New("backup SHA-256 checksum does not match")
	}
	return nil
}
