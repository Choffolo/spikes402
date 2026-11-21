package gate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQRIsStoredWithTheOffer(t *testing.T) {
	f := newFixture(t)
	f.gate.limiter = newRateLimiter(1000)
	rec := f.get("/premium/a", "", "text/html")
	if rec.Code != 402 {
		t.Fatal(rec.Code)
	}
	html1 := rec.Body.String()
	if !strings.Contains(html1, "data:image/png;base64,") {
		t.Fatal("missing QR")
	}
	src := html1[strings.Index(html1, "data:image/png;base64,"):]
	src = src[:strings.Index(src, `"`)]
	if len(src) < 100 {
		t.Fatalf("QR too short: %d", len(src))
	}
	// same session, same invoice: the page must reuse the stored QR
	rec2 := httptestCookieGet(t, f, "/premium/a", rec.Result().Cookies())
	if rec2.Code != 402 {
		t.Fatal(rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), src) {
		t.Fatal("second page used a different QR")
	}
}

func httptestCookieGet(t *testing.T, f *fixture, path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept", "text/html")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	return rec
}

func TestArgonBudgetRejectsOversizedCheck(t *testing.T) {
	b := newArgonBudget(16)
	if b.acquire(32, 0) {
		t.Fatal("a 32 MiB check must not run under a 16 MiB budget")
	}
	if !b.acquire(16, 0) {
		t.Fatal("a 16 MiB check should fit")
	}
	if b.acquire(16, 0) {
		t.Fatal("budget already used")
	}
	b.release(16)
	if !b.acquire(16, 0) {
		t.Fatal("after release")
	}
}

func TestArgonBudgetWaitsThenRefuses(t *testing.T) {
	b := newArgonBudget(8)
	if !b.acquire(8, 0) {
		t.Fatal("hold")
	}
	started := time.Now()
	if b.acquire(8, 30*time.Millisecond) {
		t.Fatal("should time out")
	}
	if d := time.Since(started); d < 30*time.Millisecond || d > 200*time.Millisecond {
		t.Fatalf("waited %s", d)
	}
}
