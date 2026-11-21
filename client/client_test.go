package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/botid"
	"github.com/Choffolo/spikes402/internal/gate"
	"github.com/Choffolo/spikes402/internal/lightning"
	"github.com/Choffolo/spikes402/pow"
	"github.com/Choffolo/spikes402/webbotauth"
)

// newProxy starts a gate with the fake backend in front of a dummy origin.
func newProxy(t *testing.T, difficulty int) *httptest.Server {
	t.Helper()
	return newProxyPoW(t, pow.Params{Algorithm: pow.SHA256d, Difficulty: difficulty}, nil)
}

func newProxyPoW(t *testing.T, p pow.Params, classifier *botid.Classifier) *httptest.Server {
	t.Helper()
	fake := lightning.NewFake()
	mux := http.NewServeMux()
	mux.Handle(FakePayPath, fake.PayHandler())
	mux.Handle("/", gate.New(gate.Options{
		PriceSats:    10,
		Protect:      []string{"/premium/**"},
		PoW:          func(int64) pow.Params { return p },
		Classifier:   classifier,
		ChallengeTTL: time.Minute,
		SecretKey:    []byte(strings.Repeat("s", 32)),
		Lightning:    fake,
		Next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "secret article")
			if r.Header.Get("Signature") != "" {
				io.WriteString(w, " (signed)")
			}
		}),
		ChallengesPerMinute:  100,
		MaxInvoicesPerMinute: 100,
		MaxPendingInvoices:   100,
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fetch(t *testing.T, c *Client, url string) string {
	t.Helper()
	resp, err := c.Get(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

func TestBotSolvesPoW(t *testing.T) {
	srv := newProxy(t, 10)
	if got := fetch(t, &Client{Mode: ModePoW}, srv.URL+"/premium/a"); got != "secret article" {
		t.Fatalf("got %q", got)
	}
}

func TestBotPays(t *testing.T) {
	srv := newProxy(t, 10)
	c := &Client{Mode: ModePay, Payer: FakePayer{ProxyURL: srv.URL}}
	if got := fetch(t, c, srv.URL+"/premium/a"); got != "secret article" {
		t.Fatalf("got %q", got)
	}
}

func TestBotRefusesHardPuzzle(t *testing.T) {
	srv := newProxy(t, 30)
	if _, err := (&Client{Mode: ModePoW, MaxDifficulty: 20}).Get(context.Background(), srv.URL+"/premium/a"); err == nil {
		t.Fatal("expected the bot to refuse")
	}
}

func TestBotFreePage(t *testing.T) {
	srv := newProxy(t, 10)
	if got := fetch(t, &Client{Mode: ModePoW}, srv.URL+"/free"); got != "secret article" {
		t.Fatalf("got %q", got)
	}
}

func TestParseChallenges(t *testing.T) {
	h := http.Header{}
	h.Add("WWW-Authenticate", `L402 macaroon="abc.def", invoice="lnbc1"`)
	h.Add("WWW-Authenticate", `PoW challenge="abc.def", difficulty="20", algorithm="sha256d"`)
	cs := ParseChallenges(h)
	if len(cs) != 2 || cs[0].Params["invoice"] != "lnbc1" || cs[1].Params["difficulty"] != "20" {
		t.Fatalf("got %+v", cs)
	}
}

func TestBotSolvesArgon2id(t *testing.T) {
	srv := newProxyPoW(t, pow.Params{Algorithm: pow.Argon2id, Difficulty: 2, MemoryMiB: 8}, nil)
	if got := fetch(t, &Client{Mode: ModePoW}, srv.URL+"/premium/a"); got != "secret article" {
		t.Fatalf("got %q", got)
	}
}

func TestBotArgonLimits(t *testing.T) {
	srv := newProxyPoW(t, pow.Params{Algorithm: pow.Argon2id, Difficulty: 2, MemoryMiB: 128}, nil)
	_, err := (&Client{Mode: ModePoW}).Get(context.Background(), srv.URL+"/premium/a")
	if err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("got %v", err)
	}
	if _, err := (&Client{Mode: ModePoW, MaxArgonMemoryMiB: 128, MaxArgonDifficulty: 1}).Get(context.Background(), srv.URL+"/premium/a"); err == nil {
		t.Fatal("difficulty limit ignored")
	}
}

func TestPuzzleParsing(t *testing.T) {
	c := &Client{}
	for _, params := range []map[string]string{
		{"difficulty": "10", "algorithm": "md5"},
		{"difficulty": "x", "algorithm": "sha256d"},
		{"difficulty": "10", "algorithm": "argon2id"},                     // no memory
		{"difficulty": "10", "algorithm": "argon2id", "memory-mib": "1"},  // below the minimum
		{"difficulty": "-1", "algorithm": "argon2id", "memory-mib": "16"}, // negative
		{"difficulty": "29", "algorithm": "sha256d"},                      // over the default limit
		{"difficulty": "15", "algorithm": "argon2id", "memory-mib": "16"}, // over the default limit
	} {
		if _, err := c.puzzle(Challenge{Scheme: "PoW", Params: params}); err == nil {
			t.Errorf("%v accepted", params)
		}
	}
}

func TestBotSignsRequests(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	dir := httptest.NewServer(webbotauth.DirectoryHandler(key))
	defer dir.Close()
	classifier := botid.New(nil, webbotauth.NewVerifier(webbotauth.NewDirectories(true)))
	classifier.TrustedAgents = []string{"127.0.0.1"} // where the test directory is served
	srv := newProxyPoW(t, pow.Params{Algorithm: pow.SHA256d, Difficulty: 8}, classifier)

	agent := dir.URL + webbotauth.DirectoryPath
	c := &Client{Mode: ModePoW, Signer: &webbotauth.Signer{Key: key, AgentURL: agent}}
	resp, err := c.Get(context.Background(), srv.URL+"/premium/a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "secret article (signed)" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get(gate.ClassHeader); got != `verified-signed; name="`+agent+`"; method=web-bot-auth` {
		t.Fatalf("class %q", got)
	}
}

func TestBotDoesNotLogPreimage(t *testing.T) {
	srv := newProxy(t, 10)
	var buf bytes.Buffer
	c := &Client{
		Mode:  ModePay,
		Payer: FakePayer{ProxyURL: srv.URL},
		Logf:  func(format string, args ...any) { fmt.Fprintf(&buf, format+"\n", args...) },
	}
	if got := fetch(t, c, srv.URL+"/premium/a"); got != "secret article" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(buf.String(), "preimage") {
		t.Fatalf("log leaked preimage: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "402: paid") {
		t.Fatalf("expected a paid log, got %s", buf.String())
	}
}

func TestBotRetriesFinalURLAfterRedirect(t *testing.T) {
	gate := newProxy(t, 10)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, gate.URL+"/premium/a", http.StatusFound)
	}))
	t.Cleanup(front.Close)
	if got := fetch(t, &Client{Mode: ModePoW}, front.URL+"/go"); got != "secret article" {
		t.Fatalf("got %q", got)
	}
}

func TestBotRefusesCrossHostRedirectWithProof(t *testing.T) {
	gate := newProxy(t, 10)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "leaked")
	}))
	t.Cleanup(other.Close)
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Redirect(w, r, other.URL+"/stolen", http.StatusFound)
			return
		}
		req, err := http.NewRequest(r.Method, gate.URL+"/premium/a", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			w.Header()[k] = vs
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(wrap.Close)
	_, err := (&Client{Mode: ModePoW}).Get(context.Background(), wrap.URL+"/premium/a")
	if err == nil || !strings.Contains(err.Error(), "different host") {
		t.Fatalf("got %v, want a cross-host redirect refusal", err)
	}
}
