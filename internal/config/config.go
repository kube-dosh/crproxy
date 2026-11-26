package config

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"crproxy/internal/netutil"
	"crproxy/internal/oci"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig     `yaml:"server"`
	Auth      AuthConfig       `yaml:"auth"`
	Storage   StorageConfig    `yaml:"storage"`
	Proxy     ProxyConfig      `yaml:"proxy"`
	Upstreams []UpstreamConfig `yaml:"upstreams_seed"`
}

type ProxyConfig struct {
	Disabled   bool   `yaml:"disabled"`
	PathPrefix string `yaml:"path_prefix"`
}

type UpstreamConfig struct {
	ID                 string `yaml:"id"`
	Name               string `yaml:"name"`
	Endpoint           string `yaml:"endpoint"`
	RouteHost          string `yaml:"route_host"`
	RoutePrefix        string `yaml:"route_prefix"`
	RepositoryPrefix   string `yaml:"repository_prefix"`
	AuthType           string `yaml:"auth_type"`
	Username           string `yaml:"username"`
	Password           string `yaml:"password"`
	PasswordEnv        string `yaml:"password_env"`
	BearerToken        string `yaml:"bearer_token"`
	BearerTokenEnv     string `yaml:"bearer_token_env"`
	CAPEM              string `yaml:"ca_pem"`
	CAFile             string `yaml:"ca_file"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
	TagTTL             string `yaml:"tag_ttl"`
	RequestTimeout     string `yaml:"request_timeout"`
	MaxBlobBytes       int64  `yaml:"max_blob_bytes"`
	Disabled           bool   `yaml:"disabled"`
}

type ServerConfig struct {
	Listen         string    `yaml:"listen"`
	PublicHost     string    `yaml:"public_host"`
	PublicScheme   string    `yaml:"public_scheme"`
	TLS            TLSConfig `yaml:"tls"`
	TrustedProxies []string  `yaml:"trusted_proxies"`
}

type TLSConfig struct {
	Mode          string `yaml:"mode"`
	CertFile      string `yaml:"cert_file"`
	KeyFile       string `yaml:"key_file"`
	MinVersion    string `yaml:"min_version"`
	ReloadSeconds int    `yaml:"reload_seconds"`
	HSTS          bool   `yaml:"hsts"`
}

type AuthConfig struct {
	Registry       RegistryAuthConfig   `yaml:"registry"`
	Web            WebAuthConfig        `yaml:"web"`
	BootstrapAdmin BootstrapAdminConfig `yaml:"bootstrap_admin"`
	JWTSecretFile  string               `yaml:"jwt_secret_file"`
	MasterKeyFile  string               `yaml:"master_key_file"`
}

type WebAuthConfig struct {
	SessionTTL  string `yaml:"session_ttl"`
	IdleTimeout string `yaml:"idle_timeout"`
}

type RegistryAuthConfig struct {
	Challenge      string `yaml:"challenge"`
	Service        string `yaml:"service"`
	Realm          string `yaml:"realm"`
	AllowAnonymous bool   `yaml:"allow_anonymous"`
	TokenTTL       string `yaml:"token_ttl"`
}

type BootstrapAdminConfig struct {
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	PasswordEnv string `yaml:"password_env"`
}

type StorageConfig struct {
	Root                string  `yaml:"root"`
	MaxBlobBytes        int64   `yaml:"max_blob_bytes"`
	MaxManifestBytes    int64   `yaml:"max_manifest_bytes"`
	UploadTTL           string  `yaml:"uploads_ttl"`
	MaxBytes            int64   `yaml:"max_bytes"`
	CacheMaxBytes       int64   `yaml:"cache_max_bytes"`
	MinFreeBytes        int64   `yaml:"min_free_bytes"`
	HighWatermark       float64 `yaml:"high_watermark"`
	LowWatermark        float64 `yaml:"low_watermark"`
	OnFull              string  `yaml:"on_full"`
	DownloadConcurrency int     `yaml:"download_concurrency"`
	TmpTTL              string  `yaml:"tmp_ttl"`
	TrashTTL            string  `yaml:"trash_ttl"`
	AuditRetention      string  `yaml:"audit_retention"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{
			Listen:     ":5000",
			PublicHost: "localhost:5000",
			TLS: TLSConfig{
				MinVersion:    "1.2",
				ReloadSeconds: 60,
			},
		},
		Auth: AuthConfig{
			Registry: RegistryAuthConfig{
				Challenge: "bearer",
				Service:   "crproxy",
				TokenTTL:  "5m",
			},
			Web: WebAuthConfig{
				SessionTTL:  "12h",
				IdleTimeout: "30m",
			},
			BootstrapAdmin: BootstrapAdminConfig{
				Username:    "admin",
				PasswordEnv: "CRPROXY_ADMIN_PASSWORD",
			},
		},
		Storage: StorageConfig{
			Root:                "./data",
			MaxBlobBytes:        20 << 30,
			MaxManifestBytes:    4 << 20,
			UploadTTL:           "24h",
			MaxBytes:            100 << 30,
			CacheMaxBytes:       60 << 30,
			MinFreeBytes:        5 << 30,
			HighWatermark:       0.85,
			LowWatermark:        0.70,
			OnFull:              "bypass",
			DownloadConcurrency: 8,
			TmpTTL:              "1h",
			TrashTTL:            "24h",
			AuditRetention:      "2160h",
		},
		Proxy: ProxyConfig{PathPrefix: "r"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil && err != io.EOF {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return Config{}, fmt.Errorf("parse config: multiple YAML documents are not supported")
			}
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	if cfg.Server.PublicHost == "" {
		if strings.HasPrefix(cfg.Server.Listen, ":") {
			cfg.Server.PublicHost = "localhost" + cfg.Server.Listen
		} else {
			cfg.Server.PublicHost = cfg.Server.Listen
		}
	}
	cfg.Server.PublicHost = strings.ToLower(strings.TrimSpace(cfg.Server.PublicHost))
	if err := netutil.ValidateHostPort(cfg.Server.PublicHost); err != nil {
		return Config{}, fmt.Errorf("invalid server.public_host %q: %w", cfg.Server.PublicHost, err)
	}
	switch cfg.Server.TLS.Mode {
	case "", "file":
	default:
		return Config{}, fmt.Errorf("unsupported server.tls.mode %q", cfg.Server.TLS.Mode)
	}
	if cfg.Server.TLS.Mode != "file" && (cfg.Server.TLS.CertFile != "" || cfg.Server.TLS.KeyFile != "") {
		return Config{}, fmt.Errorf("tls cert_file and key_file require server.tls.mode=file")
	}
	if cfg.Server.TLS.Mode == "file" && (cfg.Server.TLS.CertFile == "" || cfg.Server.TLS.KeyFile == "") {
		return Config{}, fmt.Errorf("tls cert_file and key_file are required when mode=file")
	}
	cfg.Server.PublicScheme = strings.ToLower(strings.TrimSpace(cfg.Server.PublicScheme))
	switch cfg.Server.PublicScheme {
	case "":
		if cfg.Server.TLS.Mode == "file" {
			cfg.Server.PublicScheme = "https"
		} else {
			cfg.Server.PublicScheme = "http"
		}
	case "http", "https":
	default:
		return Config{}, fmt.Errorf("invalid server.public_scheme %q", cfg.Server.PublicScheme)
	}
	if cfg.Server.TLS.HSTS && cfg.Server.PublicScheme != "https" {
		return Config{}, fmt.Errorf("server.tls.hsts requires public_scheme=https")
	}
	for index, raw := range cfg.Server.TrustedProxies {
		prefix, err := parseTrustedProxy(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid server.trusted_proxies[%d]: %w", index, err)
		}
		cfg.Server.TrustedProxies[index] = prefix
	}
	if cfg.Auth.Registry.Service == "" {
		cfg.Auth.Registry.Service = "crproxy"
	}
	if cfg.Auth.Registry.Challenge == "" {
		cfg.Auth.Registry.Challenge = "bearer"
	}
	if cfg.Auth.Registry.Challenge != "bearer" && cfg.Auth.Registry.Challenge != "basic" {
		return Config{}, fmt.Errorf("invalid auth.registry.challenge %q", cfg.Auth.Registry.Challenge)
	}
	if cfg.Auth.Registry.Realm != "" {
		cfg.Auth.Registry.Realm = strings.TrimRight(strings.TrimSpace(cfg.Auth.Registry.Realm), "/")
		realm, err := url.Parse(cfg.Auth.Registry.Realm)
		if err != nil || realm.Host == "" || (realm.Scheme != "https" && realm.Scheme != "http") || realm.User != nil || realm.Fragment != "" {
			return Config{}, fmt.Errorf("invalid auth.registry.realm %q", cfg.Auth.Registry.Realm)
		}
		if cfg.Server.PublicScheme == "https" && realm.Scheme != "https" {
			return Config{}, fmt.Errorf("auth.registry.realm must use HTTPS when public_scheme=https")
		}
	}
	if cfg.Auth.BootstrapAdmin.Username == "" {
		cfg.Auth.BootstrapAdmin.Username = "admin"
	}
	if cfg.Auth.BootstrapAdmin.Password == "" && cfg.Auth.BootstrapAdmin.PasswordEnv != "" {
		cfg.Auth.BootstrapAdmin.Password = os.Getenv(cfg.Auth.BootstrapAdmin.PasswordEnv)
	}
	if cfg.Storage.MaxBlobBytes < 0 {
		return Config{}, fmt.Errorf("storage.max_blob_bytes cannot be negative")
	}
	if cfg.Storage.MaxBlobBytes == 0 {
		cfg.Storage.MaxBlobBytes = 20 << 30
	}
	if cfg.Storage.MaxManifestBytes < 0 {
		return Config{}, fmt.Errorf("storage.max_manifest_bytes cannot be negative")
	}
	if cfg.Storage.MaxManifestBytes == 0 {
		cfg.Storage.MaxManifestBytes = 4 << 20
	}
	if cfg.Auth.Registry.TokenTTL == "" {
		cfg.Auth.Registry.TokenTTL = "5m"
	}
	if cfg.Auth.Web.SessionTTL == "" {
		cfg.Auth.Web.SessionTTL = "12h"
	}
	if cfg.Auth.Web.IdleTimeout == "" {
		cfg.Auth.Web.IdleTimeout = "30m"
	}
	if _, err := cfg.WebSessionTTL(); err != nil {
		return Config{}, err
	}
	if _, err := cfg.WebIdleTimeout(); err != nil {
		return Config{}, err
	}
	if cfg.Storage.UploadTTL == "" {
		cfg.Storage.UploadTTL = "24h"
	}
	if cfg.Storage.MaxBytes < 0 {
		return Config{}, fmt.Errorf("storage.max_bytes cannot be negative")
	}
	if cfg.Storage.MaxBytes == 0 {
		cfg.Storage.MaxBytes = 100 << 30
	}
	if cfg.Storage.CacheMaxBytes < 0 {
		return Config{}, fmt.Errorf("storage.cache_max_bytes cannot be negative")
	}
	if cfg.Storage.CacheMaxBytes == 0 {
		cfg.Storage.CacheMaxBytes = cfg.Storage.MaxBytes
	}
	if cfg.Storage.CacheMaxBytes > cfg.Storage.MaxBytes {
		return Config{}, fmt.Errorf("storage.cache_max_bytes cannot exceed storage.max_bytes")
	}
	if cfg.Storage.MinFreeBytes < 0 {
		return Config{}, fmt.Errorf("storage.min_free_bytes cannot be negative")
	}
	if cfg.Storage.HighWatermark < 0 || cfg.Storage.HighWatermark > 1 {
		return Config{}, fmt.Errorf("storage.high_watermark must be between 0 and 1")
	}
	if cfg.Storage.HighWatermark == 0 {
		cfg.Storage.HighWatermark = 0.85
	}
	if cfg.Storage.LowWatermark < 0 || cfg.Storage.LowWatermark >= cfg.Storage.HighWatermark {
		return Config{}, fmt.Errorf("storage.low_watermark must be non-negative and lower than high_watermark")
	}
	if cfg.Storage.LowWatermark == 0 {
		cfg.Storage.LowWatermark = cfg.Storage.HighWatermark * 0.8
	}
	switch cfg.Storage.OnFull {
	case "":
		cfg.Storage.OnFull = "bypass"
	case "bypass", "reject":
	default:
		return Config{}, fmt.Errorf("invalid storage.on_full %q", cfg.Storage.OnFull)
	}
	if cfg.Storage.DownloadConcurrency < 0 {
		return Config{}, fmt.Errorf("storage.download_concurrency cannot be negative")
	}
	if cfg.Storage.DownloadConcurrency == 0 {
		cfg.Storage.DownloadConcurrency = 8
	}
	if cfg.Storage.TmpTTL == "" {
		cfg.Storage.TmpTTL = "1h"
	}
	if cfg.Storage.TrashTTL == "" {
		cfg.Storage.TrashTTL = "24h"
	}
	if cfg.Storage.AuditRetention == "" {
		cfg.Storage.AuditRetention = "2160h"
	}
	if cfg.Server.TLS.MinVersion == "" {
		cfg.Server.TLS.MinVersion = "1.2"
	}
	if cfg.Server.TLS.MinVersion != "1.2" && cfg.Server.TLS.MinVersion != "1.3" {
		return Config{}, fmt.Errorf("invalid server.tls.min_version %q", cfg.Server.TLS.MinVersion)
	}
	if cfg.Server.TLS.ReloadSeconds < 0 {
		return Config{}, fmt.Errorf("server.tls.reload_seconds cannot be negative")
	}
	if _, err := cfg.TokenTTL(); err != nil {
		return Config{}, err
	}
	if _, err := cfg.UploadTTL(); err != nil {
		return Config{}, err
	}
	if _, err := cfg.Duration("storage.tmp_ttl", cfg.Storage.TmpTTL); err != nil {
		return Config{}, err
	}
	if _, err := cfg.Duration("storage.trash_ttl", cfg.Storage.TrashTTL); err != nil {
		return Config{}, err
	}
	if _, err := cfg.Duration("storage.audit_retention", cfg.Storage.AuditRetention); err != nil {
		return Config{}, err
	}
	if err := normalizeProxy(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

var proxyNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func normalizeProxy(cfg *Config) error {
	if cfg.Proxy.PathPrefix == "" {
		cfg.Proxy.PathPrefix = "r"
	}
	if !proxyNamePattern.MatchString(cfg.Proxy.PathPrefix) || cfg.Proxy.PathPrefix == "_catalog" {
		return fmt.Errorf("invalid proxy.path_prefix %q", cfg.Proxy.PathPrefix)
	}
	ids := map[string]bool{}
	hosts := map[string]bool{}
	prefixes := map[string]bool{}
	for i := range cfg.Upstreams {
		up := &cfg.Upstreams[i]
		if up.Name == "" {
			up.Name = up.ID
		}
		if up.ID == "" {
			up.ID = up.Name
		}
		if !proxyNamePattern.MatchString(up.ID) || !proxyNamePattern.MatchString(up.Name) {
			return fmt.Errorf("invalid upstream id/name %q/%q", up.ID, up.Name)
		}
		if ids[up.ID] {
			return fmt.Errorf("duplicate upstream id %q", up.ID)
		}
		ids[up.ID] = true
		parsed, err := url.Parse(up.Endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("upstream %s endpoint must be an HTTPS URL", up.ID)
		}
		up.Endpoint = strings.TrimRight(up.Endpoint, "/")
		if up.RoutePrefix == "" {
			up.RoutePrefix = up.ID
		}
		if !proxyNamePattern.MatchString(up.RoutePrefix) {
			return fmt.Errorf("invalid upstream route_prefix %q", up.RoutePrefix)
		}
		if prefixes[up.RoutePrefix] {
			return fmt.Errorf("duplicate upstream route_prefix %q", up.RoutePrefix)
		}
		prefixes[up.RoutePrefix] = true
		if up.RouteHost != "" {
			up.RouteHost = strings.ToLower(strings.TrimSpace(up.RouteHost))
			if err := netutil.ValidateHostPort(up.RouteHost); err != nil {
				return fmt.Errorf("upstream %s has invalid route_host %q: %w", up.ID, up.RouteHost, err)
			}
			if hosts[up.RouteHost] {
				return fmt.Errorf("duplicate upstream route_host %q", up.RouteHost)
			}
			hosts[up.RouteHost] = true
		}
		up.RepositoryPrefix = strings.Trim(up.RepositoryPrefix, "/")
		if up.RepositoryPrefix != "" {
			if err := oci.ValidateRepository(up.RepositoryPrefix); err != nil {
				return fmt.Errorf("upstream %s has invalid repository_prefix %q", up.ID, up.RepositoryPrefix)
			}
		}
		switch up.AuthType {
		case "":
			up.AuthType = "anonymous"
		case "anonymous", "basic", "bearer", "challenge":
		default:
			return fmt.Errorf("upstream %s has invalid auth_type %q", up.ID, up.AuthType)
		}
		if up.Password == "" && up.PasswordEnv != "" {
			up.Password = os.Getenv(up.PasswordEnv)
		}
		if up.BearerToken == "" && up.BearerTokenEnv != "" {
			up.BearerToken = os.Getenv(up.BearerTokenEnv)
		}
		if up.AuthType == "basic" && (up.Username == "" || up.Password == "") {
			return fmt.Errorf("upstream %s basic auth requires username and password", up.ID)
		}
		if up.AuthType == "bearer" && up.BearerToken == "" {
			return fmt.Errorf("upstream %s bearer auth requires bearer_token", up.ID)
		}
		if up.TagTTL == "" {
			up.TagTTL = "60s"
		}
		if value, err := time.ParseDuration(up.TagTTL); err != nil || value <= 0 {
			return fmt.Errorf("upstream %s has invalid tag_ttl %q", up.ID, up.TagTTL)
		}
		if up.RequestTimeout == "" {
			up.RequestTimeout = "30s"
		}
		if value, err := time.ParseDuration(up.RequestTimeout); err != nil || value <= 0 {
			return fmt.Errorf("upstream %s has invalid request_timeout %q", up.ID, up.RequestTimeout)
		}
		if up.MaxBlobBytes < 0 {
			return fmt.Errorf("upstream %s max_blob_bytes cannot be negative", up.ID)
		}
		if up.MaxBlobBytes == 0 {
			up.MaxBlobBytes = cfg.Storage.MaxBlobBytes
		}
	}
	return nil
}

func (u UpstreamConfig) TagTTLDuration() time.Duration {
	value, _ := time.ParseDuration(u.TagTTL)
	return value
}

func (u UpstreamConfig) RequestTimeoutDuration() time.Duration {
	value, _ := time.ParseDuration(u.RequestTimeout)
	return value
}

func (c Config) TokenTTL() (time.Duration, error) {
	value, err := time.ParseDuration(c.Auth.Registry.TokenTTL)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid auth.registry.token_ttl %q", c.Auth.Registry.TokenTTL)
	}
	if value > 15*time.Minute {
		value = 15 * time.Minute
	}
	return value, nil
}

func (c Config) UploadTTL() (time.Duration, error) {
	value, err := time.ParseDuration(c.Storage.UploadTTL)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid storage.uploads_ttl %q", c.Storage.UploadTTL)
	}
	return value, nil
}

func (c Config) Duration(name, raw string) (time.Duration, error) {
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return value, nil
}

func (c Config) WebSessionTTL() (time.Duration, error) {
	value, err := time.ParseDuration(c.Auth.Web.SessionTTL)
	if err != nil || value <= 0 || value > 7*24*time.Hour {
		return 0, fmt.Errorf("invalid auth.web.session_ttl %q", c.Auth.Web.SessionTTL)
	}
	return value, nil
}

func (c Config) WebIdleTimeout() (time.Duration, error) {
	value, err := time.ParseDuration(c.Auth.Web.IdleTimeout)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid auth.web.idle_timeout %q", c.Auth.Web.IdleTimeout)
	}
	sessionTTL, err := c.WebSessionTTL()
	if err != nil {
		return 0, err
	}
	if value > sessionTTL {
		value = sessionTTL
	}
	return value, nil
}

