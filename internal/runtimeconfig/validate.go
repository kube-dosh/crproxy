package runtimeconfig

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

func validateConfig(cfg Config) error {
	if err := validateRegistryAuth(cfg.RegistryAuth, cfg.Startup.PublicScheme); err != nil {
		return err
	}
	if err := validateWebSession(cfg.WebSession); err != nil {
		return err
	}
	if err := validateStorage(cfg.Storage); err != nil {
		return err
	}
	if _, err := positiveDuration("storage.uploads_ttl", cfg.Uploads.UploadsTTL); err != nil {
		return err
	}
	if _, err := positiveDuration("storage.audit_retention", cfg.Audit.AuditRetention); err != nil {
		return err
	}
	adminScheme := cfg.Startup.AdminPublicScheme
	if adminScheme == "" {
		adminScheme = cfg.Startup.PublicScheme
	}
	if cfg.Security.HSTS && (cfg.Startup.PublicScheme != "https" || adminScheme != "https") {
		return fmt.Errorf("%w: HSTS requires both registry and management public schemes to be https", ErrInvalid)
	}
	if err := validateOutboundProxy(cfg.Outbound); err != nil {
		return err
	}
	return nil
}

func validateRegistryAuth(value RegistryAuth, publicScheme string) error {
	if value.Challenge != "bearer" && value.Challenge != "basic" {
		return fmt.Errorf("%w: auth.registry.challenge must be bearer or basic", ErrInvalid)
	}
	if strings.TrimSpace(value.Service) == "" {
		return fmt.Errorf("%w: auth.registry.service is required", ErrInvalid)
	}
	realm, err := url.Parse(strings.TrimSpace(value.Realm))
	if err != nil || realm.Host == "" || (realm.Scheme != "http" && realm.Scheme != "https") || realm.User != nil || realm.Fragment != "" {
		return fmt.Errorf("%w: auth.registry.realm must be an absolute HTTP(S) URL", ErrInvalid)
	}
	if publicScheme == "https" && realm.Scheme != "https" {
		return fmt.Errorf("%w: auth.registry.realm must use HTTPS when public_scheme=https", ErrInvalid)
	}
	ttl, err := positiveDuration("auth.registry.token_ttl", value.TokenTTL)
	if err != nil {
		return err
	}
	if ttl > 15*time.Minute {
		return fmt.Errorf("%w: auth.registry.token_ttl cannot exceed 15m", ErrInvalid)
	}
	return nil
}

func normalizeRegistryAuth(value *RegistryAuth) error {
	if value == nil {
		return nil
	}
	if !value.AllowAnonymous {
		value.AnonymousActions = []string{}
		return nil
	}
	allowed := map[string]bool{"pull": true, "push": true, "delete": true, "catalog": true}
	seen := make(map[string]bool, len(value.AnonymousActions))
	for _, raw := range value.AnonymousActions {
		action := strings.ToLower(strings.TrimSpace(raw))
		if !allowed[action] {
			return fmt.Errorf("%w: auth.registry.anonymous_actions contains unsupported action %q", ErrInvalid, raw)
		}
		seen[action] = true
	}
	value.AnonymousActions = nil
	for _, action := range []string{"pull", "push", "delete", "catalog"} {
		if seen[action] {
			value.AnonymousActions = append(value.AnonymousActions, action)
		}
	}
	if len(value.AnonymousActions) == 0 {
		value.AnonymousActions = []string{"pull"}
	}
	return nil
}

func validateWebSession(value WebSession) error {
	sessionTTL, err := positiveDuration("auth.web.session_ttl", value.SessionTTL)
	if err != nil {
		return err
	}
	if sessionTTL > 7*24*time.Hour {
		return fmt.Errorf("%w: auth.web.session_ttl cannot exceed 168h", ErrInvalid)
	}
	idle, err := positiveDuration("auth.web.idle_timeout", value.IdleTimeout)
	if err != nil {
		return err
	}
	if idle > sessionTTL {
		return fmt.Errorf("%w: auth.web.idle_timeout cannot exceed session_ttl", ErrInvalid)
	}
	return nil
}

