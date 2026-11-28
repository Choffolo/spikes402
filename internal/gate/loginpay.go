package gate

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Choffolo/spikes402/internal/lightning"
)

// Paying instead of the login proof-of-work. A Lightning payment can
// replace the computation of one attempt, nothing else: the pass it buys
// is the same one-use login pass a proof buys, bound to the same account,
// network and level, and the login itself must still succeed at the
// origin. A paid attempt counts like any other, so failures still raise
// the level and the price. See "Paying instead" in the README.
const (
	LoginInvoicePath = ReservedPrefix + "login-invoice"

	// An invoice lives briefly: long enough to pay from a phone, short
	// enough that the level it was priced at is rarely stale by then.
	loginInvoiceTTL = 3 * time.Minute
	// The token outlives its invoice a little, so a payment made in the
	// invoice's last seconds can still be redeemed.
	loginPayGrace    = time.Minute
	loginInvoiceMemo = "Login attempt"
	maxLoginBaseSats = 100_000
	maxLoginMaxSats  = 1_000_000
)

// LoginPrice turns a login level into sats: BaseSats at LoginBaseLevel,
// doubling with each level above it, at most MaxSats (0: no cap but the
// top level's). The price depends on nothing but the level, so it tells
// no more than the level does.
type LoginPrice struct {
	BaseSats int64 `json:"base_sats"`
	MaxSats  int64 `json:"max_sats,omitempty"`
}

// LoginInvoiceLimits bound the invoices login payments may ask the node
// for. Beyond any of them, no invoice: the client solves the
// proof-of-work instead.
type LoginInvoiceLimits struct {
	AccountOpen      int `json:"account_open"`       // unpaid, unexpired invoices per account
	AccountPerMinute int `json:"account_per_minute"` // new invoices per account per minute
	NetworkPerMinute int `json:"network_per_minute"` // per /24 or /48 per minute
	PerMinute        int `json:"per_minute"`         // all login invoices per minute
	Open             int `json:"open"`               // all unpaid, unexpired login invoices
}

// DefaultLoginInvoiceLimits are used for every limit left at 0.
var DefaultLoginInvoiceLimits = LoginInvoiceLimits{AccountOpen: 2, AccountPerMinute: 6, NetworkPerMinute: 20, PerMinute: 60, Open: 500}

// CheckLoginPrice validates a price; nil or BaseSats 0 means no paying.
func CheckLoginPrice(p *LoginPrice) error {
	if p == nil {
		return nil
	}
	if p.BaseSats < 0 || p.BaseSats > maxLoginBaseSats {
		return fmt.Errorf("base_sats must be between 0 and %d", maxLoginBaseSats)
	}
	if p.MaxSats != 0 && (p.MaxSats < p.BaseSats || p.MaxSats > maxLoginMaxSats) {
		return fmt.Errorf("max_sats must be 0 or between base_sats and %d", maxLoginMaxSats)
	}
	return nil
}

// WithDefaults fills every limit left at 0 and checks the others.
func (l LoginInvoiceLimits) WithDefaults() (LoginInvoiceLimits, error) {
	d := DefaultLoginInvoiceLimits
	for _, f := range []struct{ v, def *int }{
		{&l.AccountOpen, &d.AccountOpen}, {&l.AccountPerMinute, &d.AccountPerMinute},
		{&l.NetworkPerMinute, &d.NetworkPerMinute}, {&l.PerMinute, &d.PerMinute}, {&l.Open, &d.Open},
	} {
		if *f.v == 0 {
			*f.v = *f.def
		}
		if *f.v < 1 || *f.v > 100_000 {
			return l, errors.New(`"login_invoice_limits" values must be between 1 and 100000`)
		}
	}
	return l, nil
}

// loginPrice is the price of one attempt on route at level, or 0 if
// paying is off there (no price, or Paused: no invoices at all).
func (g *Gate) loginPrice(route *LoginRoute, level int) int64 {
	p := g.opt.LoginPrice
	if route.Price != nil {
		p = route.Price
	}
	if p == nil || p.BaseSats <= 0 || g.opt.Paused {
		return 0
	}
	sats := p.BaseSats << min(max(level-LoginBaseLevel, 0), LoginMaxLevel-LoginBaseLevel)
	if p.MaxSats > 0 {
		sats = min(sats, p.MaxSats)
	}
	return sats
}

