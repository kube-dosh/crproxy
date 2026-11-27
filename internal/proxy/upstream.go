package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type UpstreamConfig struct {
	ID                 string
	Endpoint           string
	RouteHost          string
	RoutePrefix        string
	RepositoryPrefix   string
	AuthType           string
	Username           string
	Password           string
	BearerToken        string
	CAPEM              []byte
	InsecureSkipVerify bool
	TagTTL             time.Duration
	RequestTimeout     time.Duration
	MaxBlobBytes       int64
	OutboundProxy      *OutboundProxy
}

type Upstream struct {
	ID               string
	Endpoint         *url.URL
	RouteHost        string
	RoutePrefix      string
	RepositoryPrefix string
	AuthType         string
	Username         string
	Password         string
	BearerToken      string
	TagTTL           time.Duration
	RequestTimeout   time.Duration
	MaxBlobBytes     int64
	client           *http.Client
	transport        *http.Transport
	outboundProxy    atomic.Pointer[OutboundProxy]
	tokenMu          sync.Mutex
	tokens           map[string]upstreamToken
	tokenFlights     flightGroup
}

type upstreamToken struct {
	value     string
	expiresAt time.Time
}

type bearerChallenge struct {
	Realm   string
	Service string
	Scope   string
}

func NewUpstream(cfg UpstreamConfig) (*Upstream, error) {
	if cfg.ID == "" || cfg.Endpoint == "" {
		return nil, errors.New("upstream id and endpoint are required")
	}
	endpoint, err := url.Parse(strings.TrimRight(cfg.Endpoint, "/"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return nil, fmt.Errorf("invalid upstream endpoint %q", cfg.Endpoint)
	}
	if cfg.TagTTL <= 0 {
		cfg.TagTTL = time.Minute
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.MaxBlobBytes <= 0 {
		cfg.MaxBlobBytes = 20 << 30
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec
	if len(cfg.CAPEM) > 0 {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(cfg.CAPEM) {
			return nil, fmt.Errorf("upstream %s has invalid ca_pem", cfg.ID)
		}
		tlsConfig.RootCAs = roots
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = cfg.RequestTimeout
	transport.ExpectContinueTimeout = time.Second
	transport.MaxIdleConnsPerHost = 10
	transport.DialContext = dialUpstream
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many upstream redirects")
			}
			if len(via) > 0 {
				previous := via[len(via)-1]
				if previous.URL.Scheme == "https" && req.URL.Scheme != "https" {
					return errors.New("upstream HTTPS downgrade blocked")
				}
				if !strings.EqualFold(previous.URL.Host, req.URL.Host) {
					req.Header.Del("Authorization")
				}
			}
			return nil
		},
	}
	upstream := &Upstream{
		ID:               cfg.ID,
		Endpoint:         endpoint,
		RouteHost:        strings.ToLower(strings.TrimSpace(cfg.RouteHost)),
		RoutePrefix:      cfg.RoutePrefix,
		RepositoryPrefix: strings.Trim(cfg.RepositoryPrefix, "/"),
		AuthType:         cfg.AuthType,
		Username:         cfg.Username,
		Password:         cfg.Password,
		BearerToken:      cfg.BearerToken,
		TagTTL:           cfg.TagTTL,
		RequestTimeout:   cfg.RequestTimeout,
		MaxBlobBytes:     cfg.MaxBlobBytes,
		client:           client,
		transport:        transport,
		tokens:           map[string]upstreamToken{},
	}
	if err := upstream.SetOutboundProxy(cfg.OutboundProxy); err != nil {
		return nil, err
	}
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		proxyFunc, err := upstream.outboundProxy.Load().ProxyFunc()
		if err != nil {
			return nil, err
		}
		return proxyFunc(req)
	}
	return upstream, nil
}

// SetOutboundProxy atomically changes the proxy used by subsequent upstream
// requests. Existing idle connections are closed so a new proxy takes effect
// immediately without rebuilding every upstream.
func (u *Upstream) SetOutboundProxy(value *OutboundProxy) error {
	var next *OutboundProxy
	if value != nil {
		copy := *value
		next = &copy
	}
	if _, err := next.ProxyFunc(); err != nil {
		return err
	}
	u.outboundProxy.Store(next)
	if u.transport != nil {
		u.transport.CloseIdleConnections()
	}
	return nil
}

func dialUpstream(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if network != "tcp" {
		return dialer.DialContext(ctx, network, address)
	}
	conn, err := dialer.DialContext(ctx, "tcp4", address)
	if err == nil || ctx.Err() != nil {
		return conn, err
	}
	return dialer.DialContext(ctx, "tcp6", address)
}

func (u *Upstream) MapRepository(repository string) string {
	if u.RepositoryPrefix != "" && !strings.Contains(repository, "/") {
		return u.RepositoryPrefix + "/" + repository
	}
	return repository
}