func (c Config) PublicScheme() string {
	if c.Server.PublicScheme != "" {
		return c.Server.PublicScheme
	}
	if c.Server.TLS.Mode == "file" {
		return "https"
	}
	return "http"
}

func parseTrustedProxy(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("empty CIDR or IP address")
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		if !prefix.IsValid() {
			return "", fmt.Errorf("invalid prefix")
		}
		return prefix.Masked().String(), nil
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return "", fmt.Errorf("must be a CIDR prefix or IP address")
	}
	return netip.PrefixFrom(address, address.BitLen()).String(), nil
}

func (c Config) Realm() string {
	if c.Auth.Registry.Realm != "" {
		return strings.TrimRight(c.Auth.Registry.Realm, "/")
	}
	return c.PublicScheme() + "://" + c.Server.PublicHost
}

func applyEnv(cfg *Config) error {
	setString := func(key string, target *string) {
		if value, ok := os.LookupEnv(key); ok {
			*target = value
		}
	}
	setString("CRPROXY_LISTEN", &cfg.Server.Listen)
	setString("CRPROXY_PUBLIC_HOST", &cfg.Server.PublicHost)
	setString("CRPROXY_PUBLIC_SCHEME", &cfg.Server.PublicScheme)
	setString("CRPROXY_STORAGE_ROOT", &cfg.Storage.Root)
	setString("CRPROXY_ADMIN_USERNAME", &cfg.Auth.BootstrapAdmin.Username)
	setString("CRPROXY_ADMIN_PASSWORD", &cfg.Auth.BootstrapAdmin.Password)
	setString("CRPROXY_JWT_SECRET_FILE", &cfg.Auth.JWTSecretFile)
	setString("CRPROXY_MASTER_KEY_FILE", &cfg.Auth.MasterKeyFile)
	setString("CRPROXY_AUTH_REALM", &cfg.Auth.Registry.Realm)
	setString("CRPROXY_AUTH_CHALLENGE", &cfg.Auth.Registry.Challenge)
	setString("CRPROXY_ON_FULL", &cfg.Storage.OnFull)
	if value, ok := os.LookupEnv("CRPROXY_ALLOW_ANONYMOUS"); ok {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid CRPROXY_ALLOW_ANONYMOUS %q", value)
		}
		cfg.Auth.Registry.AllowAnonymous = parsed
	}
	if value, ok := os.LookupEnv("CRPROXY_MAX_BLOB_BYTES"); ok {
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("invalid CRPROXY_MAX_BLOB_BYTES %q", value)
		}
		cfg.Storage.MaxBlobBytes = parsed
	}
	if value, ok := os.LookupEnv("CRPROXY_MAX_MANIFEST_BYTES"); ok {
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("invalid CRPROXY_MAX_MANIFEST_BYTES %q", value)
		}
		cfg.Storage.MaxManifestBytes = parsed
	}
	return nil
}
