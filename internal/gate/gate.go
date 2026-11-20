// Package gate is the heart of Spikes402: an http.Handler that sits in
// front of a website and asks for "pay or PoW" on protected paths.
package gate

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Choffolo/spikes402/internal/botid"
	"github.com/Choffolo/spikes402/internal/lightning"
	"github.com/Choffolo/spikes402/pow"
)

// Options configure a Gate.
type Options struct {
	PriceSats    int64                            // price of one request, in satoshis
	Protect      []string                         // path patterns that require payment
	PoW          func(priceSats int64) pow.Params // the puzzle for a price
	ChallengeTTL time.Duration                    // how long a 402 challenge stays valid
	SecretKey    []byte                           // signs tokens; keep it secret
	Lightning    lightning.Backend
	Next         http.Handler // where paid/free requests go (the origin)

	// Abuse protection for unpaid requests.
	ChallengesPerMinute  int          // 402 answers per client per minute
	MaxInvoicesPerMinute int          // invoices created per minute, all clients together
	MaxPendingInvoices   int          // open invoices at once; beyond this, PoW only
	ClientIPHeader       string       // header with the client IP set by a trusted proxy, or ""
	TrustedProxies       []*net.IPNet // ClientIPHeader is only believed from these addresses

	// Bot identification (optional). Classifier nil: everyone is "unknown".
	// Policies empty (the default): everyone pays the same.
	Classifier *botid.Classifier
	Policies   map[string]Policy

	// Observer, if set, is told about every request to a protected path.
	// It is called synchronously and must be fast.
	Observer func(Event)
}

// ClassHeader is the response header that tells the client how it was
// classified, e.g. `verified; name="Googlebot"; method=ip-list`.
const ClassHeader = "Spikes402-Class"

// Gate is the pay-or-PoW http.Handler.
type Gate struct {
	opt     Options
	sign    signer
	spent   *spentSet
	limiter *rateLimiter
	pending *pendingOffers
	node    *nodeGuard
	now     func() time.Time // replaceable in tests
	live    atomic.Pointer[Settings]

	// argonSlots caps how many Argon2id solutions are checked at once:
	// each check takes tens of MiB and milliseconds of CPU.
	argonSlots chan struct{}

	nodeLog  rateLog // why no invoice was offered
	classLog rateLog // bot classifications
}

// New returns a Gate. It panics on programmer errors; validate user
// input with the config package first.
func New(opt Options) *Gate {
	if len(opt.SecretKey) < 32 || opt.Lightning == nil || opt.Next == nil || opt.PriceSats < 1 ||
		opt.ChallengeTTL <= 0 || opt.PoW == nil || opt.ChallengesPerMinute < 1 || opt.MaxPendingInvoices < 1 || opt.MaxInvoicesPerMinute < 1 {
		panic("gate: invalid options")
	}
	g := &Gate{
		opt:     opt,
		sign:    signer{opt.SecretKey},
		spent:   newSpentSet(),
		limiter: newRateLimiter(opt.ChallengesPerMinute),
		pending: newPendingOffers(opt.MaxPendingInvoices),
		node:    newNodeGuard(opt.Lightning, opt.MaxInvoicesPerMinute),
		now:     time.Now,

		argonSlots: make(chan struct{}, min(4, max(1, runtime.NumCPU()/2))),
		nodeLog:    rateLog{perMinute: 1},
		classLog:   rateLog{perMinute: 60},
	}
	g.live.Store(&Settings{PriceSats: opt.PriceSats, PoW: opt.PoW, Policies: opt.Policies, ChallengesPerMinute: opt.ChallengesPerMinute})
	return g
}

