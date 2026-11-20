package botid

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Choffolo/spikes402/webbotauth"
)

// fakeDNS answers from tables and counts lookups.
type fakeDNS struct {
	ptr   map[string][]string
	a     map[string][]string
	fail  bool
	calls atomic.Int64
}

func (f *fakeDNS) LookupAddr(_ context.Context, addr string) ([]string, error) {
	f.calls.Add(1)
	if f.fail {
		return nil, errors.New("timeout")
	}
	if names, ok := f.ptr[addr]; ok {
		return names, nil
	}
	return nil, &net.DNSError{Err: "not found", IsNotFound: true}
}

func (f *fakeDNS) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	var out []net.IPAddr
	for _, s := range f.a[host] {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out, nil
}

var bots = []Bot{
	{Name: "Googlebot", UserAgent: "Googlebot", IPRangesURL: "x", ReverseDNS: []string{"googlebot.com"}},
	{Name: "GPTBot", UserAgent: "GPTBot", IPRangesURL: "x"},
}

func req(ua string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/premium/a", nil)
	r.Header.Set("User-Agent", ua)
	return r
}

const googleUA = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"

func TestIPListClassification(t *testing.T) {
	c := New(bots, nil)
	c.SetRanges("GPTBot", []netip.Prefix{netip.MustParsePrefix("20.0.0.0/24"), netip.MustParsePrefix("2001:db8::/32")})
	cases := []struct {
		ua, ip string
		want   Kind
	}{
		{"Mozilla/5.0; compatible; GPTBot/1.2", "20.0.0.7", Verified},
		{"Mozilla/5.0; compatible; gptbot/1.2", "2001:db8::5", Verified}, // case-insensitive
		{"Mozilla/5.0; compatible; GPTBot/1.2", "::ffff:20.0.0.7", Verified},
		{"Mozilla/5.0; compatible; GPTBot/1.2", "6.6.6.6", Impostor},
		{"Mozilla/5.0 Firefox", "6.6.6.6", Unknown},
		{"", "6.6.6.6", Unknown},
	}
	for _, tc := range cases {
		got := c.Classify(req(tc.ua), net.ParseIP(tc.ip), "https")
		if got.Kind != tc.want {
			t.Errorf("%q from %s: got %v, want %s", tc.ua, tc.ip, got, tc.want)
		}
		if got.Kind == Verified && (got.Name != "GPTBot" || got.Method != "ip-list") {
			t.Errorf("got %v", got)
		}
	}
}

func TestReverseDNSWhenNoList(t *testing.T) {
	c := New(bots, nil)
	dns := &fakeDNS{
		ptr: map[string][]string{
			"66.249.66.1": {"crawl-66-249-66-1.googlebot.com."},
			"6.6.6.6":     {"crawl.googlebot.com.evil.test."},
			"7.7.7.7":     {"fake.googlebot.com."}, // PTR set by the attacker, forward does not confirm
		},
		a: map[string][]string{
			"crawl-66-249-66-1.googlebot.com": {"66.249.66.1"},
			"fake.googlebot.com":              {"66.249.66.99"},
		},
	}
	c.resolver = dns
	for ip, want := range map[string]Kind{"66.249.66.1": Verified, "6.6.6.6": Impostor, "7.7.7.7": Impostor, "8.8.8.8": Impostor} {
		if got := c.Classify(req(googleUA), net.ParseIP(ip), "https"); got.Kind != want {
			t.Errorf("%s: got %v, want %s", ip, got, want)
		}
	}
	// Results are cached.
	before := dns.calls.Load()
	c.Classify(req(googleUA), net.ParseIP("66.249.66.1"), "https")
	if dns.calls.Load() != before {
		t.Fatal("DNS result not cached")
	}
	// Once the list is loaded it is used instead of DNS.
	c.SetRanges("Googlebot", []netip.Prefix{netip.MustParsePrefix("66.249.64.0/19")})
	if got := c.Classify(req(googleUA), net.ParseIP("66.249.66.2"), "https"); got.Kind != Verified || got.Method != "ip-list" {
		t.Fatalf("got %v", got)
	}
}