// loginInvoices counts open login invoices and recent ones, so paying can
// never flood the node (on top of the node guard every invoice passes).
type loginInvoices struct {
	mu    sync.Mutex
	lim   LoginInvoiceLimits
	open  map[string]openLoginInvoice // token ID
	acct  map[string]int              // open invoices per account
	byMin map[string]*window          // "a "+account, "n "+network, "all"
	swept time.Time
}

type openLoginInvoice struct {
	acct    string
	expires time.Time
}

func newLoginInvoices(lim LoginInvoiceLimits) *loginInvoices {
	return &loginInvoices{lim: lim, open: map[string]openLoginInvoice{}, acct: map[string]int{}, byMin: map[string]*window{}}
}

// reserve counts a new invoice for acct from net if every limit allows
// it. Only allowed invoices are counted, so the maps stay bounded by the
// global limits (Open, and PerMinute keys a minute).
func (l *loginInvoices) reserve(id, acct, net string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	if len(l.open) >= l.lim.Open || l.acct[acct] >= l.lim.AccountOpen {
		return false
	}
	keys := []struct {
		k   string
		max int
	}{{"all", l.lim.PerMinute}, {"a " + acct, l.lim.AccountPerMinute}, {"n " + net, l.lim.NetworkPerMinute}}
	for _, k := range keys {
		if w := l.byMin[k.k]; w != nil && now.Sub(w.start) < time.Minute && w.count >= k.max {
			return false
		}
	}
	for _, k := range keys {
		w := l.byMin[k.k]
		if w == nil || now.Sub(w.start) >= time.Minute {
			w = &window{start: now}
			l.byMin[k.k] = w
		}
		w.count++
	}
	l.open[id] = openLoginInvoice{acct: acct, expires: now.Add(loginInvoiceTTL)}
	l.acct[acct]++
	return true
}

// drop forgets an invoice that was paid or never created.
func (l *loginInvoices) drop(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dropLocked(id)
}

func (l *loginInvoices) dropLocked(id string) {
	o, ok := l.open[id]
	if !ok {
		return
	}
	delete(l.open, id)
	if l.acct[o.acct]--; l.acct[o.acct] <= 0 {
		delete(l.acct, o.acct)
	}
}

func (l *loginInvoices) sweepLocked(now time.Time) {
	for id, o := range l.open {
		if !now.Before(o.expires) {
			l.dropLocked(id)
		}
	}
	if now.Sub(l.swept) >= time.Minute {
		l.swept = now
		for k, w := range l.byMin {
			if now.Sub(w.start) >= time.Minute {
				delete(l.byMin, k)
			}
		}
	}
}

type loginInvoice struct {
	Challenge string    `json:"challenge"`
	Invoice   string    `json:"invoice"`
	QR        string    `json:"qr"` // data URI (PNG)
	PriceSats int64     `json:"price_sats"`
	Level     int       `json:"level"`
	ExpiresAt time.Time `json:"expires_at"`
	Usage     string    `json:"usage"`
}

const errNoLoginInvoice = "no invoice right now; solve the proof-of-work instead"

