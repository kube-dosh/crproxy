package netutil

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// ValidateHostPort validates an HTTP Host header style value. It accepts a
// DNS name, IPv4 address, bracketed IPv6 address, and an optional numeric port.
func ValidateHostPort(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" || strings.Contains(value, "://") || strings.ContainsAny(value, "/?#@\\ \t\r\n") {
		return fmt.Errorf("must be a hostname or host:port")
	}
	parsed, err := url.Parse("//" + value)
	if err != nil || parsed.Host != value || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("must be a hostname or host:port")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("hostname is required")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("invalid port")
		}
	}
	if strings.Contains(host, ":") {
		if _, err := netip.ParseAddr(host); err != nil {
			return fmt.Errorf("invalid IPv6 address")
		}
		return nil
	}
	if len(host) > 253 {
		return fmt.Errorf("hostname is too long")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("invalid hostname")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return fmt.Errorf("invalid hostname")
			}
		}
	}
	return nil
}
