package gate

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The paywall's own invoices: a node that repeats a payment hash must not
// let one preimage open two pages, and a lookup reporting less than the
// price is not a payment. Both checks predate login payments.
func TestPaywallRepeatedHashAndUnderpaid(t *testing.T) {
	f := newFixture(t)
	f.gate.node.backend = &sameHashNode{Fake: f.ln}
	a, b := f.challenge(t, "/premium/a"), f.challenge(t, "/premium/b")
	if a.L402.Invoice != b.L402.Invoice {
		t.Fatal("mock node should repeat the invoice")
	}
	auth := f.pay(t, a)
	_, pre, _ := strings.Cut(strings.TrimPrefix(auth, "L402 "), ":")
	if rec := f.get("/premium/a", auth, ""); rec.Code != http.StatusOK {
		t.Fatalf("first page: %d", rec.Code)
	}
	if rec := f.get("/premium/b", "L402 "+b.L402.Token+":"+pre, ""); rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), errReplay.Error()) {
		t.Fatalf("same hash, second page: %d %s", rec.Code, rec.Body)
	}
	f.now = f.now.Add(lookupUnpaidTTL + time.Millisecond)
	if code, body := claimStatus(t, f.claim(b.L402.Token, redeemCookie)); code != http.StatusConflict {
		t.Fatalf("same hash, claim for the second page: %d %+v", code, body)
	}

	f = newFixture(t)
	f.gate.invoices.lookup = underpayingNode{f.ln}
	ch := f.challenge(t, "/premium/a")
	f.pay(t, ch)
	for _, redeem := range []string{"", redeemCookie} {
		if code, body := claimStatus(t, f.claim(ch.L402.Token, redeem)); code != http.StatusPaymentRequired || body.Status != "underpaid" || body.Preimage != "" {
			t.Fatalf("underpaid claim (%q): %d %+v", redeem, code, body)
		}
	}
}

// Login lookups may take at most lookupLoginPerMin of the node lookups a
// minute; the rest stays for the paywall.
func TestLoginLookupsLeaveRoomForThePaywall(t *testing.T) {
	f := newFixture(t)
	c := f.gate.invoices
	for i := range lookupLoginPerMin {
		if _, err := c.getFor(fmt.Sprintf("%064x", i), f.now, f.now, true); err == errLookupBusy {
			t.Fatalf("login lookup %d refused", i)
		}
	}
	if _, err := c.getFor(strings.Repeat("a", 64), f.now, f.now, true); err != errLookupBusy {
		t.Fatalf("login lookup over its share: %v", err)
	}
	if _, err := c.get(strings.Repeat("b", 64), f.now, f.now); err == errLookupBusy {
		t.Fatal("paywall lookup refused while logins used their share")
	}
	f.now = f.now.Add(time.Minute)
	if _, err := c.getFor(strings.Repeat("a", 64), f.now, f.now, true); err == errLookupBusy {
		t.Fatal("new minute: login lookup still refused")
	}
}