func TestUncheckableClaimIsUnknown(t *testing.T) {
	c := New(bots, nil)
	c.resolver = &fakeDNS{fail: true}
	if got := c.Classify(req(googleUA), net.ParseIP("66.249.66.1"), "https"); got.Kind != Unknown || !strings.Contains(got.Note, "not checked") {
		t.Fatalf("DNS failure: got %v", got)
	}
	if got := c.Classify(req("GPTBot"), net.ParseIP("20.0.0.1"), "https"); got.Kind != Unknown {
		t.Fatalf("list not loaded: got %v", got)
	}
}

func TestDNSConcurrencyCap(t *testing.T) {
	c := New(bots, nil)
	c.resolver = &fakeDNS{}
	for i := 0; i < maxDNSInFlight; i++ {
		c.dnsSem <- struct{}{}
	}
	if got := c.Classify(req(googleUA), net.ParseIP("66.249.66.1"), "https"); got.Kind != Unknown {
		t.Fatalf("got %v", got)
	}
}

func TestDNSCacheBounded(t *testing.T) {
	c := New(bots, nil)
	c.resolver = &fakeDNS{}
	for i := 0; i < maxDNSCache+100; i++ {
		ip := net.IPv4(10, byte(i>>16), byte(i>>8), byte(i))
		c.Classify(req(googleUA), ip, "https")
	}
	if n := len(c.dnsCache); n > maxDNSCache {
		t.Fatalf("cache has %d entries", n)
	}
}

func TestParseRanges(t *testing.T) {
	p, err := parseRanges([]byte(`{"creationTime":"x","prefixes":[{"ipv4Prefix":"1.2.3.0/24"},{"ipv6Prefix":"2001:db8::/32"},{"ipv4Prefix":"bad"}]}`))
	if err != nil || len(p) != 2 {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := parseRanges([]byte(`{"prefixes":[]}`)); err == nil {
		t.Fatal("empty list accepted")
	}
}

func TestFetchRangesLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			w.Write([]byte(`{"prefixes":[],"pad":"` + strings.Repeat("x", maxRangesBytes) + `"}`))
			return
		}
		w.Write([]byte(`{"prefixes":[{"ipv4Prefix":"1.2.3.0/24"}]}`))
	}))
	defer srv.Close()
	c := New(nil, nil)
	if p, err := c.fetchRanges(context.Background(), srv.URL+"/ok"); err != nil || len(p) != 1 {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := c.fetchRanges(context.Background(), srv.URL+"/big"); err == nil {
		t.Fatal("oversized list accepted")
	}
}

func TestSignedBeatsUserAgent(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	dir := httptest.NewServer(webbotauth.DirectoryHandler(key))
	defer dir.Close()
	c := New(bots, webbotauth.NewVerifier(webbotauth.NewDirectories(true)))
	r := httptest.NewRequest(http.MethodGet, "https://origin.test/premium/a", nil)
	r.Header.Set("User-Agent", googleUA)
	(&webbotauth.Signer{Key: key, AgentURL: dir.URL + webbotauth.DirectoryPath}).Sign(r)
	if got := c.Classify(r, net.ParseIP("6.6.6.6"), "https"); got.Kind != Signed || got.Method != "web-bot-auth" {
		t.Fatalf("got %v", got)
	}
	// A broken signature falls back to the other checks, with a note.
	r = httptest.NewRequest(http.MethodGet, "https://origin.test/premium/a", nil)
	r.Header.Set("User-Agent", "curl")
	r.Header.Set("Signature-Input", `sig1=("@authority");created=1;expires=2;keyid="k";tag="web-bot-auth"`)
	r.Header.Set("Signature", "sig1=:AAAA:")
	if got := c.Classify(r, net.ParseIP("6.6.6.6"), "https"); got.Kind != Unknown || !strings.Contains(got.Note, "signature invalid") {
		t.Fatalf("got %v", got)
	}
}

func TestClassString(t *testing.T) {
	got := Class{Kind: Verified, Name: "GPTBot", Method: "ip-list"}.String()
	if got != `verified; name="GPTBot"; method=ip-list` {
		t.Fatalf("got %s", got)
	}
}
