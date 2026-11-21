package main

import (
	"io"
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
	proxy := newOriginProxy(u, tr)
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