func (u *Upstream) Test(ctx context.Context) error {
	testCtx, cancel := context.WithTimeout(ctx, u.RequestTimeout)
	defer cancel()
	resp, err := u.Do(testCtx, http.MethodGet, "/v2/", "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	return fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
}

func (u *Upstream) Do(ctx context.Context, method, path, repository string, headers http.Header) (*http.Response, error) {
	target := *u.Endpoint
	pathPart, rawQuery, _ := strings.Cut(path, "?")
	target.Path = strings.TrimRight(target.Path, "/") + pathPart
	target.RawQuery = rawQuery
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "crproxy/0.1")
		for key, values := range headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		rejectedToken := u.applyAuth(req, repository)
		resp, err := u.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}
		challenge, ok := parseBearerChallenge(resp.Header.Values("WWW-Authenticate"))
		if !ok {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		_, err = u.refreshToken(ctx, challenge, repository, rejectedToken)
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("upstream authentication retry exhausted")
}

func (u *Upstream) refreshToken(ctx context.Context, challenge bearerChallenge, repository, rejectedToken string) (string, error) {
	if cached, ok := u.cachedToken(repository); ok && cached != rejectedToken {
		return cached, nil
	}
	var token string
	err := u.tokenFlights.Do(repository, func() error {
		if cached, ok := u.cachedToken(repository); ok && cached != rejectedToken {
			token = cached
			return nil
		}
		u.clearToken(repository)
		value, ttl, err := u.fetchToken(ctx, challenge, repository)
		if err != nil {
			return err
		}
		u.storeToken(repository, value, ttl)
		token = value
		return nil
	})
	if err != nil {
		return "", err
	}
	if token != "" {
		return token, nil
	}
	if cached, ok := u.cachedToken(repository); ok {
		return cached, nil
	}
	return "", errors.New("upstream token refresh produced no token")
}

func (u *Upstream) applyAuth(req *http.Request, repository string) string {
	if token, ok := u.cachedToken(repository); ok {
		req.Header.Set("Authorization", "Bearer "+token)
		return token
	}
	switch u.AuthType {
	case "basic":
		if u.Username != "" {
			req.SetBasicAuth(u.Username, u.Password)
		}
	case "bearer":
		if u.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+u.BearerToken)
		}
	}
	return ""
}

func (u *Upstream) cachedToken(repository string) (string, bool) {
	u.tokenMu.Lock()
	defer u.tokenMu.Unlock()
	token, ok := u.tokens[repository]
	if !ok || time.Now().After(token.expiresAt) {
		delete(u.tokens, repository)
		return "", false
	}
	return token.value, true
}

func (u *Upstream) storeToken(repository, value string, ttl time.Duration) {
	u.tokenMu.Lock()
	defer u.tokenMu.Unlock()
	u.tokens[repository] = upstreamToken{value: value, expiresAt: time.Now().Add(ttl)}
}

func (u *Upstream) clearToken(repository string) {
	u.tokenMu.Lock()
	defer u.tokenMu.Unlock()
	delete(u.tokens, repository)
}

func (u *Upstream) fetchToken(ctx context.Context, challenge bearerChallenge, repository string) (string, time.Duration, error) {
	realm, err := url.Parse(challenge.Realm)
	if err != nil || realm.Host == "" {
		return "", 0, errors.New("invalid upstream token realm")
	}
	if u.Endpoint.Scheme == "https" && realm.Scheme != "https" {
		return "", 0, errors.New("upstream token realm must use HTTPS")
	}
	if challenge.Scope == "" && repository != "" {
		challenge.Scope = "repository:" + u.MapRepository(repository) + ":pull"
	}
	query := realm.Query()
	if challenge.Service != "" {
		query.Set("service", challenge.Service)
	}
	if challenge.Scope != "" {
		query.Set("scope", challenge.Scope)
	}
	if u.Username != "" {
		query.Set("account", u.Username)
	}
	realm.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "crproxy/0.1")
	if u.Username != "" {
		req.SetBasicAuth(u.Username, u.Password)
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("upstream token endpoint returned %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, err
	}
	var payload struct {
		Token       string          `json:"token"`
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", 0, err
	}
	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}
	if token == "" {
		return "", 0, errors.New("upstream token response has no token")
	}
	expiresIn := int64(60)
	if len(payload.ExpiresIn) > 0 {
		if value, err := strconv.ParseInt(strings.Trim(string(payload.ExpiresIn), `"`), 10, 64); err == nil && value > 0 {
			expiresIn = value
		}
	}
	ttl := time.Duration(expiresIn) * time.Second
	if ttl > 30*time.Second {
		ttl -= 30 * time.Second
	}
	if ttl < time.Second {
		ttl = time.Second
	}
	return token, ttl, nil
}

func parseBearerChallenge(headers []string) (bearerChallenge, bool) {
	for _, header := range headers {
		value := strings.TrimSpace(header)
		if len(value) < 7 || !strings.EqualFold(value[:7], "Bearer ") {
			continue
		}
		params := parseAuthParams(value[7:])
		if params["realm"] == "" {
			continue
		}
		return bearerChallenge{Realm: params["realm"], Service: params["service"], Scope: params["scope"]}, true
	}
	return bearerChallenge{}, false
}

func parseAuthParams(value string) map[string]string {
	params := map[string]string{}
	var items []string
	start := 0
	quoted := false
	escaped := false
	for i, r := range value {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quoted {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if r == ',' && !quoted {
			items = append(items, value[start:i])
			start = i + 1
		}
	}
	items = append(items, value[start:])
	for _, item := range items {
		key, value, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		params[key] = value
	}
	return params
}
