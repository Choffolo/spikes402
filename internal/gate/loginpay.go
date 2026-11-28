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
	"mime"
	"net/http"
	"net/url"
	"slices"
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
// doubling with each level above it, at most MaxSats (0:
// loginDefaultMaxFactor times BaseSats). The price depends on nothing but
// the level, so it tells no more than the level does.
type LoginPrice struct {
	BaseSats int64 `json:"base_sats"`
	MaxSats  int64 `json:"max_sats,omitempty"`
}

// loginDefaultMaxFactor caps the price at six doublings (64 × base_sats)
// when max_sats is not set: a real user whose level others raised by the
// account and network terms still pays a bounded price.
const loginDefaultMaxFactor = 64

func (p *LoginPrice) maxSats() int64 {
	if p.MaxSats > 0 {
		return p.MaxSats
	}
	return min(p.BaseSats*loginDefaultMaxFactor, maxLoginMaxSats)
}

// LoginInvoiceLimits bound the invoices login payments may ask the node
// for. Beyond any of them, no invoice: the client solves the
// proof-of-work instead.
type LoginInvoiceLimits struct {
	// Per account from one network (/24 or /48), never per account alone:
	// a limit anyone could fill for an account would let a third party
	// push its real user back to the proof-of-work, and show them that
	// someone is paying for it.
	AccountNetworkOpen      int `json:"account_network_open"`       // unpaid, unexpired invoices
	AccountNetworkPerMinute int `json:"account_network_per_minute"` // new invoices per minute
	NetworkPerMinute        int `json:"network_per_minute"`         // per /24 or /48 per minute
	PerMinute               int `json:"per_minute"`                 // all login invoices per minute
	Open                    int `json:"open"`                       // all unpaid, unexpired login invoices
}

// DefaultLoginInvoiceLimits are used for every limit left at 0.
var DefaultLoginInvoiceLimits = LoginInvoiceLimits{AccountNetworkOpen: 2, AccountNetworkPerMinute: 6, NetworkPerMinute: 20, PerMinute: 60, Open: 500}

// CheckLoginPrice validates a price; nil or BaseSats 0 means no paying.
func CheckLoginPrice(p *LoginPrice) error {
	if p == nil {
		return nil
	}
	if p.BaseSats < 0 || p.BaseSats > maxLoginBaseSats {
		return fmt.Errorf("base_sats must be between 0 and %d", maxLoginBaseSats)
	}
	if p.MaxSats < 0 || p.MaxSats != 0 && (p.MaxSats < p.BaseSats || p.MaxSats > maxLoginMaxSats) {
		return fmt.Errorf("max_sats must be between base_sats and %d (default %d × base_sats)", maxLoginMaxSats, loginDefaultMaxFactor)
	}
	return nil
}

