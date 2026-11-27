package runtimeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"crproxy/internal/secretbox"
	"crproxy/internal/store"
)

const settingPrefix = "runtime."
const settingSuffix = ".v1"
const outboundPasswordAAD = "crproxy:runtime:outbound-proxy:v1"

type KeyProvider interface {
	CurrentVersion() int64
	Sealer(version int64) (*secretbox.Sealer, error)
}

type storedSection struct {
	Version   int64           `json:"version"`
	UpdatedAt time.Time       `json:"updatedAt"`
	UpdatedBy string          `json:"updatedBy"`
	Values    json.RawMessage `json:"values"`
}

type storedOutboundProxy struct {
	HTTPProxy          string `json:"httpProxy"`
	HTTPSProxy         string `json:"httpsProxy"`
	NoProxy            string `json:"noProxy"`
	Username           string `json:"username"`
	PasswordCiphertext []byte `json:"passwordCiphertext,omitempty"`
	PasswordKeyVersion int64  `json:"passwordKeyVersion,omitempty"`
}

type legacyStorageSettings struct {
	MaxBytes      int64   `json:"maxBytes"`
	CacheMaxBytes int64   `json:"cacheMaxBytes"`
	MinFreeBytes  int64   `json:"minFreeBytes"`
	HighWatermark float64 `json:"highWatermark"`
	LowWatermark  float64 `json:"lowWatermark"`
	OnFull        string  `json:"onFull"`
}

type Manager struct {
	mu       sync.Mutex
	store    *store.Store
	keys     KeyProvider
	base     Base
	sections map[string]storedSection
	active   atomic.Pointer[Config]
	apply    func(Config)
}

func New(ctx context.Context, metadata *store.Store, base Base, keys KeyProvider) (*Manager, error) {
	if metadata == nil {
		return nil, errors.New("runtime config store is required")
	}
	base = cloneBase(base)
	base.RegistryAuth.Challenge = strings.TrimSpace(base.RegistryAuth.Challenge)
	base.RegistryAuth.Service = strings.TrimSpace(base.RegistryAuth.Service)
	base.RegistryAuth.Realm = strings.TrimRight(strings.TrimSpace(base.RegistryAuth.Realm), "/")
	if err := normalizeRegistryAuth(&base.RegistryAuth); err != nil {
		return nil, err
	}
	base.Outbound.HTTPProxy = strings.TrimSpace(base.Outbound.HTTPProxy)
	base.Outbound.HTTPSProxy = strings.TrimSpace(base.Outbound.HTTPSProxy)
	base.Outbound.NoProxy = strings.TrimSpace(base.Outbound.NoProxy)
	base.Outbound.Username = strings.TrimSpace(base.Outbound.Username)
	manager := &Manager{store: metadata, keys: keys, base: base, sections: map[string]storedSection{}}
	if err := manager.load(ctx); err != nil {
		return nil, err
	}
	if err := manager.migrateLegacyStorage(ctx); err != nil {
		return nil, err
	}
	effective, err := manager.compose(manager.sections)
	if err != nil {
		return nil, err
	}
	manager.active.Store(&effective)
	return manager, nil
}

func (m *Manager) load(ctx context.Context) error {
	for _, section := range sectionNames() {
		raw, err := m.store.Setting(ctx, settingKey(section))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("load runtime config %s: %w", section, err)
		}
		var stored storedSection
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return fmt.Errorf("parse runtime config %s: %w", section, err)
		}
		if stored.Version <= 0 {
			return fmt.Errorf("parse runtime config %s: version must be positive", section)
		}
		m.sections[section] = stored
	}
	return nil
}

func (m *Manager) migrateLegacyStorage(ctx context.Context) error {
	if _, exists := m.sections[SectionStorage]; exists {
		return nil
	}
	raw, err := m.store.Setting(ctx, "storage.settings")
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var legacy legacyStorageSettings
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return nil
	}
	values := m.base.Storage
	values.MaxBytes = legacy.MaxBytes
	values.CacheMaxBytes = legacy.CacheMaxBytes
	values.MinFreeBytes = legacy.MinFreeBytes
	values.HighWatermark = legacy.HighWatermark
	values.LowWatermark = legacy.LowWatermark
	values.OnFull = legacy.OnFull
	if err := validateStorage(values); err != nil {
		return nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	stored := storedSection{Version: 1, UpdatedAt: now, UpdatedBy: "migration", Values: encoded}
	rawStored, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if err := m.store.SetSetting(ctx, settingKey(SectionStorage), string(rawStored)); err != nil {
		return fmt.Errorf("migrate storage settings: %w", err)
	}
	m.sections[SectionStorage] = stored
	if err := m.store.DeleteSetting(ctx, "storage.settings"); err != nil {
		return fmt.Errorf("remove legacy storage settings: %w", err)
	}
	return nil
}