func validateStorage(value Storage) error {
	if value.MaxBytes <= 0 {
		return fmt.Errorf("%w: storage.max_bytes must be positive", ErrInvalid)
	}
	if value.CacheMaxBytes <= 0 || value.CacheMaxBytes > value.MaxBytes {
		return fmt.Errorf("%w: storage.cache_max_bytes must be positive and no greater than max_bytes", ErrInvalid)
	}
	if value.MinFreeBytes < 0 {
		return fmt.Errorf("%w: storage.min_free_bytes cannot be negative", ErrInvalid)
	}
	if value.HighWatermark <= 0 || value.HighWatermark > 1 {
		return fmt.Errorf("%w: storage.high_watermark must be in (0, 1]", ErrInvalid)
	}
	if value.LowWatermark <= 0 || value.LowWatermark >= value.HighWatermark {
		return fmt.Errorf("%w: storage.low_watermark must be in (0, high_watermark)", ErrInvalid)
	}
	if value.OnFull != "bypass" && value.OnFull != "reject" {
		return fmt.Errorf("%w: storage.on_full must be bypass or reject", ErrInvalid)
	}
	if value.MaxBlobBytes <= 0 {
		return fmt.Errorf("%w: storage.max_blob_bytes must be positive", ErrInvalid)
	}
	if value.MaxManifestBytes <= 0 {
		return fmt.Errorf("%w: storage.max_manifest_bytes must be positive", ErrInvalid)
	}
	if value.DownloadConcurrency <= 0 || value.DownloadConcurrency > 256 {
		return fmt.Errorf("%w: storage.download_concurrency must be between 1 and 256", ErrInvalid)
	}
	if _, err := positiveDuration("storage.tmp_ttl", value.TmpTTL); err != nil {
		return err
	}
	if _, err := positiveDuration("storage.trash_ttl", value.TrashTTL); err != nil {
		return err
	}
	return nil
}

func positiveDuration(name, raw string) (time.Duration, error) {
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive duration", ErrInvalid, name)
	}
	return value, nil
}

func validateOutboundProxy(value OutboundProxy) error {
	if err := validateProxyURL("outbound_proxy.http_proxy", value.HTTPProxy, value.Username != ""); err != nil {
		return err
	}
	if err := validateProxyURL("outbound_proxy.https_proxy", value.HTTPSProxy, value.Username != ""); err != nil {
		return err
	}
	if value.Username == "" && value.Password != "" {
		return fmt.Errorf("%w: outbound_proxy.username is required when password is configured", ErrInvalid)
	}
	if value.Username != "" && value.Password == "" {
		return fmt.Errorf("%w: outbound_proxy.password is required when username is configured", ErrInvalid)
	}
	if value.Username != "" && strings.TrimSpace(value.HTTPProxy) == "" && strings.TrimSpace(value.HTTPSProxy) == "" {
		return fmt.Errorf("%w: outbound_proxy.username requires http_proxy or https_proxy", ErrInvalid)
	}
	return nil
}

func validateProxyURL(name, raw string, separateAuth bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	proxyURL, err := url.Parse(raw)
	if err != nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") {
		return fmt.Errorf("%w: %s must be an http or https URL with a host", ErrInvalid, name)
	}
	if (proxyURL.Path != "" && proxyURL.Path != "/") || proxyURL.RawQuery != "" || proxyURL.Fragment != "" {
		return fmt.Errorf("%w: %s cannot include path, query or fragment", ErrInvalid, name)
	}
	if separateAuth && proxyURL.User != nil {
		return fmt.Errorf("%w: %s userinfo cannot be combined with username/password", ErrInvalid, name)
	}
	return nil
}
