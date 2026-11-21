package gate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProtectedCacheControl(t *testing.T) {
	f := newFixture(t)
	f.gate.opt.Next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, s-maxage=3600")
		w.Write([]byte("content of " + r.URL.Path))
	})
	const want = "private, no-store"

	rec := f.get("/premium/article", "", "")
	if rec.Code != http.StatusPaymentRequired || rec.Header().Get("Cache-Control") != want {
		t.Fatalf("402 JSON: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	rec = f.get("/premium/article", "", "text/html")
	if rec.Code != http.StatusPaymentRequired || rec.Header().Get("Cache-Control") != want {
		t.Fatalf("402 HTML: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	paid := f.pay(t, f.challenge(t, "/premium/paid"))
	rec = f.get("/premium/paid", paid, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != want {
		t.Fatalf("paid: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	work := solve(t, f.challenge(t, "/premium/pow"))
	rec = f.get("/premium/pow", work, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != want {
		t.Fatalf("PoW: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	s := f.gate.Settings()
	s.Paused = true
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	rec = f.get("/premium/paused", "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != want {
		t.Fatalf("paused: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	s.Paused = false
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}

	f.setPolicies(map[string]Policy{"unknown": {Action: ActionFree}})
	rec = f.get("/premium/free-policy", "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != want {
		t.Fatalf("free policy: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	f.setPolicies(nil)

	// A pass is issued as 204; the following navigation is a protected origin answer.
	b := f.challenge(t, "/premium/pass")
	auth := f.pay(t, b)
	req := httptest.NewRequest(http.MethodGet, "/premium/pass", nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set(RedeemHeader, redeemCookie)
	rec = httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Cache-Control") != want {
		t.Fatalf("pass issue: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	pass := cookieNamed(rec, passPrefix)
	if pass == nil {
		t.Fatal("no pass cookie")
	}
	req = httptest.NewRequest(http.MethodGet, "/premium/pass", nil)
	req.AddCookie(pass)
	rec = httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != want {
		t.Fatalf("pass redeem: %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	rec = f.get("/blog/post", "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, s-maxage=3600" {
		t.Fatalf("unprotected: %d Cache-Control %q (origin caching must be left alone)", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

func TestTokenNotBoundToRequestHost(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodGet, "http://www.example.com/premium/a", nil)
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("challenge: got %d", rec.Code)
	}
	b := decode(t, rec)
	auth := f.pay(t, b)

	req = httptest.NewRequest(http.MethodGet, "http://evil.example/premium/a", nil)
	req.Header.Set("Authorization", auth)
	rec = httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem with a different Host: got %d %q (tokens bind to method+path, not Host)", rec.Code, rec.Body.String())
	}
}
