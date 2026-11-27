package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var configEnvironmentKeys = []string{
	"CRPROXY_LISTEN",
	"CRPROXY_PUBLIC_HOST",
	"CRPROXY_PUBLIC_SCHEME",
	"CRPROXY_ADMIN_LISTEN",
	"CRPROXY_ADMIN_PUBLIC_HOST",
	"CRPROXY_ADMIN_PUBLIC_SCHEME",
	"CRPROXY_STORAGE_ROOT",
	"CRPROXY_ADMIN_USERNAME",
	"CRPROXY_ADMIN_PASSWORD",
	"CRPROXY_JWT_SECRET_FILE",
	"CRPROXY_MASTER_KEY_FILE",
	"CRPROXY_AUTH_REALM",
	"CRPROXY_AUTH_CHALLENGE",
	"CRPROXY_ON_FULL",
	"CRPROXY_ALLOW_ANONYMOUS",
	"CRPROXY_MAX_BLOB_BYTES",
	"CRPROXY_MAX_MANIFEST_BYTES",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

func TestLoadDefaults(t *testing.T) {
	isolateConfigEnvironment(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":5000" || cfg.Server.PublicHost != "localhost:5000" || cfg.PublicScheme() != "http" {
		t.Fatalf("unexpected server defaults: %+v", cfg.Server)
	}
	if cfg.Proxy.PathPrefix != "r" || cfg.Proxy.Disabled {
		t.Fatalf("unexpected proxy defaults: %+v", cfg.Proxy)
	}
	if cfg.Storage.MaxBlobBytes != 20<<30 || cfg.Storage.MaxManifestBytes != 4<<20 {
		t.Fatalf("unexpected storage limits: %+v", cfg.Storage)
	}
	if cfg.Auth.Registry.Challenge != "bearer" || cfg.Auth.Registry.TokenTTL != "5m" {
		t.Fatalf("unexpected registry auth defaults: %+v", cfg.Auth.Registry)
	}
	if cfg.Auth.Web.SessionTTL != "12h" || cfg.Auth.Web.IdleTimeout != "30m" {
		t.Fatalf("unexpected web auth defaults: %+v", cfg.Auth.Web)
	}
	if cfg.Realm() != "http://localhost:5000" {
		t.Fatalf("unexpected realm %q", cfg.Realm())
	}
	if cfg.AdminSeparate() || cfg.AdminListen() != ":5000" || cfg.AdminPublicURL() != "http://localhost:5000" {
		t.Fatalf("management plane must share the registry listener by default: admin=%+v", cfg.Server.Admin)
	}
}

func TestLoadAdminListener(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Run("explicit same listener is shared", func(t *testing.T) {
		cfg, err := Load(writeConfig(t, "server:\n  admin:\n    listen: ':5000'\n"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AdminSeparate() || cfg.AdminListen() != ":5000" || cfg.AdminPublicURL() != "http://localhost:5000" || cfg.ListenerMode() != "shared" {
			t.Fatalf("same listener must normalize to shared mode: %+v", cfg.Server)
		}
	})
	t.Run("equivalent wildcard listener is shared", func(t *testing.T) {
		cfg, err := Load(writeConfig(t, "server:\n  listen: ':5000'\n  admin:\n    listen: '0.0.0.0:5000'\n"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AdminSeparate() || cfg.ListenerMode() != "shared" {
			t.Fatalf("equivalent wildcard listeners must normalize to shared mode: %+v", cfg.Server)
		}
	})
	t.Run("split listener inherits TLS and scheme", func(t *testing.T) {
		cfg, err := Load(writeConfig(t, `server:
  public_host: registry.example.com:5000
  public_scheme: https
  tls:
    mode: file
    cert_file: cert.pem
    key_file: key.pem
  admin:
    listen: ':5443'
    public_host: registry.example.com:5443
`))
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.AdminSeparate() || cfg.AdminListen() != ":5443" || cfg.AdminPublicScheme() != "https" || cfg.AdminPublicURL() != "https://registry.example.com:5443" || cfg.ListenerMode() != "split" {
			t.Fatalf("unexpected split management plane: %+v", cfg.Server.Admin)
		}
		if cfg.AdminTLS().Mode != "file" || cfg.AdminTLS().CertFile != "cert.pem" {
			t.Fatalf("management TLS must inherit registry TLS: %+v", cfg.AdminTLS())
		}
		if cfg.RegistryPublicURL() != "https://registry.example.com:5000" {
			t.Fatalf("unexpected registry URL %q", cfg.RegistryPublicURL())
		}
	})
	t.Run("split listener can disable direct TLS", func(t *testing.T) {
		cfg, err := Load(writeConfig(t, `server:
  public_scheme: https
  admin:
    listen: ':5443'
    public_host: admin.example.com:5443
    public_scheme: https
    tls:
      mode: none
`))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AdminTLS().Mode != "" || cfg.AdminPublicScheme() != "https" {
			t.Fatalf("unexpected reverse-proxy management TLS: %+v", cfg.AdminTLS())
		}
	})
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{"invalid tls mode", "server:\n  admin:\n    listen: ':5443'\n    tls:\n      mode: optional\n", "server.admin.tls.mode"},
		{"missing tls files", "server:\n  admin:\n    listen: ':5443'\n    tls:\n      mode: file\n", "cert_file"},
		{"invalid public scheme", "server:\n  admin:\n    listen: ':5443'\n    public_host: admin.example.com:5443\n    public_scheme: ftp\n", "server.admin.public_scheme"},
		{"hsts with http management", "server:\n  public_scheme: https\n  tls:\n    hsts: true\n  admin:\n    listen: ':5443'\n    public_host: admin.example.com:5443\n    public_scheme: http\n", "server.admin.public_scheme=https"},
		{"overlapping listener", "server:\n  listen: ':5000'\n  admin:\n    listen: '127.0.0.1:5000'\n", "overlaps"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, test.body)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestLoadAdminEnvironmentOverrides(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("CRPROXY_ADMIN_LISTEN", ":5443")
	t.Setenv("CRPROXY_ADMIN_PUBLIC_HOST", "admin.example.com:5443")
	t.Setenv("CRPROXY_ADMIN_PUBLIC_SCHEME", "https")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AdminSeparate() || cfg.AdminListen() != ":5443" || cfg.AdminPublicURL() != "https://admin.example.com:5443" {
		t.Fatalf("admin environment overrides were not applied: %+v", cfg.Server.Admin)
	}
}

func TestLoadEnvironmentOverridesAndRejectsInvalidValues(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("CRPROXY_LISTEN", "127.0.0.1:5001")
	t.Setenv("CRPROXY_PUBLIC_HOST", "registry.example.com:5001")
	t.Setenv("CRPROXY_PUBLIC_SCHEME", "HTTPS")
	t.Setenv("CRPROXY_STORAGE_ROOT", "/srv/crproxy")
	t.Setenv("CRPROXY_ADMIN_USERNAME", "root")
	t.Setenv("CRPROXY_ADMIN_PASSWORD", "bootstrap-password-123")
	t.Setenv("CRPROXY_ALLOW_ANONYMOUS", "TRUE")
	t.Setenv("CRPROXY_ANONYMOUS_ACTIONS", "push, pull, push")
	t.Setenv("CRPROXY_MAX_BLOB_BYTES", "1048576")
	t.Setenv("CRPROXY_MAX_MANIFEST_BYTES", "65536")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:5001" || cfg.Server.PublicScheme != "https" || cfg.Storage.Root != "/srv/crproxy" {
		t.Fatalf("environment overrides were not applied: %+v %+v", cfg.Server, cfg.Storage)
	}
	if !cfg.Auth.Registry.AllowAnonymous || cfg.Auth.BootstrapAdmin.Username != "root" || cfg.Auth.BootstrapAdmin.Password != "bootstrap-password-123" {
		t.Fatalf("authentication overrides were not applied: %+v", cfg.Auth)
	}
	if strings.Join(cfg.Auth.Registry.AnonymousActions, ",") != "pull,push" {
		t.Fatalf("anonymous actions were not normalized: %+v", cfg.Auth.Registry.AnonymousActions)
	}
	if cfg.Storage.MaxBlobBytes != 1048576 || cfg.Storage.MaxManifestBytes != 65536 {
		t.Fatalf("limit overrides were not applied: %+v", cfg.Storage)
	}

	t.Setenv("CRPROXY_ALLOW_ANONYMOUS", "sometimes")
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "CRPROXY_ALLOW_ANONYMOUS") {
		t.Fatalf("invalid boolean environment value must fail, got %v", err)
	}
	t.Setenv("CRPROXY_ALLOW_ANONYMOUS", "true")
	t.Setenv("CRPROXY_MAX_BLOB_BYTES", "not-a-number")
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "CRPROXY_MAX_BLOB_BYTES") {
		t.Fatalf("invalid byte limit environment value must fail, got %v", err)
	}
}

func TestLoadOutboundProxy(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("HTTP_PROXY", "http://env-http.example.com:3128")
	t.Setenv("HTTPS_PROXY", "http://env-https.example.com:3128")
	t.Setenv("NO_PROXY", "localhost,.env.example.com")
	t.Setenv("TEST_OUTBOUND_PROXY_PASSWORD", "proxy-secret")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutboundProxy == nil || cfg.OutboundProxy.HTTPProxy != "http://env-http.example.com:3128" || cfg.OutboundProxy.HTTPSProxy != "http://env-https.example.com:3128" || cfg.OutboundProxy.NoProxy != "localhost,.env.example.com" {
		t.Fatalf("environment outbound proxy fallback was not applied: %+v", cfg.OutboundProxy)
	}

	cfg, err = Load(writeConfig(t, `outbound_proxy:
  http_proxy: http://yaml-http.example.com:3128
  https_proxy: http://yaml-https.example.com:3128
  no_proxy: 127.0.0.1,.yaml.example.com
  username: proxy-user
  password_env: TEST_OUTBOUND_PROXY_PASSWORD
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutboundProxy.HTTPProxy != "http://yaml-http.example.com:3128" || cfg.OutboundProxy.HTTPSProxy != "http://yaml-https.example.com:3128" || cfg.OutboundProxy.NoProxy != "127.0.0.1,.yaml.example.com" {
		t.Fatalf("YAML outbound proxy did not override the environment: %+v", cfg.OutboundProxy)
	}
	if cfg.OutboundProxy.Username != "proxy-user" || cfg.OutboundProxy.Password != "proxy-secret" {
		t.Fatalf("outbound proxy credentials were not resolved: %+v", cfg.OutboundProxy)
	}

	cfg, err = Load(writeConfig(t, "outbound_proxy:\n  http_proxy: ''\n  https_proxy: ''\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutboundProxy.HTTPProxy != "" || cfg.OutboundProxy.HTTPSProxy != "" {
		t.Fatalf("explicit empty YAML proxy must disable environment fallback: %+v", cfg.OutboundProxy)
	}
}

func TestLoadOutboundProxyValidation(t *testing.T) {
	isolateConfigEnvironment(t)
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"bad scheme", "outbound_proxy:\n  http_proxy: ftp://proxy.example.com:3128\n", "outbound_proxy.http_proxy"},
		{"missing host", "outbound_proxy:\n  https_proxy: http:///proxy\n", "outbound_proxy.https_proxy"},
		{"path", "outbound_proxy:\n  http_proxy: http://proxy.example.com:3128/path\n", "path"},
		{"query", "outbound_proxy:\n  http_proxy: http://proxy.example.com:3128?x=1\n", "query"},
		{"userinfo and separate auth", "outbound_proxy:\n  http_proxy: http://user:pass@proxy.example.com:3128\n  username: user\n  password: pass\n", "userinfo"},
		{"username without password", "outbound_proxy:\n  http_proxy: http://proxy.example.com:3128\n  username: user\n", "password"},
		{"password without username", "outbound_proxy:\n  http_proxy: http://proxy.example.com:3128\n  password: pass\n", "username"},
		{"credentials without proxy", "outbound_proxy:\n  username: user\n  password: pass\n", "http_proxy or https_proxy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestLoadRejectsUnknownAndMultipleDocuments(t *testing.T) {
	isolateConfigEnvironment(t)
	path := writeConfig(t, "server:\n  listen: ':5000'\n  misspelled: true\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field misspelled not found") {
		t.Fatalf("unknown fields must be rejected, got %v", err)
	}
	path = writeConfig(t, "server: {}\n---\nserver: {}\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("multiple documents must be rejected, got %v", err)
	}
}

func TestLoadServerAndTLSValidation(t *testing.T) {
	isolateConfigEnvironment(t)
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"public scheme", "server:\n  public_scheme: ftp\n", "public_scheme"},
		{"public host url", "server:\n  public_host: https://registry.example.com\n", "public_host"},
		{"public host path", "server:\n  public_host: registry.example.com/v2\n", "public_host"},
		{"public host port", "server:\n  public_host: registry.example.com:99999\n", "port"},
		{"tls mode", "server:\n  tls:\n    mode: automatic\n", "tls.mode"},
		{"tls files without mode", "server:\n  tls:\n    cert_file: cert.pem\n    key_file: key.pem\n", "mode=file"},
		{"tls file missing pair", "server:\n  tls:\n    mode: file\n    cert_file: cert.pem\n", "required"},
		{"tls minimum", "server:\n  tls:\n    min_version: '1.0'\n", "min_version"},
		{"negative reload", "server:\n  tls:\n    reload_seconds: -1\n", "reload_seconds"},
		{"hsts over http", "server:\n  public_scheme: http\n  tls:\n    hsts: true\n", "hsts"},
		{"trusted proxy", "server:\n  trusted_proxies: ['not-a-network']\n", "trusted_proxies"},
		{"challenge", "auth:\n  registry:\n    challenge: digest\n", "challenge"},
		{"realm relative", "auth:\n  registry:\n    realm: /v2/token\n", "realm"},
		{"realm downgrade", "server:\n  public_scheme: https\n  tls:\n    mode: file\n    cert_file: cert.pem\n    key_file: key.pem\nauth:\n  registry:\n    realm: http://registry.example.com/v2/token\n", "must use HTTPS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}

	cfg, err := Load(writeConfig(t, "server:\n  public_host: '[::1]:5000'\n  public_scheme: https\n  trusted_proxies: ['10.0.0.7', '192.168.0.0/16']\n  tls:\n    mode: file\n    cert_file: cert.pem\n    key_file: key.pem\n    min_version: '1.3'\n    reload_seconds: 0\n    hsts: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.PublicHost != "[::1]:5000" || cfg.PublicScheme() != "https" {
		t.Fatalf("unexpected validated server: %+v", cfg.Server)
	}
	if strings.Join(cfg.Server.TrustedProxies, ",") != "10.0.0.7/32,192.168.0.0/16" {
		t.Fatalf("trusted proxies were not normalized: %+v", cfg.Server.TrustedProxies)
	}
}

func TestLoadStorageValidation(t *testing.T) {
	isolateConfigEnvironment(t)
	tests := []struct {
		name string
		body string
		want string
	}{
		{"negative max blob", "  max_blob_bytes: -1\n", "max_blob_bytes"},
		{"negative max manifest", "  max_manifest_bytes: -1\n", "max_manifest_bytes"},
		{"negative max bytes", "  max_bytes: -1\n", "max_bytes"},
		{"negative cache max", "  cache_max_bytes: -1\n", "cache_max_bytes"},
		{"cache above max", "  max_bytes: 100\n  cache_max_bytes: 101\n", "cache_max_bytes"},
		{"negative min free", "  min_free_bytes: -1\n", "min_free_bytes"},
		{"high watermark", "  high_watermark: 1.1\n", "high_watermark"},
		{"low watermark", "  high_watermark: 0.8\n  low_watermark: 0.8\n", "low_watermark"},
		{"negative concurrency", "  download_concurrency: -1\n", "download_concurrency"},
		{"token ttl", "  uploads_ttl: 0s\n", "uploads_ttl"},
		{"tmp ttl", "  tmp_ttl: -1s\n", "tmp_ttl"},
		{"trash ttl", "  trash_ttl: nope\n", "trash_ttl"},
		{"audit retention", "  audit_retention: 0s\n", "audit_retention"},
		{"on full", "  on_full: drop\n", "on_full"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yaml := "storage:\n  max_bytes: 100000\n  cache_max_bytes: 0\n" + test.body
			_, err := Load(writeConfig(t, yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}

	cfg, err := Load(writeConfig(t, "storage:\n  max_bytes: 100000\n  cache_max_bytes: 50000\n  high_watermark: 0.8\n  low_watermark: 0.4\n  min_free_bytes: 0\n  download_concurrency: 0\n  max_blob_bytes: 0\n  max_manifest_bytes: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.CacheMaxBytes != 50000 || cfg.Storage.HighWatermark != 0.8 || cfg.Storage.LowWatermark != 0.4 || cfg.Storage.DownloadConcurrency != 8 {
		t.Fatalf("unexpected storage values: %+v", cfg.Storage)
	}
}

func TestLoadProxyAndUpstreamValidation(t *testing.T) {
	isolateConfigEnvironment(t)
	valid := `proxy:
  path_prefix: mirror
upstreams_seed:
  - id: ghcr
    name: ghcr
    endpoint: https://ghcr.io
    route_prefix: ghcr
    route_host: GHCR.Example.COM:443
    repository_prefix: example/
    auth_type: basic
    username: robot
    password_env: TEST_UPSTREAM_PASSWORD
    tag_ttl: 90s
    request_timeout: 45s
    max_blob_bytes: 1048576
`
	t.Setenv("TEST_UPSTREAM_PASSWORD", "upstream-password")
	cfg, err := Load(writeConfig(t, valid))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Proxy.PathPrefix != "mirror" || len(cfg.Upstreams) != 1 {
		t.Fatalf("unexpected proxy/upstream config: %+v %+v", cfg.Proxy, cfg.Upstreams)
	}
	upstream := cfg.Upstreams[0]
	if upstream.RouteHost != "ghcr.example.com:443" || upstream.RepositoryPrefix != "example" || upstream.Password != "upstream-password" {
		t.Fatalf("upstream was not normalized: %+v", upstream)
	}
	if upstream.TagTTLDuration() != 90*time.Second || upstream.RequestTimeoutDuration() != 45*time.Second || upstream.MaxBlobBytes != 1048576 {
		t.Fatalf("upstream timing and limits were not applied: %+v", upstream)
	}

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"proxy prefix", "proxy:\n  path_prefix: _catalog\n", "path_prefix"},
		{"http endpoint", upstreamYAML("endpoint: http://registry.example.com\n", ""), "HTTPS"},
		{"credentials in endpoint", upstreamYAML("endpoint: https://user:pass@registry.example.com\n", ""), "HTTPS"},
		{"query endpoint", upstreamYAML("endpoint: https://registry.example.com?x=1\n", ""), "HTTPS"},
		{"duplicate id", "upstreams_seed:\n  - id: dup\n    endpoint: https://one.example.com\n  - id: dup\n    endpoint: https://two.example.com\n", "duplicate upstream id"},
		{"duplicate prefix", "upstreams_seed:\n  - id: one\n    endpoint: https://one.example.com\n    route_prefix: shared\n  - id: two\n    endpoint: https://two.example.com\n    route_prefix: shared\n", "duplicate upstream route_prefix"},
		{"duplicate host", "upstreams_seed:\n  - id: one\n    endpoint: https://one.example.com\n    route_host: Registry.Example.com\n  - id: two\n    endpoint: https://two.example.com\n    route_host: registry.example.com\n", "duplicate upstream route_host"},
		{"invalid route host", upstreamYAML("route_host: https://registry.example.com\n", ""), "route_host"},
		{"invalid repository prefix", upstreamYAML("repository_prefix: Bad Prefix\n", ""), "repository_prefix"},
		{"basic credentials", upstreamYAML("auth_type: basic\n", ""), "requires username and password"},
		{"missing password env", upstreamYAML("auth_type: basic\nusername: robot\npassword_env: MISSING_UPSTREAM_PASSWORD\n", ""), "requires username and password"},
		{"bearer credentials", upstreamYAML("auth_type: bearer\n", ""), "requires bearer_token"},
		{"auth type", upstreamYAML("auth_type: oauth\n", ""), "invalid auth_type"},
		{"tag ttl", upstreamYAML("tag_ttl: 0s\n", ""), "tag_ttl"},
		{"request timeout", upstreamYAML("request_timeout: -1s\n", ""), "request_timeout"},
		{"max blob", upstreamYAML("max_blob_bytes: -1\n", ""), "max_blob_bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func upstreamYAML(extra, tail string) string {
	fields := "  - id: registry\n"
	if !strings.Contains(extra, "endpoint:") {
		fields += "    endpoint: https://registry.example.com\n"
	}
	return "upstreams_seed:\n" + fields + "    " + strings.ReplaceAll(strings.TrimSpace(extra), "\n", "\n    ") + "\n" + tail
}

func isolateConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range configEnvironmentKeys {
		value, present := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crproxy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
