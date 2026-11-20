package webbotauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// agentServer publishes a key directory and counts fetches.
type agentServer struct {
	*httptest.Server
	key   ed25519.PrivateKey
	hits  atomic.Int64
	down  atomic.Bool
	extra func(w http.ResponseWriter) bool // optional override
}

func newAgent(t *testing.T) *agentServer {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	a := &agentServer{key: key}
	dir := DirectoryHandler(key)
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.hits.Add(1)
		if a.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if a.extra != nil && a.extra(w) {
			return
		}
		dir.ServeHTTP(w, r)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *agentServer) url() string { return a.URL + DirectoryPath }

func (a *agentServer) signed(t *testing.T, target string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	s := &Signer{Key: a.key, AgentURL: a.url()}
	if err := s.Sign(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func newDevVerifier() *Verifier { return NewVerifier(NewDirectories(true)) }

func TestVerifySignedRequest(t *testing.T) {
	a := newAgent(t)
	v := newDevVerifier()
	r := a.signed(t, "https://origin.test/premium/a")
	res := v.Verify(r, "https")
	if res.Outcome != Verified || res.Agent != strings.ToLower(a.url()) || res.KeyID != Thumbprint(a.key.Public().(ed25519.PublicKey)) {
		t.Fatalf("got %+v", res)
	}
	// Second request from the same agent: served from cache.
	if res := v.Verify(a.signed(t, "https://origin.test/premium/b"), "https"); res.Outcome != Verified || a.hits.Load() != 1 {
		t.Fatalf("got %+v, %d fetches", res, a.hits.Load())
	}
}

func TestVerifyRejects(t *testing.T) {
	a := newAgent(t)
	v := newDevVerifier()
	cases := map[string]func(r *http.Request){
		"tampered path": func(r *http.Request) { r.URL.Path = "/premium/other" },
		"other host":    func(r *http.Request) { r.Host = "evil.test" },
		"bad signature": func(r *http.Request) {
			r.Header.Set("Signature", "sig1=:"+strings.Repeat("A", 86)+"==:")
		},
		"agent not covered": func(r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), ` "signature-agent";key="sig1"`, "", 1))
		},
		"no authority": func(r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), ` "@authority"`, "", 1))
		},
		"no expires": func(r *http.Request) {
			in := r.Header.Get("Signature-Input")
			i := strings.Index(in, ";expires=")
			j := strings.Index(in[i+1:], ";") + i + 1
			r.Header.Set("Signature-Input", in[:i]+in[j:])
		},
		"malformed": func(r *http.Request) { r.Header.Set("Signature-Input", "sig1=(") },
	}
	for name, mutate := range cases {
		r := a.signed(t, "https://origin.test/premium/a")
		mutate(r)
		if res := v.Verify(r, "https"); res.Outcome != Invalid {
			t.Errorf("%s: got %+v", name, res)
		}
	}
}

func TestVerifyReplay(t *testing.T) {
	a := newAgent(t)
	v := newDevVerifier()
	r := a.signed(t, "https://origin.test/premium/a")
	if res := v.Verify(r, "https"); res.Outcome != Verified {
		t.Fatalf("first: %+v", res)
	}
	if res := v.Verify(r, "https"); res.Outcome != Invalid || !strings.Contains(res.Reason, "replay") {
		t.Fatalf("replay: %+v", res)
	}
}

func TestVerifyTimeWindow(t *testing.T) {
	a := newAgent(t)
	now := time.Now()
	for name, c := range map[string]struct {
		signedAt time.Time
		lifetime time.Duration
	}{
		"expired":  {now.Add(-2 * time.Minute), time.Minute},
		"future":   {now.Add(5 * time.Minute), time.Minute},
		"too long": {now, 2 * time.Hour},
	} {
		v := newDevVerifier()
		r := httptest.NewRequest(http.MethodGet, "https://origin.test/x", nil)
		s := &Signer{Key: a.key, AgentURL: a.url(), Lifetime: c.lifetime, now: func() time.Time { return c.signedAt }}
		s.Sign(r)
		if res := v.Verify(r, "https"); res.Outcome != Invalid {
			t.Errorf("%s: got %+v", name, res)
		}
	}
}

