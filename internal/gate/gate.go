// Package gate is the heart of Spikes402: an http.Handler that sits in
// front of a website and asks for "pay or PoW" on protected paths.
package gate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Choffolo/spikes402/internal/lightning"
	"github.com/Choffolo/spikes402/pow"
)

// Options configure a Gate.
type Options struct {
	PriceSats     int64         // price of one request, in satoshis
	Protect       []string      // path patterns that require payment
	PowDifficulty int           // leading zero bits required by the PoW puzzle
	ChallengeTTL  time.Duration // how long a 402 challenge stays valid
	SecretKey     []byte        // signs tokens; keep it secret
	Lightning     lightning.Backend
	Next          http.Handler // where paid/free requests go (the origin)
}

// Gate is the pay-or-PoW http.Handler.
type Gate struct {
	opt   Options
	sign  signer
	spent *spentSet
	now   func() time.Time // replaceable in tests
}

// New returns a Gate. It panics on programmer errors; validate user
// input with the config package first.
func New(opt Options) *Gate {
	if len(opt.SecretKey) < 32 || opt.Lightning == nil || opt.Next == nil || opt.PriceSats < 1 || opt.ChallengeTTL <= 0 {
		panic("gate: invalid options")
	}
	return &Gate{opt: opt, sign: signer{opt.SecretKey}, spent: newSpentSet(), now: time.Now}
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

	var problem string
	if auth := r.Header.Get("Authorization"); auth != "" {
		err := g.redeem(r, auth)
		if err == nil {
			r.Header.Del("Authorization") // our credential, not the origin's business
			g.opt.Next.ServeHTTP(w, r)
			return
		}
		problem = err.Error()
	}
	g.challenge(w, r, problem)
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

// redeem checks an "L402 <token>:<preimage>" or "PoW <token>:<nonce>"
// credential and marks the token as spent if it is valid.
func (g *Gate) redeem(r *http.Request, auth string) error {
	scheme, cred, _ := strings.Cut(auth, " ")
	token, proof, ok := strings.Cut(strings.TrimSpace(cred), ":")
	if !ok {
		return errBadToken
	}
	c, err := g.sign.verify(token)
	if err != nil {
		return err
	}
	now := g.now()
	if now.Unix() >= c.Expires {
		return errExpired
	}
	if c.Resource != resource(r) {
		return errWrongTarget
	}

	switch strings.ToUpper(scheme) {
	case "L402", "LSAT": // LSAT is the older name of L402
		preimage, err := hex.DecodeString(proof)
		want, err2 := hex.DecodeString(c.PaymentHash)
		if err != nil || err2 != nil || len(preimage) != 32 || len(want) != 32 {
			return errBadProof
		}
		got := sha256.Sum256(preimage)
		if subtle.ConstantTimeCompare(got[:], want) != 1 {
			return errBadProof
		}
	case "POW":
		nonce, err := strconv.ParseUint(proof, 10, 64)
		if err != nil || !pow.Check(token, nonce, c.PowBits) {
			return errBadProof
		}
	default:
		return errScheme
	}

	// Checked last, so a bad attempt does not burn a good token.
	if !g.spent.use(c.ID, time.Unix(c.Expires, 0), now) {
		return errReplay
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
	Challenge  string `json:"challenge"`
	Difficulty int    `json:"difficulty"`
	Algorithm  string `json:"algorithm"`
	Usage      string `json:"usage"`
}

// challenge answers 402 Payment Required with both ways in.
func (g *Gate) challenge(w http.ResponseWriter, r *http.Request, problem string) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	now := g.now()
	c := claims{
		ID:       hex.EncodeToString(id),
		Resource: resource(r),
		Expires:  now.Add(g.opt.ChallengeTTL).Unix(),
		PowBits:  g.opt.PowDifficulty,
	}

	// If the Lightning node is unreachable we still offer proof-of-work.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	inv, err := g.opt.Lightning.CreateInvoice(ctx, g.opt.PriceSats, "Spikes402: "+c.Resource, g.opt.ChallengeTTL)
	if err != nil {
		log.Printf("spikes402: cannot create invoice, offering proof-of-work only: %v", err)
	} else {
		c.PaymentHash = hex.EncodeToString(inv.PaymentHash[:])
	}

	token, err := g.sign.sign(c)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	body := challengeBody{
		Error:     "payment required",
		Problem:   problem,
		Resource:  c.Resource,
		PriceSats: g.opt.PriceSats,
		ExpiresAt: time.Unix(c.Expires, 0).UTC(),
		PoW: powOffer{
			Challenge:  token,
			Difficulty: c.PowBits,
			Algorithm:  "sha256d",
			Usage:      `find nonce so that SHA256(SHA256(challenge + ":" + nonce)) has "difficulty" leading zero bits, then send "Authorization: PoW <challenge>:<nonce>"`,
		},
	}

	h := w.Header()
	if c.PaymentHash != "" {
		h.Add("WWW-Authenticate", fmt.Sprintf(`L402 macaroon=%q, invoice=%q`, token, inv.PaymentRequest))
		body.L402 = &l402Offer{
			Token:   token,
			Invoice: inv.PaymentRequest,
			Usage:   `pay the invoice, then send "Authorization: L402 <token>:<preimage hex>"`,
		}
	}
	h.Add("WWW-Authenticate", fmt.Sprintf(`PoW challenge=%q, difficulty="%d", algorithm="sha256d"`, token, c.PowBits))
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
	enc.SetIndent("", "  ")
	enc.Encode(body)
}