func (m *Manager) SetApply(apply func(Config)) {
	m.mu.Lock()
	m.apply = apply
	current := m.active.Load()
	m.mu.Unlock()
	if apply != nil && current != nil {
		apply(cloneConfig(*current))
	}
}

func (m *Manager) Current() *Config {
	return m.active.Load()
}

func (m *Manager) View() View {
	return m.Current().View()
}

func (m *Manager) UpdateRegistryAuth(ctx context.Context, expectedVersion int64, actor string, value RegistryAuth) (View, error) {
	value.Challenge = strings.TrimSpace(value.Challenge)
	value.Service = strings.TrimSpace(value.Service)
	value.Realm = strings.TrimRight(strings.TrimSpace(value.Realm), "/")
	value.TokenTTL = strings.TrimSpace(value.TokenTTL)
	if value.Realm == "" {
		value.Realm = m.base.RegistryAuth.Realm
	}
	if err := normalizeRegistryAuth(&value); err != nil {
		return View{}, err
	}
	return m.save(ctx, SectionRegistryAuth, expectedVersion, actor, value, func(candidate *Config) {
		candidate.RegistryAuth = value
	})
}

func (m *Manager) UpdateWebSession(ctx context.Context, expectedVersion int64, actor string, value WebSession) (View, error) {
	value.SessionTTL = strings.TrimSpace(value.SessionTTL)
	value.IdleTimeout = strings.TrimSpace(value.IdleTimeout)
	return m.save(ctx, SectionWebSession, expectedVersion, actor, value, func(candidate *Config) {
		candidate.WebSession = value
	})
}

func (m *Manager) UpdateStorage(ctx context.Context, expectedVersion int64, actor string, value Storage) (View, error) {
	value.OnFull = strings.TrimSpace(value.OnFull)
	value.TmpTTL = strings.TrimSpace(value.TmpTTL)
	value.TrashTTL = strings.TrimSpace(value.TrashTTL)
	return m.save(ctx, SectionStorage, expectedVersion, actor, value, func(candidate *Config) {
		candidate.Storage = value
	})
}

func (m *Manager) UpdateUploads(ctx context.Context, expectedVersion int64, actor string, value Uploads) (View, error) {
	value.UploadsTTL = strings.TrimSpace(value.UploadsTTL)
	return m.save(ctx, SectionUploads, expectedVersion, actor, value, func(candidate *Config) {
		candidate.Uploads = value
	})
}

func (m *Manager) UpdateAudit(ctx context.Context, expectedVersion int64, actor string, value Audit) (View, error) {
	value.AuditRetention = strings.TrimSpace(value.AuditRetention)
	return m.save(ctx, SectionAudit, expectedVersion, actor, value, func(candidate *Config) {
		candidate.Audit = value
	})
}

func (m *Manager) UpdateProxy(ctx context.Context, expectedVersion int64, actor string, value Proxy) (View, error) {
	return m.save(ctx, SectionProxy, expectedVersion, actor, value, func(candidate *Config) {
		candidate.Proxy = value
	})
}

func (m *Manager) UpdateSecurity(ctx context.Context, expectedVersion int64, actor string, value Security) (View, error) {
	return m.save(ctx, SectionSecurity, expectedVersion, actor, value, func(candidate *Config) {
		candidate.Security = value
	})
}

