package proxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpstreamUsesHTTPProxyFromEnvironment(t *testing.T) {
	if os.Getenv("CRPROXY_PROXY_ENV_HELPER") == "1" {
		upstream, err := NewUpstream(UpstreamConfig{
			ID:             "proxy-env",
			Endpoint:       "http://upstream.invalid",
			AuthType:       "anonymous",
			TagTTL:         time.Minute,
			RequestTimeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := upstream.Do(context.Background(), http.MethodGet, "/v2/", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || string(body) != "proxied" || resp.Header.Get("X-Proxy-Hit") != "1" {
			t.Fatalf("request did not use the configured proxy: status=%d body=%q headers=%v", resp.StatusCode, body, resp.Header)
		}
		return
	}

	var hits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() || r.URL.Host != "upstream.invalid" {
			http.Error(w, "not an absolute-form upstream request", http.StatusBadGateway)
			return
		}
		hits.Add(1)
		w.Header().Set("X-Proxy-Hit", "1")
		_, _ = io.WriteString(w, "proxied")
	}))
	defer proxyServer.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestUpstreamUsesHTTPProxyFromEnvironment$", "-test.v")
	cmd.Env = append(cleanProxyEnvironment(os.Environ()),
		"CRPROXY_PROXY_ENV_HELPER=1",
		"HTTP_PROXY="+proxyServer.URL,
		"HTTPS_PROXY="+proxyServer.URL,
		"NO_PROXY=",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proxy helper failed: %v\n%s", err, output)
	}
	if hits.Load() != 1 {
		t.Fatalf("proxy handled %d requests, want 1", hits.Load())
	}
}

func TestUpstreamUsesHTTPSProxyTunnelFromEnvironment(t *testing.T) {
	if os.Getenv("CRPROXY_HTTPS_PROXY_ENV_HELPER") == "1" {
		upstream, err := NewUpstream(UpstreamConfig{
			ID:                 "https-proxy-env",
			Endpoint:           os.Getenv("CRPROXY_HTTPS_PROXY_TARGET"),
			AuthType:           "anonymous",
			InsecureSkipVerify: true,
			TagTTL:             time.Minute,
			RequestTimeout:     5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := upstream.Do(context.Background(), http.MethodGet, "/v2/", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || string(body) != "tunneled" {
			t.Fatalf("HTTPS request did not use the CONNECT proxy: status=%d body=%q", resp.StatusCode, body)
		}
		return
	}

	upstreamServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tunneled")
	}))
	defer upstreamServer.Close()
	upstreamURL, err := url.Parse(upstreamServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	var connects atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		connects.Add(1)
		target, err := net.DialTimeout("tcp", upstreamURL.Host, 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			_ = target.Close()
			http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
			return
		}
		client, buffered, err := hijacker.Hijack()
		if err != nil {
			_ = target.Close()
			return
		}
		_, _ = fmt.Fprint(buffered, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		go func() {
			_, _ = io.Copy(target, client)
			_ = target.Close()
		}()
		_, _ = io.Copy(client, target)
		_ = client.Close()
	}))
	defer proxyServer.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestUpstreamUsesHTTPSProxyTunnelFromEnvironment$", "-test.v")
	cmd.Env = append(cleanProxyEnvironment(os.Environ()),
		"CRPROXY_HTTPS_PROXY_ENV_HELPER=1",
		"CRPROXY_HTTPS_PROXY_TARGET=https://upstream.test:"+upstreamURL.Port(),
		"HTTPS_PROXY="+proxyServer.URL,
		"NO_PROXY=",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("HTTPS proxy helper failed: %v\n%s", err, output)
	}
	if connects.Load() != 1 {
		t.Fatalf("proxy handled %d CONNECT requests, want 1", connects.Load())
	}
}

