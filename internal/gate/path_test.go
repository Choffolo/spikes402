package gate

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rawGet sends a request line exactly as written, like an attacker would.
func (f *fixture) rawGet(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader("GET " + target + " HTTP/1.1\r\nHost: x\r\n\r\n")))
	if err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	req.RemoteAddr = "192.0.2.1:1"
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	return rec
}

// Every spelling of a protected path found in the security review must
// either be asked to pay (402) or be refused (400), never reach the origin.
func TestProtectedPathVariants(t *testing.T) {
	f := newFixture(t) // protects /premium/** and /*.pdf
	for _, target := range []string{
		"/premium/article", "/PREMIUM/article", "/Premium/article", "/premium/", "/premium", "/premium?x=1",
		"//premium/article", "/premium/./article", "/premium/../premium/article", "/x/../premium/article",
		"/premium%2farticle", "/premium%2Farticle", "/x/..%2fpremium/article", "/%70remium/article",
		"/x/%2e%2e/premium/article", "/x/..;/premium/article", "/premium;x/article", "/premium%3bx/article",
		"/premium;jsessionid=1", "/x/%252e%252e/premium/article", "/premium%2500/article",
		"/premium%00/article", "/premium/article%00", "/premium%20/article", "/premium%09/article",
		"/premium./article", "/premium%2e/article", "/premium\\article", "/x\\..\\premium\\article",
		"/premium%5carticle", "/premium%5Carticle", "/premium%0d%0a/article",
		"/a.pdf", "/A.PDF", "/a.pdf/", "/a.pdf;.html", "/a.pdf.", "/a.pdf%20", "/a%2epdf",
	} {
		rec := f.rawGet(t, target)
		if rec.Code != http.StatusPaymentRequired && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %q, want 402 or 400", target, rec.Code, rec.Body.String())
		}
	}
	// Free pages still pass, including harmless encodings.
	for _, target := range []string{"/", "/free", "/blog/post%20one", "/premiumx", "/caf%C3%A9", "/a.pdf.html", "/docs/"} {
		if rec := f.rawGet(t, target); rec.Code != http.StatusOK {
			t.Errorf("%s: got %d %q, want 200", target, rec.Code, rec.Body.String())
		}
	}
}

func TestCaseSensitiveOption(t *testing.T) {
	f := newFixture(t)
	opt := f.gate.opt
	opt.CaseSensitive = true
	f.gate = New(opt)
	if rec := f.rawGet(t, "/PREMIUM/article"); rec.Code != http.StatusOK {
		t.Fatalf("case-sensitive: got %d", rec.Code)
	}
	if rec := f.rawGet(t, "/premium;x/article"); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("path parameters must still be ignored for matching: got %d", rec.Code)
	}
}

func TestLongURIs(t *testing.T) {
	f := newFixture(t)
	if rec := f.rawGet(t, "/premium/a?"+strings.Repeat("x", MaxURIBytes)); rec.Code != http.StatusRequestURITooLong {
		t.Fatalf("got %d, want 414", rec.Code)
	}
	if rec := f.rawGet(t, "/free?"+strings.Repeat("x", MaxURIBytes)); rec.Code != http.StatusRequestURITooLong {
		t.Fatalf("free page: got %d, want 414", rec.Code)
	}
	// Just under the limit: the token and the stored offer do not grow with the URL.
	short := f.challenge(t, "/premium/a?q=1")
	long := f.challenge(t, "/premium/a?q="+strings.Repeat("x", MaxURIBytes-100))
	if d := len(long.PoW.Challenge) - len(short.PoW.Challenge); d > 8 || d < -8 {
		t.Fatalf("token grows with the URL: %d vs %d bytes", len(long.PoW.Challenge), len(short.PoW.Challenge))
	}
	for k := range f.gate.pending.offers {
		if len(k) > 200 {
			t.Fatalf("stored offer key is %d bytes", len(k))
		}
	}
	// And it still unlocks the long URL, and only it.
	target := "/premium/a?q=" + strings.Repeat("x", MaxURIBytes-100)
	if rec := f.get(target, f.pay(t, long), ""); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestMatchKey(t *testing.T) {
	for in, want := range map[string]string{
		"/Premium/Article/": "/premium/article", "/x/..;/premium": "/premium", "/premium;a=b/x": "/premium/x",
		"/a.pdf. ": "/a.pdf", "//a///b": "/a/b", "/..": "/", "/": "/",
	} {
		if got := matchKey(in, false); got != want {
			t.Errorf("matchKey(%q) = %q, want %q", in, got, want)
		}
	}
	if got := matchKey("/Premium", true); got != "/Premium" {
		t.Errorf("case-sensitive: %q", got)
	}
}