// WithDefaults fills every limit left at 0 and checks the others.
func (l LoginInvoiceLimits) WithDefaults() (LoginInvoiceLimits, error) {
	d := DefaultLoginInvoiceLimits
	for _, f := range []struct{ v, def *int }{
		{&l.AccountNetworkOpen, &d.AccountNetworkOpen}, {&l.AccountNetworkPerMinute, &d.AccountNetworkPerMinute},
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
	if p == nil || p.BaseSats <= 0 || g.live.Load().Paused {
		return 0
	}
	sats := p.BaseSats << min(max(level-LoginBaseLevel, 0), LoginMaxLevel-LoginBaseLevel)
	return min(sats, p.maxSats())
}

// loginInvoices counts open login invoices and recent ones, so paying can
// never flood the node (on top of the node guard every invoice passes).
type loginInvoices struct {
	mu    sync.Mutex
	lim   LoginInvoiceLimits
	open  map[string]openLoginInvoice // token ID
	pair  map[string]int              // open invoices per account and network
	byMin map[string]*window          // "p "+pair, "n "+network, "all"
	swept time.Time
}

type openLoginInvoice struct {
	pair    string
	expires time.Time
}

func newLoginInvoices(lim LoginInvoiceLimits) *loginInvoices {
	return &loginInvoices{lim: lim, open: map[string]openLoginInvoice{}, pair: map[string]int{}, byMin: map[string]*window{}}
}

// reserve counts a new invoice for acct from net if every limit allows
// it. Only allowed invoices are counted, so the maps stay bounded by the
// global limits (Open, and PerMinute keys a minute). There is no limit
// per account alone (see LoginInvoiceLimits): many networks asking for
// one account are bounded by the per-network and global limits, which
// cap the node's load whatever the accounts.
func (l *loginInvoices) reserve(id, acct, net string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	pair := acct + " " + net
	if len(l.open) >= l.lim.Open || l.pair[pair] >= l.lim.AccountNetworkOpen {
		return false
	}
	keys := []struct {
		k   string
		max int
	}{{"all", l.lim.PerMinute}, {"p " + pair, l.lim.AccountNetworkPerMinute}, {"n " + net, l.lim.NetworkPerMinute}}
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
	l.open[id] = openLoginInvoice{pair: pair, expires: now.Add(loginInvoiceTTL)}
	l.pair[pair]++
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
	if l.pair[o.pair]--; l.pair[o.pair] <= 0 {
		delete(l.pair, o.pair)
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
	// Not a "simple" cross-site request: a hostile page must not be able
	// to create invoices in a visitor's name and fill their limits.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if !g.loginSameSite(r) {
		http.Error(w, errCrossSite, http.StatusForbidden)
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
			g.nodeLog.printf(now, "spikes402: no login invoice, offering proof-of-work only: %s", sanitize(err.Error()))
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

var (
	errUnpaid    = errors.New("invoice not paid yet")
	errUnderpaid = errors.New("the node reports less than the price received for this invoice")
)

const errCrossSite = "cross-site request refused"

// loginSameSite reports whether a browser request to a login endpoint
// comes from this site's own pages (login.js fetches same-origin). A
// request without Sec-Fetch-Site or Origin (API clients, old browsers)
// passes: the checks only stop a hostile page acting in a visitor's
// browser, whose requests always carry Origin on POST. Only "same-origin"
// and "none" pass Sec-Fetch-Site ("same-site" is a sibling subdomain);
// Origin must name the request's own Host (a page cannot choose the Host
// of the visitor's request), one of public_hosts or the origin's host.
func (g *Gate) loginSameSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	o := r.Header.Values("Origin")
	if len(o) == 0 {
		return true
	}
	u, err := url.Parse(o[0])
	if len(o) != 1 || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return false // includes "null"
	}
	return strings.EqualFold(u.Host, r.Host) ||
		slices.ContainsFunc(g.loginHosts, func(h string) bool { return strings.EqualFold(u.Hostname(), hostOnly(h)) })
}

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
		return c, errLoginLevel // c: for the new price (see serveLoginRedeem)
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
		pay, err := g.invoices.getFor(c.PaymentHash, time.Unix(c.Expires, 0), now, true)
		switch {
		case err == errLookupBusy || err == errNoLookup || (err != nil && err != lightning.ErrNotFound):
			return claims{}, &busyError{http.StatusServiceUnavailable, 2 * time.Second, "cannot ask the node right now; retry shortly, or send the preimage"}
		case err != nil || !pay.Paid:
			return claims{}, errUnpaid
		case pay.ReceivedSat < c.PriceSats:
			return claims{}, errUnderpaid
		}
	}
	// The payment hash is spent too, for the token's lifetime: a node that
	// handed out one payment hash twice must not let one preimage redeem
	// two tokens.
	hashKey := "loginpay-hash:" + c.PaymentHash
	if g.spent.has(hashKey) {
		return claims{}, errReplay
	}
	if err := g.spent.use(key, time.Unix(c.Expires, 0), now); err != nil {
		return claims{}, err
	}
	if err := g.spent.use(hashKey, time.Unix(c.Expires, 0), now); err != nil {
		return claims{}, err
	}
	g.loginPay.drop(c.ID)
	return c, nil
}

// writeStalePayment answers a payment made at a level that has risen
// since: 409, what happened, and the price of a new invoice now. No
// refund and no top-up: the strict level rule is what stops payments
// being stockpiled. The new price is what a challenge from this network
// for this account would show anyway.
func (g *Gate) writeStalePayment(w http.ResponseWriter, route *LoginRoute, acct, net string) {
	level := g.login.Level(acct, net, g.now())
	price := g.loginPrice(route, level)
	msg := "the price went up after a failed login attempt on this account or from this network, so this payment no longer buys an attempt"
	if price > 0 {
		msg += fmt.Sprintf("; a new invoice costs %d sats", price)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	json.NewEncoder(w).Encode(struct {
		Error     string `json:"error"`
		PriceSats int64  `json:"price_sats"`
		Level     int    `json:"level"`
	}{msg, price, level})
}