// ServeHTTP implements http.Handler.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Refuse paths like "/free/../paid": the origin could normalise them
	// into a protected path we did not recognise.
	if !isCleanPath(r.URL.Path) {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if !g.protected(r.URL.Path) {
		g.opt.Next.ServeHTTP(w, r)
		return
	}

	set := g.live.Load() // one consistent snapshot for this request
	class := g.classify(r)
	w.Header().Set(ClassHeader, class.String())
	pol := g.policyFor(set, class)
	switch pol.Action {
	case ActionBlock:
		g.observe(r, class, OutcomeBlocked)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	case ActionFree:
		g.observe(r, class, OutcomeFree)
		r.Header.Del("Authorization")
		g.opt.Next.ServeHTTP(w, r)
		return
	}
	price := pol.price(set.PriceSats)
	key := g.pendingKey(r, price, pol.Action)

	var problem string
	if auth := r.Header.Get("Authorization"); auth != "" {
		id, err := g.redeem(r, auth)
		if err == nil {
			outcome := OutcomePaid
			if scheme, _, _ := strings.Cut(auth, " "); strings.EqualFold(scheme, "PoW") {
				outcome = OutcomePoW
			}
			g.observe(r, class, outcome)
			g.pending.drop(key, id)
			r.Header.Del("Authorization") // our credential, not the origin's business
			g.opt.Next.ServeHTTP(w, r)
			return
		}
		var busy *busyError
		if errors.As(err, &busy) {
			if busy.status == http.StatusTooManyRequests {
				g.observe(r, class, OutcomeRateLimited)
			} else {
				g.observe(r, class, OutcomeBusy)
			}
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(busy.wait.Seconds()))))
			http.Error(w, busy.msg, busy.status)
			return
		}
		problem = err.Error()
	}
	g.challenge(w, r, set, class, problem, key, price, pol.Action == ActionPoWOnly)
}

// classify identifies the client and logs anything noteworthy.
func (g *Gate) classify(r *http.Request) botid.Class {
	if g.opt.Classifier == nil {
		return botid.Class{Kind: botid.Unknown}
	}
	c := g.opt.Classifier.Classify(r, g.clientIP(r), g.scheme(r))
	if c.Kind != botid.Unknown || c.Note != "" {
		g.classLog.printf(g.now(), "spikes402: %s; client=%s; resource=%q; user-agent=%q",
			sanitize(c.String()), g.client(r), sanitize(resource(r)), sanitize(r.UserAgent()))
	}
	return c
}

// scheme is "https" if the client reached us over TLS: directly, or via a
// trusted proxy that says so in X-Forwarded-Proto.
func (g *Gate) scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if ip := remoteIP(r); ip != nil && isTrusted(ip, g.opt.TrustedProxies) {
		first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
		if strings.EqualFold(strings.TrimSpace(first), "https") {
			return "https"
		}
	}
	return "http"
}

func isCleanPath(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	c := path.Clean(p)
	return c == p || c+"/" == p
}

// protected reports whether p matches one of the configured patterns.
// Patterns use path.Match syntax ("/a/*.html"); a pattern ending in "/**"
// matches everything below that prefix ("/premium/**").
func (g *Gate) protected(p string) bool {
	for _, pat := range g.opt.Protect {
		if prefix, ok := strings.CutSuffix(pat, "/**"); ok {
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				return true
			}
			continue
		}
		if ok, _ := path.Match(pat, p); ok {
			return true
		}
	}
	return false
}

// resource is what a token is bound to: the method and the full path
// with query string. A token for one page cannot unlock another.
func resource(r *http.Request) string {
	return r.Method + " " + r.URL.RequestURI()
}

var (
	errExpired     = errors.New("token expired")
	errWrongTarget = errors.New("token is for a different resource")
	errBadProof    = errors.New("wrong preimage or proof-of-work solution")
	errReplay      = errors.New("token already used")
	errScheme      = errors.New("unsupported Authorization scheme (use L402 or PoW)")
)

// busyError asks the client to retry later instead of getting a new 402.
type busyError struct {
	status int
	wait   time.Duration
	msg    string
}

func (e *busyError) Error() string { return e.msg }

// redeem checks an "L402 <token>:<preimage>" or "PoW <token>:<nonce>"
// credential and marks the token as spent if it is valid. It returns the
// token ID.
func (g *Gate) redeem(r *http.Request, auth string) (string, error) {
	c, err := g.check(r, auth)
	if err != nil {
		return "", err
	}
	// Checked last, so a bad attempt does not burn a good token.
	if err := g.spent.use(c.ID, time.Unix(c.Expires, 0), g.now()); err != nil {
		return "", err
	}
	return c.ID, nil
}

