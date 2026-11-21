package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOriginErrorIsClean502(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(dead.URL)
	dead.Close()
	proxy := originProxy(u)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest("GET", "http://example.com/x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(strings.ToLower(body), "bad gateway") {
		t.Fatalf("body %q", body)
	}
}

func TestOriginHeaderTimeout(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		io.WriteString(w, "late")
	}))
	defer origin.Close()
	u, _ := url.Parse(origin.URL)
	tr := originTransport()
	tr.ResponseHeaderTimeout = 50 * time.Millisecond
	proxy := newOriginProxy(u, originOpt{Transport: tr})
	rec := httptest.NewRecorder()
	started := time.Now()
	proxy.ServeHTTP(rec, httptest.NewRequest("GET", "http://example.com/x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d after %s", rec.Code, time.Since(started))
	}
	if time.Since(started) > 300*time.Millisecond {
		t.Fatalf("timeout took too long: %s", time.Since(started))
	}
}

func TestOriginTransportKeepsIdleConns(t *testing.T) {
	tr := originTransport()
	if tr.MaxIdleConnsPerHost < 256 || tr.MaxIdleConns < tr.MaxIdleConnsPerHost {
		t.Fatalf("idle conns: per host %d, total %d", tr.MaxIdleConnsPerHost, tr.MaxIdleConns)
	}
	if tr.ResponseHeaderTimeout <= 0 || tr.TLSHandshakeTimeout <= 0 {
		t.Fatal("timeouts not set")
	}
}

