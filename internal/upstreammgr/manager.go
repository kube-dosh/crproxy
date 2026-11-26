package upstreammgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"crproxy/internal/netutil"
	"crproxy/internal/oci"
	"crproxy/internal/proxy"
	"crproxy/internal/secretbox"
	"crproxy/internal/store"
)

var routePrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var ErrInvalid = errors.New("invalid upstream configuration")

type Credentials struct {
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	BearerToken string `json:"bearerToken,omitempty"`
}

func (c Credentials) Empty() bool {
	return c.Username == "" && c.Password == "" && c.BearerToken == ""
}

type UpsertInput struct {
	ID                 string
	Name               string
	Endpoint           string
	RouteHost          string
	RoutePrefix        string
	RepositoryPrefix   string
	AuthType           string
	CAPEM              string
	InsecureSkipVerify bool
	TagTTLSeconds      int64
	RequestTimeoutSecs int64
	MaxBlobBytes       *int64
	Enabled            bool
	Credentials        Credentials
	ReplaceCredentials bool
}

type Manager struct {
	mu     sync.Mutex
	store  *store.Store
	proxy  *proxy.Handler
	keys   KeyProvider
	logger *slog.Logger
}

type KeyProvider interface {
	CurrentVersion() int64
	Sealer(version int64) (*secretbox.Sealer, error)
}

type staticKeyProvider struct {
	sealer  *secretbox.Sealer
	version int64
}

func (p staticKeyProvider) CurrentVersion() int64 { return p.version }
func (p staticKeyProvider) Sealer(version int64) (*secretbox.Sealer, error) {
	if version != p.version {
		return nil, fmt.Errorf("upstream credentials use unsupported master key version %d", version)
	}
	return p.sealer, nil
}

func New(metadata *store.Store, proxyHandler *proxy.Handler, sealer *secretbox.Sealer, keyVersion int64, logger *slog.Logger) (*Manager, error) {
	if sealer == nil {
		return nil, errors.New("upstream manager sealer is required")
	}
	return NewWithKeyProvider(metadata, proxyHandler, staticKeyProvider{sealer: sealer, version: keyVersion}, logger)
}