func TestVerifyNeedsHTTPS(t *testing.T) {
	a := newAgent(t)
	v := NewVerifier(NewDirectories(false))
	if res := v.Verify(a.signed(t, "https://origin.test/x"), "http"); res.Outcome != Unverified {
		t.Fatalf("got %+v", res)
	}
	if res := v.Verify(httptest.NewRequest(http.MethodGet, "/x", nil), "https"); res.Outcome != None {
		t.Fatalf("unsigned: got %+v", res)
	}
}

func TestUnsupportedComponentIsUnverified(t *testing.T) {
	a := newAgent(t)
	v := newDevVerifier()
	r := a.signed(t, "https://origin.test/x")
	r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), `"@path"`, `"@query-param";name="a"`, 1))
	if res := v.Verify(r, "https"); res.Outcome != Unverified {
		t.Fatalf("got %+v", res)
	}
}

func TestUnknownKeyAndDirectoryDown(t *testing.T) {
	a := newAgent(t)
	v := newDevVerifier()
	// A key that the directory does not publish.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	r := httptest.NewRequest(http.MethodGet, "https://origin.test/x", nil)
	(&Signer{Key: other, AgentURL: a.url()}).Sign(r)
	if res := v.Verify(r, "https"); res.Outcome != Unverified {
		t.Fatalf("unknown key: %+v", res)
	}

	// Directory down: unverified, and not refetched for every request.
	b := newAgent(t)
	b.down.Store(true)
	for i := 0; i < 3; i++ {
		if res := v.Verify(b.signed(t, "https://origin.test/x"), "https"); res.Outcome != Unverified {
			t.Fatalf("down: %+v", res)
		}
	}
	if b.hits.Load() != 1 {
		t.Fatalf("directory fetched %d times, want 1 (negative cache)", b.hits.Load())
	}
}

func TestStaleKeysSurviveFailedRefresh(t *testing.T) {
	a := newAgent(t)
	v := newDevVerifier()
	now := time.Now()
	v.now = func() time.Time { return now }
	v.Dirs.now = func() time.Time { return now }
	if res := v.Verify(a.signed(t, "https://origin.test/x"), "https"); res.Outcome != Verified {
		t.Fatalf("got %+v", res)
	}
	a.down.Store(true)
	now = now.Add(2 * time.Hour) // past max-age (1 h)
	r := httptest.NewRequest(http.MethodGet, "https://origin.test/y", nil)
	(&Signer{Key: a.key, AgentURL: a.url(), now: func() time.Time { return now }}).Sign(r)
	if res := v.Verify(r, "https"); res.Outcome != Verified {
		t.Fatalf("a failed refresh must not evict keys: %+v", res)
	}
}

func TestDirectoryRules(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter) bool{
		"redirect": func(w http.ResponseWriter) bool {
			w.Header().Set("Location", "https://elsewhere.test/")
			w.WriteHeader(http.StatusFound)
			return true
		},
		"wrong media type": func(w http.ResponseWriter) bool {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(`{"keys":[]}`))
			return true
		},
		"too large": func(w http.ResponseWriter) bool {
			w.Header().Set("Content-Type", DirectoryMediaType)
			w.Write([]byte(`{"keys":[],"pad":"` + strings.Repeat("x", maxDirectoryBytes) + `"}`))
			return true
		},
	}
	for name, override := range cases {
		a := newAgent(t)
		a.extra = override
		v := newDevVerifier()
		if res := v.Verify(a.signed(t, "https://origin.test/x"), "https"); res.Outcome != Unverified {
			t.Errorf("%s: got %+v", name, res)
		}
	}
}