func (m *Manager) UpdateOutboundProxy(ctx context.Context, expectedVersion int64, actor string, input OutboundProxyInput) (View, error) {
	if strings.TrimSpace(input.Username) == "" && input.Password != "" {
		return View{}, fmt.Errorf("%w: outbound_proxy.username is required when password is configured", ErrInvalid)
	}
	m.mu.Lock()
	current := m.active.Load()
	if current == nil {
		m.mu.Unlock()
		return View{}, errors.New("runtime configuration is not initialized")
	}
	if expectedVersion != current.Sections[SectionOutbound].Version {
		conflict := conflictFor(SectionOutbound, current)
		m.mu.Unlock()
		return View{}, conflict
	}
	password := current.Outbound.Password
	if strings.TrimSpace(input.Username) == "" {
		password = ""
	} else if input.ClearPassword {
		password = ""
	} else if input.Password != "" {
		password = input.Password
	}
	effective := OutboundProxy{
		HTTPProxy:  strings.TrimSpace(input.HTTPProxy),
		HTTPSProxy: strings.TrimSpace(input.HTTPSProxy),
		NoProxy:    strings.TrimSpace(input.NoProxy),
		Username:   strings.TrimSpace(input.Username),
		Password:   password,
	}
	if err := validateOutboundProxy(effective); err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	stored := storedOutboundProxy{
		HTTPProxy:  effective.HTTPProxy,
		HTTPSProxy: effective.HTTPSProxy,
		NoProxy:    effective.NoProxy,
		Username:   effective.Username,
	}
	if effective.Password != "" {
		if m.keys == nil {
			m.mu.Unlock()
			return View{}, fmt.Errorf("%w: master key is unavailable for outbound proxy password", ErrInvalid)
		}
		sealer, err := m.keys.Sealer(m.keys.CurrentVersion())
		if err != nil {
			m.mu.Unlock()
			return View{}, err
		}
		ciphertext, err := sealer.Seal([]byte(effective.Password), outboundPasswordAAD)
		if err != nil {
			m.mu.Unlock()
			return View{}, err
		}
		stored.PasswordCiphertext = ciphertext
		stored.PasswordKeyVersion = m.keys.CurrentVersion()
	}
	m.mu.Unlock()
	return m.save(ctx, SectionOutbound, expectedVersion, actor, stored, func(candidate *Config) {
		candidate.Outbound = effective
	})
}

func (m *Manager) ResetSection(ctx context.Context, section string, expectedVersion int64, actor string) (View, error) {
	if !validSection(section) {
		return View{}, ErrUnknownSection
	}
	m.mu.Lock()
	current := m.active.Load()
	if current == nil {
		m.mu.Unlock()
		return View{}, errors.New("runtime configuration is not initialized")
	}
	if expectedVersion != current.Sections[section].Version {
		conflict := conflictFor(section, current)
		m.mu.Unlock()
		return View{}, conflict
	}
	nextSections := cloneSections(m.sections)
	delete(nextSections, section)
	candidate, err := m.compose(nextSections)
	if err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	if err := m.store.DeleteSetting(ctx, settingKey(section)); err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	m.sections = nextSections
	m.active.Store(&candidate)
	apply := m.apply
	m.mu.Unlock()
	if apply != nil {
		apply(cloneConfig(candidate))
	}
	return candidate.View(), nil
}