func TestUpstreamUsesConfiguredHTTPProxy(t *testing.T) {
	var hits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() || r.URL.Host != "upstream.invalid" {
			http.Error(w, "not an absolute-form upstream request", http.StatusBadGateway)
			return
		}
		hits.Add(1)
		_, _ = io.WriteString(w, "proxied")
	}))
	defer proxyServer.Close()

	upstream, err := NewUpstream(UpstreamConfig{
		ID:             "configured-proxy",
		Endpoint:       "http://upstream.invalid",
		AuthType:       "anonymous",
		RequestTimeout: 5 * time.Second,
		OutboundProxy:  &OutboundProxy{HTTPProxy: proxyServer.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := upstream.Do(context.Background(), http.MethodGet, "/v2/", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "proxied" {
		t.Fatalf("request did not use the configured proxy: status=%d body=%q", response.StatusCode, body)
	}
	if hits.Load() != 1 {
		t.Fatalf("proxy handled %d requests, want 1", hits.Load())
	}
}

func TestUpstreamConfiguredProxyHonorsNoProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "proxy must be bypassed", http.StatusBadGateway)
	}))
	defer proxyServer.Close()

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	defer upstreamServer.Close()

	upstream, err := NewUpstream(UpstreamConfig{
		ID:             "configured-proxy-no-proxy",
		Endpoint:       upstreamServer.URL,
		AuthType:       "anonymous",
		RequestTimeout: 5 * time.Second,
		OutboundProxy: &OutboundProxy{
			HTTPProxy: proxyServer.URL,
			NoProxy:   "127.0.0.1,localhost",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := upstream.Do(context.Background(), http.MethodGet, "/v2/", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "direct" {
		t.Fatalf("NO_PROXY did not bypass the configured proxy: body=%q", body)
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("proxy handled %d requests, want 0", proxyHits.Load())
	}
}

func TestUpstreamHotSwitchesConfiguredProxy(t *testing.T) {
	var firstHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		_, _ = io.WriteString(w, "first-proxy")
	}))
	defer first.Close()
	var secondHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		_, _ = io.WriteString(w, "second-proxy")
	}))
	defer second.Close()

	upstream, err := NewUpstream(UpstreamConfig{
		ID:             "hot-proxy",
		Endpoint:       "http://upstream.invalid",
		AuthType:       "anonymous",
		RequestTimeout: 5 * time.Second,
		OutboundProxy:  &OutboundProxy{HTTPProxy: first.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertProxyBody := func(want string) {
		t.Helper()
		response, err := upstream.Do(context.Background(), http.MethodGet, "/v2/", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != want {
			t.Fatalf("unexpected proxy response %q, want %q", body, want)
		}
	}
	assertProxyBody("first-proxy")
	if err := upstream.SetOutboundProxy(&OutboundProxy{HTTPProxy: second.URL}); err != nil {
		t.Fatal(err)
	}
	assertProxyBody("second-proxy")
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Fatalf("unexpected proxy hits first=%d second=%d", firstHits.Load(), secondHits.Load())
	}
}

func TestUpstreamConfiguredProxySendsCredentials(t *testing.T) {
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password"))
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Proxy-Authorization"); got != want {
			http.Error(w, "proxy authorization mismatch", http.StatusProxyAuthRequired)
			return
		}
		_, _ = io.WriteString(w, "authenticated")
	}))
	defer proxyServer.Close()

	upstream, err := NewUpstream(UpstreamConfig{
		ID:             "configured-proxy-auth",
		Endpoint:       "http://upstream.invalid",
		AuthType:       "anonymous",
		RequestTimeout: 5 * time.Second,
		OutboundProxy: &OutboundProxy{
			HTTPProxy: proxyServer.URL,
			Username:  "proxy-user",
			Password:  "proxy-password",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := upstream.Do(context.Background(), http.MethodGet, "/v2/", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "authenticated" {
		t.Fatalf("configured proxy credentials were not used: status=%d body=%q", response.StatusCode, body)
	}
}

func cleanProxyEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(name) {
		case "ALL_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "REQUEST_METHOD":
			continue
		default:
			clean = append(clean, item)
		}
	}
	return clean
}
