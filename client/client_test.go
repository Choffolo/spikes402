package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/gate"
	"github.com/Choffolo/spikes402/internal/lightning"
)

// newProxy starts a gate with the fake backend in front of a dummy origin.
func newProxy(t *testing.T, difficulty int) *httptest.Server {
	t.Helper()
	fake := lightning.NewFake()
	mux := http.NewServeMux()
	mux.Handle(FakePayPath, fake.PayHandler())
	mux.Handle("/", gate.New(gate.Options{
		PriceSats:     10,
		Protect:       []string{"/premium/**"},
		PowDifficulty: difficulty,
		ChallengeTTL:  time.Minute,
		SecretKey:     []byte(strings.Repeat("s", 32)),
		Lightning:     fake,
		Next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "secret article")
		}),
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