// serveLoginInvoice answers POST /.spikes402/login-invoice
// {"method", "path", "username"} with an invoice for one attempt by that
// account at its current level. When paying is off or a limit is reached
// it answers 503 with a proof-of-work challenge instead (fail closed:
// the node is left alone, the wall stays).
func (g *Gate) serveLoginInvoice(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", protectedCacheControl)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct{ Method, Path, Username string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Method == "" {
		req.Method = http.MethodPost
	}
	route := g.loginRoute(req.Method, req.Path)
	if route == nil || req.Username == "" || len(req.Username) > loginMaxUsername {
		http.Error(w, "not a login route, or no username", http.StatusBadRequest)
		return
	}
	now := g.now()
	if ok, wait := g.loginAllow(r, now); !ok {
		w.Header().Set("Retry-After", itoaWait(wait))
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	acct, net := g.loginAccount(req.Username), g.loginNetwork(r)
	inv, err := g.newLoginInvoice(route, acct, net, now)
	if err != nil {
		if err != errLoginPayOff {
			g.nodeLog.printf(now, "spikes402: no login invoice, offering proof-of-work only: %v", err)
		}
		g.sendLoginChallenge(w, r, route, acct, http.StatusServiceUnavailable, errNoLoginInvoice)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(inv)
}

var errLoginPayOff = errors.New("paying is off for this login route")

func (g *Gate) newLoginInvoice(route *LoginRoute, acct, net string, now time.Time) (loginInvoice, error) {
	level := g.login.Level(acct, net, now)
	price := g.loginPrice(route, level)
	if price <= 0 {
		return loginInvoice{}, errLoginPayOff
	}
	b := make([]byte, 16)
	rand.Read(b) // never fails (crypto/rand)
	id := hex.EncodeToString(b)
	if !g.loginPay.reserve(id, acct, net, now) {
		return loginInvoice{}, errNodeRate
	}
	inv, err := g.node.createInvoice(now, price, loginInvoiceMemo, loginInvoiceTTL)
	if err != nil {
		g.loginPay.drop(id)
		return loginInvoice{}, err
	}
	exp := now.Add(loginInvoiceTTL + loginPayGrace)
	token, err := g.sign.sign(claims{
		ID: id, Resource: loginResource(route), Expires: exp.Unix(), PriceSats: price,
		PaymentHash: hex.EncodeToString(inv.PaymentHash[:]), Kind: "loginpay", Acct: acct, Net: net, Level: level,
	})
	if err != nil {
		g.loginPay.drop(id)
		return loginInvoice{}, err
	}
	return loginInvoice{
		Challenge: token, Invoice: inv.PaymentRequest, QR: string(invoiceQR(inv.PaymentRequest)),
		PriceSats: price, Level: level, ExpiresAt: now.Add(loginInvoiceTTL).UTC(),
		Usage: `pay the invoice, then send POST ` + LoginRedeemPath + ` with "Authorization: L402 <challenge>:<preimage>" (or "L402 <challenge>" to ask whether it was paid) for a one-use login cookie; it buys one attempt, not access`,
	}, nil
}

var errUnpaid = errors.New("invoice not paid yet")

// checkLoginPay verifies "<token>[:<preimage>]" for a login invoice and
// spends the token: one paid attempt per invoice. Like a proof, a payment
// is refused once the level has risen above the one it was priced at;
// the payer then needs a new invoice (see "Paying instead" in the README).
// Without a preimage the node is asked, through the shared lookup cache
// and its limits.
func (g *Gate) checkLoginPay(cred, resHash, net string, now time.Time) (claims, error) {
	token, preimage, hasPre := strings.Cut(strings.TrimSpace(cred), ":")
	c, err := g.checkLoginClaims(token, "loginpay", resHash, "", net)
	if err != nil {
		return claims{}, err
	}
	if c.Level < g.login.Level(c.Acct, net, now) {
		return claims{}, errLoginLevel
	}
	key := loginSpentKey(c.ID)
	if g.spent.has(key) {
		return claims{}, errReplay
	}
	if hasPre {
		raw, err := hex.DecodeString(preimage)
		want, err2 := hex.DecodeString(c.PaymentHash)
		got := sha256.Sum256(raw)
		if err != nil || err2 != nil || len(raw) != 32 || subtle.ConstantTimeCompare(got[:], want) != 1 {
			return claims{}, errBadProof
		}
	} else {
		pay, err := g.invoices.get(c.PaymentHash, time.Unix(c.Expires, 0), now)
		switch {
		case err == errLookupBusy || err == errNoLookup || (err != nil && err != lightning.ErrNotFound):
			return claims{}, &busyError{http.StatusServiceUnavailable, 2 * time.Second, "cannot ask the node right now; retry shortly, or send the preimage"}
		case err != nil || !pay.Paid:
			return claims{}, errUnpaid
		}
	}
	if err := g.spent.use(key, time.Unix(c.Expires, 0), now); err != nil {
		return claims{}, err
	}
	g.loginPay.drop(c.ID)
	return c, nil
}