// check verifies a credential without spending it.
func (g *Gate) check(r *http.Request, auth string) (claims, error) {
	scheme, cred, _ := strings.Cut(auth, " ")
	token, proof, ok := strings.Cut(strings.TrimSpace(cred), ":")
	if !ok {
		return claims{}, errBadToken
	}
	c, err := g.sign.verify(token)
	if err != nil {
		return claims{}, err
	}
	if g.now().Unix() >= c.Expires {
		return claims{}, errExpired
	}
	if c.Resource != resource(r) {
		return claims{}, errWrongTarget
	}

	switch strings.ToUpper(scheme) {
	case "L402", "LSAT": // LSAT is the older name of L402
		preimage, err := hex.DecodeString(proof)
		want, err2 := hex.DecodeString(c.PaymentHash)
		if err != nil || err2 != nil || len(preimage) != 32 || len(want) != 32 {
			return claims{}, errBadProof
		}
		got := sha256.Sum256(preimage)
		if subtle.ConstantTimeCompare(got[:], want) != 1 {
			return claims{}, errBadProof
		}
	case "POW":
		nonce, err := strconv.ParseUint(proof, 10, 64)
		p := c.powParams()
		if err != nil || p.Validate() != nil {
			return claims{}, errBadProof
		}
		if p.Algorithm == pow.Argon2id {
			return c, g.checkArgon(r, p, token, nonce, c)
		}
		if !p.Check(token, nonce) {
			return claims{}, errBadProof
		}
	default:
		return claims{}, errScheme
	}
	return c, nil
}

// checkArgon verifies an Argon2id solution. A check costs the server tens
// of MiB and milliseconds, so it is only done for a genuine, unexpired,
// unused token of ours (checked above and here), counts against the
// client's rate limit, runs at most len(argonSlots) at a time, and a
// wrong answer burns the token: one check per token, ever.
func (g *Gate) checkArgon(r *http.Request, p pow.Params, token string, nonce uint64, c claims) error {
	now := g.now()
	if g.spent.has(c.ID) {
		return errReplay
	}
	if ok, wait := g.limiter.allow(g.client(r), now); !ok {
		return &busyError{http.StatusTooManyRequests, wait, "too many requests"}
	}
	select {
	case g.argonSlots <- struct{}{}:
		defer func() { <-g.argonSlots }()
	default:
		return &busyError{http.StatusServiceUnavailable, time.Second, "busy checking proofs of work, retry shortly"}
	}
	if !p.Check(token, nonce) {
		g.spent.use(c.ID, time.Unix(c.Expires, 0), now)
		return errBadProof
	}
	return nil
}

// challengeBody is the JSON answer to an unpaid request.
type challengeBody struct {
	Error     string     `json:"error"`
	Problem   string     `json:"problem,omitempty"`
	Resource  string     `json:"resource"`
	PriceSats int64      `json:"price_sats"`
	ExpiresAt time.Time  `json:"expires_at"`
	L402      *l402Offer `json:"l402,omitempty"`
	PoW       powOffer   `json:"pow"`
}

type l402Offer struct {
	Token   string `json:"token"`
	Invoice string `json:"invoice"`
	Usage   string `json:"usage"`
}

type powOffer struct {
	Challenge       string  `json:"challenge"`
	Difficulty      int     `json:"difficulty"`
	Algorithm       string  `json:"algorithm"`
	MemoryMiB       int     `json:"memory_mib,omitempty"`
	ExpectedSeconds float64 `json:"expected_seconds_one_cpu_core"`
	Usage           string  `json:"usage"`
}

func (g *Gate) pendingKey(r *http.Request, price int64, a Action) string {
	return g.client(r) + " " + strconv.FormatInt(price, 10) + " " + string(a) + " " + resource(r)
}

func (g *Gate) client(r *http.Request) string {
	return clientKey(r, g.opt.ClientIPHeader, g.opt.TrustedProxies)
}

func (g *Gate) clientIP(r *http.Request) net.IP {
	return clientIP(r, g.opt.ClientIPHeader, g.opt.TrustedProxies)
}