func NewWithKeyProvider(metadata *store.Store, proxyHandler *proxy.Handler, keys KeyProvider, logger *slog.Logger) (*Manager, error) {
	if metadata == nil || proxyHandler == nil || keys == nil {
		return nil, errors.New("upstream manager dependencies are required")
	}
	if keys.CurrentVersion() <= 0 {
		return nil, errors.New("upstream master key version must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{store: metadata, proxy: proxyHandler, keys: keys, logger: logger}, nil
}

func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloadLocked(ctx)
}

func (m *Manager) reloadLocked(ctx context.Context) error {
	records, err := m.store.ListUpstreams(ctx)
	if err != nil {
		return err
	}
	upstreams := make([]*proxy.Upstream, 0, len(records))
	for _, record := range records {
		if !record.Enabled {
			continue
		}
		upstream, err := m.build(record)
		if err != nil {
			return fmt.Errorf("load upstream %s: %w", record.ID, err)
		}
		upstreams = append(upstreams, upstream)
	}
	return m.proxy.ReplaceUpstreams(upstreams)
}

func (m *Manager) Upsert(ctx context.Context, input UpsertInput) (store.Upstream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.upsertLocked(ctx, input, false)
}

func (m *Manager) Create(ctx context.Context, input UpsertInput) (store.Upstream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.upsertLocked(ctx, input, true)
}

func (m *Manager) upsertLocked(ctx context.Context, input UpsertInput, create bool) (store.Upstream, error) {
	normalized, err := normalizeInput(input)
	if err != nil {
		return store.Upstream{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	existing, existingErr := m.store.UpstreamByID(ctx, normalized.ID)
	exists := existingErr == nil
	if existingErr != nil && !errors.Is(existingErr, store.ErrNotFound) {
		return store.Upstream{}, existingErr
	}
	if create && exists {
		return store.Upstream{}, fmt.Errorf("%w: upstream %s already exists", store.ErrConflict, normalized.ID)
	}
	replaceCredentials := normalized.ReplaceCredentials || !exists
	if !replaceCredentials {
		credentials, err := m.decrypt(existing)
		if err != nil {
			return store.Upstream{}, err
		}
		normalized.Credentials = credentials
	}
	if err := validateCredentials(normalized.AuthType, normalized.Credentials); err != nil {
		return store.Upstream{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	record := store.Upstream{
		ID:                 normalized.ID,
		Name:               normalized.Name,
		Endpoint:           normalized.Endpoint,
		RouteHost:          normalized.RouteHost,
		RoutePrefix:        normalized.RoutePrefix,
		RepositoryPrefix:   normalized.RepositoryPrefix,
		AuthType:           normalized.AuthType,
		CAPEM:              normalized.CAPEM,
		InsecureSkipVerify: normalized.InsecureSkipVerify,
		TagTTLSeconds:      normalized.TagTTLSeconds,
		RequestTimeoutSecs: normalized.RequestTimeoutSecs,
		MaxBlobBytes:       normalized.MaxBlobBytes,
		Enabled:            normalized.Enabled,
	}
	if _, err := m.upstreamFromRecord(record, normalized.Credentials); err != nil {
		return store.Upstream{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var ciphertext []byte
	nextVersion := int64(0)
	expectedVersion := int64(0)
	if exists {
		expectedVersion = existing.CredentialsVersion
	}
	if replaceCredentials {
		nextVersion = 1
		if exists {
			nextVersion = existing.CredentialsVersion + 1
		}
		ciphertext, err = m.sealCredentials(normalized.ID, nextVersion, normalized.Credentials)
		if err != nil {
			return store.Upstream{}, err
		}
	}
	storedVersion, err := m.store.UpsertUpstreamWithCredentials(ctx, record, ciphertext, m.keys.CurrentVersion(), replaceCredentials, expectedVersion)
	if err != nil {
		return store.Upstream{}, err
	}
	if replaceCredentials && storedVersion != nextVersion {
		return store.Upstream{}, fmt.Errorf("%w: upstream %s credentials changed concurrently", store.ErrConflict, normalized.ID)
	}
	if err := m.reloadLocked(ctx); err != nil {
		return store.Upstream{}, err
	}
	return m.store.UpstreamByID(ctx, normalized.ID)
}

func (m *Manager) Delete(ctx context.Context, upstreamID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.DeleteUpstream(ctx, upstreamID); err != nil {
		return err
	}
	return m.reloadLocked(ctx)
}

func (m *Manager) Get(ctx context.Context, upstreamID string) (store.Upstream, error) {
	return m.store.UpstreamByID(ctx, upstreamID)
}

func (m *Manager) List(ctx context.Context) ([]store.Upstream, error) {
	return m.store.ListUpstreams(ctx)
}

func (m *Manager) Test(ctx context.Context, upstreamID string) error {
	record, err := m.store.UpstreamByID(ctx, upstreamID)
	if err != nil {
		return err
	}
	upstream, err := m.build(record)
	if err != nil {
		_ = m.store.UpdateUpstreamHealth(ctx, upstreamID, "unhealthy", err.Error())
		return err
	}
	if err := upstream.Test(ctx); err != nil {
		_ = m.store.UpdateUpstreamHealth(ctx, upstreamID, "unhealthy", err.Error())
		return err
	}
	return m.store.UpdateUpstreamHealth(ctx, upstreamID, "healthy", "")
}

func (m *Manager) Credentials(ctx context.Context, upstreamID string) (Credentials, error) {
	record, err := m.store.UpstreamByID(ctx, upstreamID)
	if err != nil {
		return Credentials{}, err
	}
	return m.decrypt(record)
}

func (m *Manager) build(record store.Upstream) (*proxy.Upstream, error) {
	credentials, err := m.decrypt(record)
	if err != nil {
		return nil, err
	}
	return m.upstreamFromRecord(record, credentials)
}

func (m *Manager) upstreamFromRecord(record store.Upstream, credentials Credentials) (*proxy.Upstream, error) {
	return proxy.NewUpstream(proxy.UpstreamConfig{
		ID:                 record.ID,
		Endpoint:           record.Endpoint,
		RouteHost:          record.RouteHost,
		RoutePrefix:        record.RoutePrefix,
		RepositoryPrefix:   record.RepositoryPrefix,
		AuthType:           record.AuthType,
		Username:           credentials.Username,
		Password:           credentials.Password,
		BearerToken:        credentials.BearerToken,
		CAPEM:              []byte(record.CAPEM),
		InsecureSkipVerify: record.InsecureSkipVerify,
		TagTTL:             time.Duration(record.TagTTLSeconds) * time.Second,
		RequestTimeout:     time.Duration(record.RequestTimeoutSecs) * time.Second,
		MaxBlobBytes:       valueOrZero(record.MaxBlobBytes),
	})
}

func (m *Manager) sealCredentials(upstreamID string, version int64, credentials Credentials) ([]byte, error) {
	if credentials.Empty() {
		return nil, nil
	}
	plaintext, err := json.Marshal(credentials)
	if err != nil {
		return nil, err
	}
	defer clearBytes(plaintext)
	sealer, err := m.keys.Sealer(m.keys.CurrentVersion())
	if err != nil {
		return nil, err
	}
	return sealer.Seal(plaintext, credentialsAAD(upstreamID, version))
}

func (m *Manager) decrypt(record store.Upstream) (Credentials, error) {
	if len(record.Credentials) == 0 {
		return Credentials{}, nil
	}
	sealer, err := m.keys.Sealer(record.CredentialsKeyVer)
	if err != nil {
		return Credentials{}, err
	}
	plaintext, err := sealer.Open(record.Credentials, credentialsAAD(record.ID, record.CredentialsVersion))
	if err != nil {
		return Credentials{}, err
	}
	defer clearBytes(plaintext)
	var credentials Credentials
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return Credentials{}, errors.New("decrypted upstream credentials are invalid")
	}
	return credentials, nil
}

func normalizeInput(input UpsertInput) (UpsertInput, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		input.Name = input.ID
	}
	if input.ID == "" || !routePrefixPattern.MatchString(input.ID) || !routePrefixPattern.MatchString(input.Name) {
		return UpsertInput{}, errors.New("upstream id and name must use lowercase letters, numbers, underscore or hyphen")
	}
	if input.RoutePrefix == "" {
		input.RoutePrefix = input.ID
	}
	if !routePrefixPattern.MatchString(input.RoutePrefix) || input.RoutePrefix == "_catalog" {
		return UpsertInput{}, errors.New("invalid upstream route prefix")
	}
	input.RouteHost = strings.ToLower(strings.TrimSpace(input.RouteHost))
	if input.RouteHost != "" {
		if err := netutil.ValidateHostPort(input.RouteHost); err != nil {
			return UpsertInput{}, errors.New("invalid upstream route host")
		}
	}
	input.RepositoryPrefix = strings.Trim(input.RepositoryPrefix, "/")
	if input.RepositoryPrefix != "" {
		if err := oci.ValidateRepository(input.RepositoryPrefix); err != nil {
			return UpsertInput{}, errors.New("invalid upstream repository prefix")
		}
	}
	if input.AuthType == "" {
		input.AuthType = "anonymous"
	}
	switch input.AuthType {
	case "anonymous", "basic", "bearer", "challenge":
	default:
		return UpsertInput{}, errors.New("invalid upstream auth type")
	}
	parsed, err := url.Parse(strings.TrimRight(input.Endpoint, "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return UpsertInput{}, errors.New("upstream endpoint must be an HTTPS URL")
	}
	input.Endpoint = strings.TrimRight(input.Endpoint, "/")
	if input.TagTTLSeconds <= 0 {
		input.TagTTLSeconds = 60
	}
	if input.RequestTimeoutSecs <= 0 {
		input.RequestTimeoutSecs = 30
	}
	if input.MaxBlobBytes != nil && *input.MaxBlobBytes <= 0 {
		input.MaxBlobBytes = nil
	}
	return input, nil
}

func validateCredentials(authType string, credentials Credentials) error {
	switch authType {
	case "basic":
		if credentials.Username == "" || credentials.Password == "" {
			return errors.New("basic authentication requires username and password")
		}
	case "bearer":
		if credentials.BearerToken == "" {
			return errors.New("bearer authentication requires a token")
		}
	}
	return nil
}

func credentialsAAD(upstreamID string, version int64) string {
	return fmt.Sprintf("crproxy:upstream:%s:credentials:%d", upstreamID, version)
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
