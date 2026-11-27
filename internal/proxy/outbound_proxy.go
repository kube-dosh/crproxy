package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// OutboundProxy controls the HTTP and HTTPS proxy used when contacting
// upstream registries. Authentication is applied to both proxy URLs.
type OutboundProxy struct {
	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string
	Username   string
	Password   string
}

func (p *OutboundProxy) ProxyFunc() (func(*http.Request) (*url.URL, error), error) {
	if p == nil {
		return http.ProxyFromEnvironment, nil
	}
	if err := validateOutboundProxyURL("http_proxy", p.HTTPProxy); err != nil {
		return nil, err
	}
	if err := validateOutboundProxyURL("https_proxy", p.HTTPSProxy); err != nil {
		return nil, err
	}
	if p.Username == "" && p.Password != "" {
		return nil, fmt.Errorf("outbound proxy username is required when password is configured")
	}
	if p.Username != "" && p.Password == "" {
		return nil, fmt.Errorf("outbound proxy password is required when username is configured")
	}

	config := &httpproxy.Config{
		HTTPProxy:  strings.TrimSpace(p.HTTPProxy),
		HTTPSProxy: strings.TrimSpace(p.HTTPSProxy),
		NoProxy:    strings.TrimSpace(p.NoProxy),
	}
	base := config.ProxyFunc()
	return func(request *http.Request) (*url.URL, error) {
		proxyURL, err := base(request.URL)
		if err != nil || proxyURL == nil || p.Username == "" {
			return proxyURL, err
		}
		authenticated := *proxyURL
		authenticated.User = url.UserPassword(p.Username, p.Password)
		return &authenticated, nil
	}, nil
}

func validateOutboundProxyURL(name, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	proxyURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") {
		return fmt.Errorf("invalid outbound proxy %s", name)
	}
	if proxyURL.Path != "" && proxyURL.Path != "/" || proxyURL.RawQuery != "" || proxyURL.Fragment != "" {
		return fmt.Errorf("invalid outbound proxy %s: path, query and fragment are not allowed", name)
	}
	return nil
}