// challenge answers 402 Payment Required with both ways in, or 429 Too
// Many Requests if this client asked for too many challenges.
func (g *Gate) challenge(w http.ResponseWriter, r *http.Request, set *Settings, class botid.Class, problem, key string, price int64, powOnly bool) {
	now := g.now()
	if ok, wait := g.limiter.allow(g.client(r), now); !ok {
		g.observe(r, class, OutcomeRateLimited)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	// Same client, same page, offer still fresh: hand it out again rather
	// than creating another invoice.
	o, ok := g.pending.get(key, now, g.opt.ChallengeTTL/2)
	if !ok || g.spent.has(o.id) {
		var err error
		if o, err = g.newOffer(r, now, key, set.PoW(price), price, powOnly); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	if problem != "" {
		g.observe(r, class, OutcomeRejected)
	} else {
		g.observe(r, class, OutcomeChallenged)
	}
	g.writeChallenge(w, r, o, problem)
}

// newOffer creates a token and, if there is room, an invoice.
func (g *Gate) newOffer(r *http.Request, now time.Time, key string, p pow.Params, price int64, powOnly bool) (offer, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return offer{}, err
	}
	c := claims{
		ID:       hex.EncodeToString(id),
		Resource: resource(r),
		Expires:  now.Add(g.opt.ChallengeTTL).Unix(),
	}
	c.PowBits = p.Difficulty
	if p.Algorithm == pow.Argon2id {
		c.PowAlg, c.PowMem = p.Algorithm, p.MemoryMiB
	}

	// Proof-of-work is always offered. An invoice only if the node may be
	// called: not too many open invoices, within the global rate, node
	// healthy, and the policy for this client allows it.
	var invoice string
	if !powOnly && g.pending.reserve(now) {
		inv, err := g.node.createInvoice(now, price, "Spikes402: "+c.Resource, g.opt.ChallengeTTL)
		if err != nil {
			g.nodeLog.printf(now, "spikes402: no invoice, offering proof-of-work only: %v", err)
		} else {
			c.PaymentHash = hex.EncodeToString(inv.PaymentHash[:])
			invoice = inv.PaymentRequest
		}
	}

	token, err := g.sign.sign(c)
	if err != nil {
		return offer{}, err
	}
	o := offer{id: c.ID, token: token, invoice: invoice, expires: time.Unix(c.Expires, 0), price: price, pow: p}
	if invoice != "" {
		g.pending.put(key, o)
	}
	return o, nil
}

func (g *Gate) writeChallenge(w http.ResponseWriter, r *http.Request, o offer, problem string) {
	body := challengeBody{
		Error:     "payment required",
		Problem:   problem,
		Resource:  resource(r),
		PriceSats: o.price,
		ExpiresAt: o.expires.UTC(),
		PoW: powOffer{
			Challenge:       o.token,
			Difficulty:      o.pow.Difficulty,
			Algorithm:       o.pow.Algorithm,
			ExpectedSeconds: math.Round(o.pow.ExpectedSeconds()*10) / 10,
			Usage:           `find nonce so that SHA256(SHA256(challenge + ":" + nonce)) has "difficulty" leading zero bits, then send "Authorization: PoW <challenge>:<nonce>"`,
		},
	}
	powHeader := fmt.Sprintf(`PoW challenge=%q, difficulty="%d", algorithm="sha256d"`, o.token, o.pow.Difficulty)
	if o.pow.Algorithm == pow.Argon2id {
		body.PoW.MemoryMiB = o.pow.MemoryMiB
		body.PoW.Usage = `find nonce so that Argon2id(password = challenge + ":" + nonce, salt = "spikes402-pow-v1", time = 1, memory = memory_mib MiB, threads = 1, 32 bytes) has "difficulty" leading zero bits, then send "Authorization: PoW <challenge>:<nonce>"`
		powHeader = fmt.Sprintf(`PoW challenge=%q, difficulty="%d", algorithm="argon2id", memory-mib="%d"`, o.token, o.pow.Difficulty, o.pow.MemoryMiB)
	}

	h := w.Header()
	if o.invoice != "" {
		h.Add("WWW-Authenticate", fmt.Sprintf(`L402 macaroon=%q, invoice=%q`, o.token, o.invoice))
		body.L402 = &l402Offer{
			Token:   o.token,
			Invoice: o.invoice,
			Usage:   `pay the invoice, then send "Authorization: L402 <token>:<preimage hex>"`,
		}
	}
	h.Add("WWW-Authenticate", powHeader)
	h.Set("Cache-Control", "no-store")

	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		h.Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusPaymentRequired)
		writeHTML(w, body)
		return
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(body)
}
