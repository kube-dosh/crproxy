// Package requestinfo resolves the externally visible request origin while
// only honoring forwarding headers received from explicitly trusted proxies.
package requestinfo

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type Resolver struct {
	trusted        []netip.Prefix
	fallbackScheme string
}

func New(trusted []string, fallbackScheme string) (*Resolver, error) {
	resolver := &Resolver{fallbackScheme: strings.ToLower(strings.TrimSpace(fallbackScheme))}
	if resolver.fallbackScheme != "http" && resolver.fallbackScheme != "https" {
		return nil, fmt.Errorf("fallback scheme must be http or https")
	}
	for _, raw := range trusted {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			address, addressErr := netip.ParseAddr(strings.TrimSpace(raw))
			if addressErr != nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", raw)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		resolver.trusted = append(resolver.trusted, prefix.Masked())
	}
	return resolver, nil
}

func (r *Resolver) IsTrusted(remoteAddr string) bool {
	if r == nil || len(r.trusted) == 0 {
		return false
	}
	address := remoteIP(remoteAddr)
	if !address.IsValid() {
		return false
	}
	for _, prefix := range r.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (r *Resolver) ClientIP(request *http.Request) string {
	if request == nil {
		return ""
	}
	remote := remoteIP(request.RemoteAddr)
	if !r.IsTrusted(request.RemoteAddr) {
		if remote.IsValid() {
			return remote.String()
		}
		return request.RemoteAddr
	}
	chain := forwardingChain(request)
	if len(chain) == 0 {
		if remote.IsValid() {
			return remote.String()
		}
		return request.RemoteAddr
	}
	chain = append(chain, remote)
	for index := len(chain) - 1; index >= 0; index-- {
		candidate := chain[index]
		if !candidate.IsValid() {
			continue
		}
		if index == 0 || !r.contains(candidate) {
			return candidate.String()
		}
	}
	return remote.String()
}

func (r *Resolver) Scheme(request *http.Request) string {
	if request == nil {
		if r == nil {
			return "http"
		}
		return r.fallbackScheme
	}
	if request.TLS != nil {
		return "https"
	}
	if r.IsTrusted(request.RemoteAddr) {
		if value := firstForwardedValue(request.Header.Get("X-Forwarded-Proto")); value != "" {
			value = strings.ToLower(value)
			if value == "http" || value == "https" {
				return value
			}
		}
		if value := forwardedParameter(request.Header.Get("Forwarded"), "proto"); value == "http" || value == "https" {
			return value
		}
	}
	if r != nil && r.fallbackScheme != "" {
		return r.fallbackScheme
	}
	return "http"
}

func (r *Resolver) Host(request *http.Request) string {
	if request == nil {
		return ""
	}
	host := strings.TrimSpace(request.Host)
	if r.IsTrusted(request.RemoteAddr) {
		if value := firstForwardedValue(request.Header.Get("X-Forwarded-Host")); validAuthority(value) {
			host = value
		} else if value := forwardedParameter(request.Header.Get("Forwarded"), "host"); validAuthority(value) {
			host = value
		}
	}
	return normalizeHost(host)
}

func (r *Resolver) OriginAllowed(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" && parsed.Path != "/" {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, r.Scheme(request)) {
		return false
	}
	return strings.EqualFold(normalizeHost(parsed.Host), r.Host(request))
}

func (r *Resolver) contains(address netip.Addr) bool {
	for _, prefix := range r.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func remoteIP(remoteAddr string) netip.Addr {
	value := strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}
	}
	return address.Unmap()
}

func forwardingChain(request *http.Request) []netip.Addr {
	var result []netip.Addr
	for _, header := range request.Header.Values("X-Forwarded-For") {
		for _, item := range strings.Split(header, ",") {
			if address := parseForwardedAddress(item); address.IsValid() {
				result = append(result, address)
			}
		}
	}
	if len(result) > 0 {
		return result
	}
	for _, header := range request.Header.Values("Forwarded") {
		for _, item := range splitForwarded(header) {
			if value := forwardedElementParameter(item, "for"); value != "" {
				if address := parseForwardedAddress(value); address.IsValid() {
					result = append(result, address)
				}
			}
		}
	}
	return result
}

func parseForwardedAddress(raw string) netip.Addr {
	value := strings.TrimSpace(raw)
	if value == "" || strings.EqualFold(value, "unknown") || strings.HasPrefix(value, "_") {
		return netip.Addr{}
	}
	value = unquote(value)
	if strings.HasPrefix(value, "[") {
		if end := strings.IndexByte(value, ']'); end >= 0 {
			value = value[1:end]
		}
	} else if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	if zone := strings.LastIndexByte(value, '%'); zone >= 0 {
		value = value[:zone]
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}
	}
	return address.Unmap()
}

func firstForwardedValue(raw string) string {
	for _, item := range strings.Split(raw, ",") {
		value := strings.TrimSpace(item)
		if value != "" {
			return value
		}
	}
	return ""
}

func forwardedParameter(raw, key string) string {
	for _, item := range splitForwarded(raw) {
		if value := forwardedElementParameter(item, key); value != "" {
			return strings.ToLower(value)
		}
	}
	return ""
}

func forwardedElementParameter(element, key string) string {
	for _, pair := range splitQuoted(element, ';') {
		name, value, ok := strings.Cut(pair, "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), key) {
			return unquote(strings.TrimSpace(value))
		}
	}
	return ""
}

func splitForwarded(raw string) []string {
	return splitQuoted(raw, ',')
}

func splitQuoted(raw string, separator byte) []string {
	var result []string
	start := 0
	inQuote := false
	escaped := false
	for index := 0; index < len(raw); index++ {
		character := raw[index]
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && inQuote {
			escaped = true
			continue
		}
		if character == '"' {
			inQuote = !inQuote
			continue
		}
		if character == separator && !inQuote {
			result = append(result, strings.TrimSpace(raw[start:index]))
			start = index + 1
		}
	}
	result = append(result, strings.TrimSpace(raw[start:]))
	return result
}

func unquote(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if decoded, err := strconv.Unquote(value); err == nil {
			return decoded
		}
		return value[1 : len(value)-1]
	}
	return value
}

func validAuthority(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || strings.ContainsAny(value, ",/\\@ \t\r\n") {
		return false
	}
	parsed, err := url.Parse("//" + value)
	return err == nil && parsed.Host != "" && parsed.User == nil && parsed.Path == ""
}

func normalizeHost(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}
