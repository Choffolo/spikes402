package gate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// BenchmarkChallengeHTML is the 402 HTML path when the same invoice is shown
// again (same client, session cookie kept — the usual retry or refresh).
func BenchmarkChallengeHTML(b *testing.B) {
	f := newFixture(b)
	f.gate.limiter = newRateLimiter(1_000_000_000)
	req := httptest.NewRequest(http.MethodGet, "/premium/article", nil)
	req.Header.Set("Accept", "text/html")
	warm := httptest.NewRecorder()
	f.gate.ServeHTTP(warm, req)
	if warm.Code != http.StatusPaymentRequired {
		b.Fatalf("warmup: %d", warm.Code)
	}
	cookies := warm.Result().Cookies()
	if len(cookies) == 0 {
		b.Fatal("warmup set no session cookie")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest(http.MethodGet, "/premium/article", nil)
		r.Header.Set("Accept", "text/html")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		f.gate.ServeHTTP(rec, r)
		if rec.Code != http.StatusPaymentRequired {
			b.Fatalf("got %d", rec.Code)
		}
	}
}
