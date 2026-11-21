package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/gate"
	"github.com/Choffolo/spikes402/internal/lightning"
	"github.com/Choffolo/spikes402/pow"
)

func freePathServer(b *testing.B) *httptest.Server {
	b.Helper()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	b.Cleanup(origin.Close)
	ou, _ := url.Parse(origin.URL)
	g := gate.New(gate.Options{
		PriceSats: 10, Protect: []string{"/premium/**"},
		PoW:          func(int64) pow.Params { return pow.Params{Algorithm: pow.SHA256d, Difficulty: 8} },
		ChallengeTTL: time.Minute, SecretKey: []byte("ssssssssssssssssssssssssssssssss"),
		Lightning: lightning.NewFake(), Next: originProxy(ou),
		ChallengesPerMinute: 1000, MaxInvoicesPerMinute: 1000, MaxPendingInvoices: 1000,
	})
	srv := httptest.NewServer(g)
	b.Cleanup(srv.Close)
	return srv
}

func BenchmarkFreePath(b *testing.B) {
	srv := freePathServer(b)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		b.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		b.Fatal(resp.StatusCode)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(srv.URL + "/")
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			b.Fatal(resp.StatusCode)
		}
	}
}

func BenchmarkFreePathParallel(b *testing.B) {
	srv := freePathServer(b)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 256}}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		b.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := client.Get(srv.URL + "/")
			if err != nil {
				b.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				b.Errorf("status %d", resp.StatusCode)
			}
		}
	})
}
