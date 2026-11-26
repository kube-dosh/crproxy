package requestinfo

import (
	"net/http/httptest"
	"testing"
)

func TestForwardedHeadersRequireTrustedProxy(t *testing.T) {
	resolver, err := New([]string{"127.0.0.1/32", "10.0.0.0/8"}, "https")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://internal.local/api/v1/overview", nil)
	request.RemoteAddr = "127.0.0.1:45123"
	request.Host = "internal.local"
	request.Header.Set("X-Forwarded-For", "203.0.113.10, 10.2.3.4")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "registry.example.com")
	request.Header.Set("Origin", "https://registry.example.com")
	if got := resolver.ClientIP(request); got != "203.0.113.10" {
		t.Fatalf("unexpected client IP %q", got)
	}
	if got := resolver.Scheme(request); got != "https" {
		t.Fatalf("unexpected scheme %q", got)
	}
	if got := resolver.Host(request); got != "registry.example.com" {
		t.Fatalf("unexpected host %q", got)
	}
	if !resolver.OriginAllowed(request) {
		t.Fatal("trusted forwarded origin should be allowed")
	}
	request.Header.Set("Origin", "http://registry.example.com")
	if resolver.OriginAllowed(request) {
		t.Fatal("scheme mismatch must be rejected")
	}
}

func TestUntrustedPeerCannotSpoofForwardingHeaders(t *testing.T) {
	resolver, err := New([]string{"10.0.0.1/32"}, "http")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "http://registry.local/api/v1/auth/login", nil)
	request.RemoteAddr = "203.0.113.9:5000"
	request.Host = "registry.local"
	request.Header.Set("X-Forwarded-For", "1.2.3.4")
	request.Header.Set("X-Forwarded-Host", "evil.example")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("Origin", "https://evil.example")
	if got := resolver.ClientIP(request); got != "203.0.113.9" {
		t.Fatalf("unexpected spoofed client IP %q", got)
	}
	if got := resolver.Host(request); got != "registry.local" {
		t.Fatalf("unexpected spoofed host %q", got)
	}
	if resolver.OriginAllowed(request) {
		t.Fatal("untrusted origin must be rejected")
	}
}

func TestRFC7239ForwardedAndIPv6(t *testing.T) {
	resolver, err := New([]string{"127.0.0.1"}, "https")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://internal.local/", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "internal.local"
	request.Header.Set("Forwarded", `for="[2001:db8::7]:4711";proto=https;host="registry.example.com"`)
	if got := resolver.ClientIP(request); got != "2001:db8::7" {
		t.Fatalf("unexpected RFC7239 client IP %q", got)
	}
	if got := resolver.Host(request); got != "registry.example.com" {
		t.Fatalf("unexpected RFC7239 host %q", got)
	}
	if got := resolver.Scheme(request); got != "https" {
		t.Fatalf("unexpected RFC7239 scheme %q", got)
	}
}