func TestParseDirectorySkipsBadKeys(t *testing.T) {
	_, k, _ := ed25519.GenerateKey(rand.Reader)
	x := base64.RawURLEncoding.EncodeToString(k.Public().(ed25519.PublicKey))
	body := `{"keys":[
		{"kty":"RSA","n":"x","e":"AQAB"},
		{"kty":"OKP","crv":"Ed25519","x":"short"},
		{"kty":"OKP","crv":"Ed25519","x":"JrQLj5P_89iXES9-vFgrIy29clF9CC_oPPsw3c5D0bs"},
		{"kty":"OKP","crv":"Ed25519","kid":"wrong-kid","x":"` + x + `"},
		{"kty":"OKP","crv":"Ed25519","use":"enc","x":"` + x + `"},
		{"kty":"OKP","crv":"Ed25519","kid":"` + Thumbprint(k.Public().(ed25519.PublicKey)) + `","x":"` + x + `","use":"sig"}]}`
	keys, err := parseDirectory([]byte(body), true)
	if err != nil || len(keys) != 1 {
		t.Fatalf("got %d keys, %v", len(keys), err)
	}
	// At another URL, kid is an operator label and is not checked.
	keys, _ = parseDirectory([]byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"label","x":"`+x+`"}]}`), false)
	if len(keys) != 1 {
		t.Fatal("kid must not be enforced outside the well-known path")
	}
	if _, err := parseDirectory([]byte(`{"nokeys":1}`), true); err == nil {
		t.Fatal("not a JWK Set")
	}
}

func TestKeyValidityWindow(t *testing.T) {
	_, k, _ := ed25519.GenerateKey(rand.Reader)
	x := base64.RawURLEncoding.EncodeToString(k.Public().(ed25519.PublicKey))
	keys, _ := parseDirectory([]byte(`{"keys":[{"kty":"OKP","crv":"Ed25519","x":"`+x+`","nbf":1000,"exp":2000}]}`), false)
	e := &dirEntry{keys: keys}
	tp := Thumbprint(k.Public().(ed25519.PublicKey))
	for at, ok := range map[int64]bool{999: false, 1000: true, 1999: true, 2000: false} {
		if _, err := e.key(tp, time.Unix(at, 0)); (err == nil) != ok {
			t.Errorf("at %d: got %v", at, err)
		}
	}
}

func TestFetchesAreCoalescedAndBudgeted(t *testing.T) {
	a := newAgent(t)
	v := newDevVerifier()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); v.Verify(a.signed(t, "https://origin.test/x"), "https") }()
	}
	wg.Wait()
	if a.hits.Load() != 1 {
		t.Fatalf("%d fetches for one directory, want 1", a.hits.Load())
	}

	// Many different directories: only the burst budget is fetched.
	d := NewDirectories(true)
	for i := 0; i < 3*fetchBurst; i++ {
		d.lookup(a.URL+"/dir"+string(rune('a'+i)), "k", 0)
	}
	if n := a.hits.Load() - 1; n > fetchBurst {
		t.Fatalf("%d fetches, budget is %d", n, fetchBurst)
	}
}

func TestSSRFGuards(t *testing.T) {
	d := NewDirectories(false)
	for _, addr := range []string{"127.0.0.1:443", "10.1.2.3:443", "192.168.1.1:443", "169.254.169.254:443", "[::1]:443", "[fd00::1]:443", "100.64.0.1:443", "0.0.0.0:443"} {
		if d.checkAddress("tcp", addr, nil) == nil {
			t.Errorf("%s should be refused", addr)
		}
	}
	if err := d.checkAddress("tcp", net.JoinHostPort("8.8.8.8", "443"), nil); err != nil {
		t.Errorf("public address refused: %v", err)
	}
	for _, u := range []string{"http://agent.test/x", "https://agent.test:8443/x", "https://user@agent.test/", "/relative", "ftp://agent.test"} {
		if _, err := d.identifier(u); err == nil {
			t.Errorf("%s should be refused", u)
		}
	}
	if id, err := d.identifier("HTTPS://Agent.Test:443/.well-known/http-message-signatures-directory?x=1#f"); err != nil || id != "https://agent.test/.well-known/http-message-signatures-directory" {
		t.Errorf("normalization: %q %v", id, err)
	}
}

func TestReplayCacheFailsClosed(t *testing.T) {
	v := newDevVerifier()
	far := time.Now().Add(time.Hour)
	for i := 0; i < maxReplayEntries; i++ {
		var k [32]byte
		k[0], k[1], k[2] = byte(i), byte(i>>8), byte(i>>16)
		v.seen[k] = far
	}
	if err := v.remember([32]byte{0xff, 0xff, 0xff, 0xff}, far); err != errFull {
		t.Fatalf("got %v", err)
	}
}
