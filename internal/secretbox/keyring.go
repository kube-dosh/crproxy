package secretbox

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const keyringFormat = "crproxy-master-keyring-v1"

type diskKeyring struct {
	Format  string            `json:"format"`
	Version int64             `json:"version"`
	Keys    map[string]string `json:"keys"`
}

type Keyring struct {
	mu      sync.RWMutex
	path    string
	current int64
	keys    map[int64]KeyVersion
	sealers map[int64]*Sealer
}

type KeyVersion struct {
	Version int64
	Key     []byte
}

func LoadOrCreateKeyring(path string) (*Keyring, error) {
	if path == "" {
		return nil, errors.New("master key path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, KeySize)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		keyring := &Keyring{path: path, current: 1, keys: map[int64]KeyVersion{1: {Version: 1, Key: key}}, sealers: map[int64]*Sealer{}}
		if err := keyring.save(); err != nil {
			return nil, err
		}
		return keyring, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == KeySize {
		legacyPath := path + ".legacy"
		if _, statErr := os.Stat(legacyPath); errors.Is(statErr, os.ErrNotExist) {
			if err := os.WriteFile(legacyPath, raw, 0o600); err != nil {
				return nil, fmt.Errorf("preserve legacy master key: %w", err)
			}
		}
		keyring := &Keyring{path: path, current: 1, keys: map[int64]KeyVersion{1: {Version: 1, Key: append([]byte(nil), raw...)}}, sealers: map[int64]*Sealer{}}
		if err := keyring.save(); err != nil {
			return nil, err
		}
		return keyring, nil
	}
	var disk diskKeyring
	if err := json.Unmarshal(raw, &disk); err != nil {
		return nil, fmt.Errorf("parse master keyring: %w", err)
	}
	if disk.Format != keyringFormat {
		return nil, fmt.Errorf("unsupported master keyring format %q", disk.Format)
	}
	if disk.Version <= 0 {
		return nil, errors.New("master keyring version must be positive")
	}
	keyring := &Keyring{path: path, current: disk.Version, keys: map[int64]KeyVersion{}, sealers: map[int64]*Sealer{}}
	for versionText, encoded := range disk.Keys {
		version, err := strconv.ParseInt(versionText, 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid master key version %q", versionText)
		}
		key, err := base64.RawStdEncoding.DecodeString(encoded)
		if err != nil || len(key) != KeySize {
			return nil, fmt.Errorf("master key version %d is invalid", version)
		}
		keyring.keys[version] = KeyVersion{Version: version, Key: key}
		if _, err := keyring.sealerFor(version); err != nil {
			return nil, err
		}
	}
	if _, ok := keyring.keys[keyring.current]; !ok {
		return nil, fmt.Errorf("current master key version %d is missing", keyring.current)
	}
	return keyring, nil
}

func (k *Keyring) CurrentVersion() int64 {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.current
}

func (k *Keyring) Sealer(version int64) (*Sealer, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.sealerFor(version)
}

func (k *Keyring) Path() string {
	return k.path
}

func (k *Keyring) GenerateVersion() (int64, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	next := k.current + 1
	for {
		if _, exists := k.keys[next]; !exists {
			break
		}
		next++
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return 0, err
	}
	sealer, err := New(key)
	if err != nil {
		return 0, err
	}
	k.keys[next] = KeyVersion{Version: next, Key: key}
	k.sealers[next] = sealer
	previous := k.current
	k.current = next
	if err := k.saveLocked(); err != nil {
		delete(k.keys, next)
		delete(k.sealers, next)
		k.current = previous
		return 0, err
	}
	return next, nil
}

func (k *Keyring) Versions() []int64 {
	k.mu.RLock()
	defer k.mu.RUnlock()
	versions := make([]int64, 0, len(k.keys))
	for version := range k.keys {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	return versions
}

func (k *Keyring) sealerFor(version int64) (*Sealer, error) {
	if sealer := k.sealers[version]; sealer != nil {
		return sealer, nil
	}
	entry, ok := k.keys[version]
	if !ok {
		return nil, fmt.Errorf("master key version %d is not available", version)
	}
	sealer, err := New(entry.Key)
	if err != nil {
		return nil, err
	}
	k.sealers[version] = sealer
	return sealer, nil
}

func (k *Keyring) save() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.saveLocked()
}

func (k *Keyring) saveLocked() error {
	disk := diskKeyring{Format: keyringFormat, Version: k.current, Keys: make(map[string]string, len(k.keys))}
	for version, entry := range k.keys {
		disk.Keys[strconv.FormatInt(version, 10)] = base64.RawStdEncoding.EncodeToString(entry.Key)
	}
	raw, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(k.path), 0o750); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(k.path), ".master-keyring-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
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
	if err := os.Rename(tempName, k.path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(k.path))
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func (k *Keyring) String() string {
	versions := k.Versions()
	parts := make([]string, 0, len(versions))
	for _, version := range versions {
		parts = append(parts, strconv.FormatInt(version, 10))
	}
	return fmt.Sprintf("keyring(current=%d, versions=%s)", k.CurrentVersion(), strings.Join(parts, ","))
}