func TestForwardedHeadersUntrustedPeer(t *testing.T) {
	got, gotHost, origin := captureOrigin(t)
	defer origin.Close()
	u, _ := url.Parse(origin.URL)
	proxy := newOriginProxy(u, originOpt{ClientIPHeader: "X-Real-IP"})

	req := httptest.NewRequest(http.MethodGet, "http://www.example.com/x", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	req.Header.Set("Forwarded", "for=1.2.3.4")
	proxy.ServeHTTP(httptest.NewRecorder(), req)

	if *gotHost != u.Host {
		t.Errorf("Host %q, want the origin host %q", *gotHost, u.Host)
	}
	if (*got).Get("X-Forwarded-For") != "203.0.113.9" {
		t.Errorf("X-Forwarded-For %q, want the socket address", (*got).Get("X-Forwarded-For"))
	}
	if (*got).Get("X-Forwarded-Proto") != "http" {
		t.Errorf("X-Forwarded-Proto %q, want http (no TLS)", (*got).Get("X-Forwarded-Proto"))
	}
	if (*got).Get("X-Forwarded-Host") != u.Host {
		t.Errorf("X-Forwarded-Host %q, want the origin host (not the request Host)", (*got).Get("X-Forwarded-Host"))
	}
	for _, name := range []string{"X-Real-IP", "CF-Connecting-IP", "Forwarded"} {
		if v := (*got).Get(name); v != "" {
			t.Errorf("%s %q leaked from an untrusted peer", name, v)
		}
	}
}

func TestForwardedHeadersTrustedPeer(t *testing.T) {
	got, gotHost, origin := captureOrigin(t)
	defer origin.Close()
	u, _ := url.Parse(origin.URL)
	proxy := newOriginProxy(u, originOpt{
		Trusted:        []*net.IPNet{mustCIDR(t, "127.0.0.0/8")},
		ClientIPHeader: "X-Real-IP",
	})

	req := httptest.NewRequest(http.MethodGet, "http://www.example.com/x", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "www.example.com")
	req.Header.Set("X-Real-IP", "203.0.113.9")
	proxy.ServeHTTP(httptest.NewRecorder(), req)

	if *gotHost != u.Host {
		t.Errorf("Host %q, want the origin host when preserve_host is off", *gotHost)
	}
	if (*got).Get("X-Forwarded-For") != "203.0.113.9, 127.0.0.1" {
		t.Errorf("X-Forwarded-For %q, want the inbound chain plus the peer", (*got).Get("X-Forwarded-For"))
	}
	if (*got).Get("X-Forwarded-Proto") != "https" {
		t.Errorf("X-Forwarded-Proto %q, want the inbound value", (*got).Get("X-Forwarded-Proto"))
	}
	if (*got).Get("X-Forwarded-Host") != "www.example.com" {
		t.Errorf("X-Forwarded-Host %q, want the inbound public host", (*got).Get("X-Forwarded-Host"))
	}
	if (*got).Get("X-Real-IP") != "203.0.113.9" {
		t.Errorf("X-Real-IP %q, want the inbound value from a trusted proxy", (*got).Get("X-Real-IP"))
	}
}

func TestPreserveHost(t *testing.T) {
	got, gotHost, origin := captureOrigin(t)
	defer origin.Close()
	u, _ := url.Parse(origin.URL)
	loopback := []*net.IPNet{mustCIDR(t, "127.0.0.0/8")}

	req := httptest.NewRequest(http.MethodGet, "http://www.example.com/x", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	proxy := newOriginProxy(u, originOpt{Trusted: loopback, PreserveHost: true})
	proxy.ServeHTTP(httptest.NewRecorder(), req)
	if *gotHost != "www.example.com" {
		t.Fatalf("trusted preserve_host: Host %q, want www.example.com", *gotHost)
	}
	if (*got).Get("X-Forwarded-Host") != "www.example.com" {
		t.Fatalf("X-Forwarded-Host %q", (*got).Get("X-Forwarded-Host"))
	}

	// An untrusted peer cannot force the origin Host, even with preserve_host.
	req = httptest.NewRequest(http.MethodGet, "http://evil.example/x", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-Host", "evil.example")
	proxy.ServeHTTP(httptest.NewRecorder(), req)
	if *gotHost != u.Host {
		t.Fatalf("untrusted preserve_host: Host %q, want the origin host %q", *gotHost, u.Host)
	}
	if (*got).Get("X-Forwarded-Host") != u.Host {
		t.Fatalf("untrusted X-Forwarded-Host %q, want the origin host", (*got).Get("X-Forwarded-Host"))
	}

	// With public_hosts, only a listed name is sent through.
	proxy = newOriginProxy(u, originOpt{
		Trusted:      loopback,
		PreserveHost: true,
		PublicHosts:  []string{"www.example.com", "example.com"},
	})
	req = httptest.NewRequest(http.MethodGet, "http://www.example.com:443/x", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	proxy.ServeHTTP(httptest.NewRecorder(), req)
	if *gotHost != "www.example.com:443" {
		t.Fatalf("listed host: Host %q, want the inbound Host", *gotHost)
	}

	req = httptest.NewRequest(http.MethodGet, "http://not-listed.example/x", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	proxy.ServeHTTP(httptest.NewRecorder(), req)
	if *gotHost != u.Host {
		t.Fatalf("unlisted host: Host %q, want the origin host %q", *gotHost, u.Host)
	}
}

func TestPreserveHostPrefersForwardedHost(t *testing.T) {
	_, gotHost, origin := captureOrigin(t)
	defer origin.Close()
	u, _ := url.Parse(origin.URL)
	proxy := newOriginProxy(u, originOpt{
		Trusted:      []*net.IPNet{mustCIDR(t, "10.0.0.0/8")},
		PreserveHost: true,
		PublicHosts:  []string{"www.example.com"},
	})
	req := httptest.NewRequest(http.MethodGet, "http://spikes402.internal/x", nil)
	req.RemoteAddr = "10.0.0.2:5555"
	req.Header.Set("X-Forwarded-Host", "www.example.com")
	proxy.ServeHTTP(httptest.NewRecorder(), req)
	if *gotHost != "www.example.com" {
		t.Fatalf("Host %q, want X-Forwarded-Host (the public name)", *gotHost)
	}
}

func captureOrigin(t *testing.T) (*http.Header, *string, *httptest.Server) {
	t.Helper()
	var got http.Header
	var host string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		host = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	return &got, &host, origin
}

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