func (m *Manager) save(ctx context.Context, section string, expectedVersion int64, actor string, storedValue any, mutate func(*Config)) (View, error) {
	if !validSection(section) {
		return View{}, ErrUnknownSection
	}
	m.mu.Lock()
	current := m.active.Load()
	if current == nil {
		m.mu.Unlock()
		return View{}, errors.New("runtime configuration is not initialized")
	}
	if expectedVersion != current.Sections[section].Version {
		conflict := conflictFor(section, current)
		m.mu.Unlock()
		return View{}, conflict
	}
	candidate := cloneConfig(*current)
	mutate(&candidate)
	now := time.Now().UTC()
	candidate.Sections[section] = SectionMeta{
		Version:   current.Sections[section].Version + 1,
		UpdatedAt: &now,
		UpdatedBy: actor,
		Source:    "runtime",
	}
	if err := validateConfig(candidate); err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	values, err := json.Marshal(storedValue)
	if err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	stored := storedSection{
		Version:   candidate.Sections[section].Version,
		UpdatedAt: now,
		UpdatedBy: actor,
		Values:    values,
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	if err := m.store.SetSetting(ctx, settingKey(section), string(raw)); err != nil {
		m.mu.Unlock()
		return View{}, err
	}
	nextSections := cloneSections(m.sections)
	nextSections[section] = stored
	m.sections = nextSections
	m.active.Store(&candidate)
	apply := m.apply
	m.mu.Unlock()
	if apply != nil {
		apply(cloneConfig(candidate))
	}
	return candidate.View(), nil
}

func (m *Manager) compose(sections map[string]storedSection) (Config, error) {
	cfg := Config{
		RegistryAuth: m.base.RegistryAuth,
		WebSession:   m.base.WebSession,
		Storage:      m.base.Storage,
		Uploads:      m.base.Uploads,
		Audit:        m.base.Audit,
		Proxy:        m.base.Proxy,
		Outbound:     m.base.Outbound,
		Security:     m.base.Security,
		Startup:      cloneStartup(m.base.Startup),
		Sections:     make(map[string]SectionMeta, len(sectionNames())),
	}
	for _, section := range sectionNames() {
		cfg.Sections[section] = SectionMeta{Source: "startup"}
	}
	for section, stored := range sections {
		meta := SectionMeta{
			Version:   stored.Version,
			UpdatedAt: timePointer(stored.UpdatedAt),
			UpdatedBy: stored.UpdatedBy,
			Source:    "runtime",
		}
		if err := applyStoredSection(&cfg, section, stored.Values, m.keys); err != nil {
			return Config{}, fmt.Errorf("load runtime config %s: %w", section, err)
		}
		cfg.Sections[section] = meta
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyStoredSection(cfg *Config, section string, raw json.RawMessage, keys KeyProvider) error {
	switch section {
	case SectionRegistryAuth:
		if err := json.Unmarshal(raw, &cfg.RegistryAuth); err != nil {
			return err
		}
		return normalizeRegistryAuth(&cfg.RegistryAuth)
	case SectionWebSession:
		return json.Unmarshal(raw, &cfg.WebSession)
	case SectionStorage:
		return json.Unmarshal(raw, &cfg.Storage)
	case SectionUploads:
		return json.Unmarshal(raw, &cfg.Uploads)
	case SectionAudit:
		return json.Unmarshal(raw, &cfg.Audit)
	case SectionProxy:
		return json.Unmarshal(raw, &cfg.Proxy)
	case SectionSecurity:
		return json.Unmarshal(raw, &cfg.Security)
	case SectionOutbound:
		var stored storedOutboundProxy
		if err := json.Unmarshal(raw, &stored); err != nil {
			return err
		}
		cfg.Outbound = OutboundProxy{
			HTTPProxy:  stored.HTTPProxy,
			HTTPSProxy: stored.HTTPSProxy,
			NoProxy:    stored.NoProxy,
			Username:   stored.Username,
		}
		if len(stored.PasswordCiphertext) > 0 {
			if keys == nil {
				return errors.New("outbound proxy password exists but master key is unavailable")
			}
			if stored.PasswordKeyVersion <= 0 {
				return errors.New("outbound proxy password key version is invalid")
			}
			sealer, err := keys.Sealer(stored.PasswordKeyVersion)
			if err != nil {
				return err
			}
			plaintext, err := sealer.Open(stored.PasswordCiphertext, outboundPasswordAAD)
			if err != nil {
				return err
			}
			cfg.Outbound.Password = string(plaintext)
		}
		return nil
	default:
		return ErrUnknownSection
	}
}

func settingKey(section string) string {
	return settingPrefix + section + settingSuffix
}

func sectionNames() []string {
	return []string{
		SectionRegistryAuth,
		SectionWebSession,
		SectionStorage,
		SectionUploads,
		SectionAudit,
		SectionProxy,
		SectionOutbound,
		SectionSecurity,
	}
}

func validSection(section string) bool {
	for _, candidate := range sectionNames() {
		if candidate == section {
			return true
		}
	}
	return false
}

func conflictFor(section string, current *Config) *ConflictError {
	meta := current.Sections[section]
	return &ConflictError{Section: section, CurrentVersion: meta.Version, UpdatedAt: meta.UpdatedAt, UpdatedBy: meta.UpdatedBy}
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func cloneSections(input map[string]storedSection) map[string]storedSection {
	output := make(map[string]storedSection, len(input))
	for key, value := range input {
		value.Values = append(json.RawMessage(nil), value.Values...)
		output[key] = value
	}
	return output
}

func cloneBase(input Base) Base {
	input.RegistryAuth.AnonymousActions = cloneStrings(input.RegistryAuth.AnonymousActions)
	input.Startup.TrustedProxies = cloneStrings(input.Startup.TrustedProxies)
	return input
}

func cloneStartup(input Startup) Startup {
	input.TrustedProxies = cloneStrings(input.TrustedProxies)
	return input
}

func cloneStrings(input []string) []string {
	output := make([]string, len(input))
	copy(output, input)
	return output
}

func cloneConfig(input Config) Config {
	input.RegistryAuth.AnonymousActions = cloneStrings(input.RegistryAuth.AnonymousActions)
	input.Startup = cloneStartup(input.Startup)
	input.Sections = cloneMeta(input.Sections)
	return input
}
