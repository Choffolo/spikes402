package webbotauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Choffolo/spikes402/internal/sfv"
)

// Outcome of verifying a request (draft appendix C.1).
type Outcome string

const (
	None       Outcome = ""           // no Web Bot Auth signature
	Verified   Outcome = "verified"   // signature and key material validate
	Invalid    Outcome = "invalid"    // signature, components, key or freshness fail
	Unverified Outcome = "unverified" // not enough information to decide
)

// Result of Verify.
type Result struct {
	Outcome Outcome
	Agent   string // identifier URL (only meaningful when Verified)
	KeyID   string
	Reason  string // why it is not Verified
}

// Verifier limits.
const (
	MaxLifetime      = time.Hour   // expires - created
	ClockSkew        = time.Minute // tolerated for created
	maxSignatures    = 4           // web-bot-auth signatures examined per request
	maxReplayEntries = 100_000     // remembered signatures/nonces
	directoryWait    = 1500 * time.Millisecond
)

// Verifier checks Web Bot Auth signatures.
type Verifier struct {
	Dirs *Directories
	now  func() time.Time

	mu     sync.Mutex
	seen   map[[32]byte]time.Time // replay protection: key -> expires
	sweeps time.Time
}

// NewVerifier returns a verifier using dirs for key discovery.
func NewVerifier(dirs *Directories) *Verifier {
	return &Verifier{Dirs: dirs, now: time.Now, seen: make(map[[32]byte]time.Time)}
}

// Verify checks the request's Web Bot Auth signatures. scheme is the
// scheme the client used ("https" behind a TLS-terminating proxy).
// The first verified signature wins.
func (v *Verifier) Verify(r *http.Request, scheme string) Result {
	if len(r.Header.Values("Signature-Input")) == 0 && len(r.Header.Values("Signature")) == 0 {
		return Result{}
	}
	if scheme != "https" && !v.Dirs.devMode {
		return Result{Outcome: Unverified, Reason: "signature not sent over https"}
	}
	inputs, err1 := sfv.ParseDictionary(strings.Join(r.Header.Values("Signature-Input"), ", "))
	sigs, err2 := sfv.ParseDictionary(strings.Join(r.Header.Values("Signature"), ", "))
	agent, err3 := parseAgentHeader(r)
	if err := errors.Join(err1, err2, err3); err != nil {
		return Result{Outcome: Invalid, Reason: "malformed signature headers"}
	}

	best := Result{Outcome: Unverified, Reason: "no web-bot-auth signature"}
	checked := 0
	for _, m := range inputs {
		l, ok := m.Value.(sfv.InnerList)
		if !ok || l.Params.Get("tag") != Tag {
			continue // other signatures are not ours to judge
		}
		if checked++; checked > maxSignatures {
			break
		}
		res := v.verifyOne(requestInfo{r: r, scheme: scheme, agent: agent}, m.Key, l, sigs)
		if res.Outcome == Verified {
			return res
		}
		if best.Reason == "no web-bot-auth signature" || res.Outcome == Invalid {
			best = res
		}
	}
	return best
}

func (v *Verifier) verifyOne(ri requestInfo, label string, l sfv.InnerList, sigs sfv.Dictionary) Result {
	invalid := func(format string, a ...any) Result {
		return Result{Outcome: Invalid, Reason: fmt.Sprintf(format, a...)}
	}
	created, ok1 := l.Params.Get("created").(int64)
	expires, ok2 := l.Params.Get("expires").(int64)
	keyid, ok3 := l.Params.Get("keyid").(string)
	if !ok1 || !ok2 || !ok3 {
		return invalid("created, expires and keyid are required")
	}
	if alg := l.Params.Get("alg"); alg != nil && alg != "ed25519" {
		return Result{Outcome: Unverified, KeyID: keyid, Reason: "only ed25519 is supported"}
	}
	now := v.now().Unix()
	switch {
	case created > now+int64(ClockSkew/time.Second):
		return invalid("created is in the future")
	case expires <= now:
		return invalid("signature expired")
	case expires <= created || expires-created > int64(MaxLifetime/time.Second):
		return invalid("validity window longer than %s", MaxLifetime)
	}

	// Required components: @authority or @target-uri, and the
	// Signature-Agent member of this label.
	var hasTarget, hasAgent bool
	for _, c := range l.Items {
		name, _ := c.Value.(string)
		switch {
		case (name == "@authority" || name == "@target-uri") && len(c.Params) == 0:
			hasTarget = true
		case name == "signature-agent":
			key, _ := c.Params.Get("key").(string)
			hasAgent = (ri.agent.legacy && len(c.Params) == 0) || (!ri.agent.legacy && key == label)
		}
	}
	if !hasTarget || !hasAgent {
		return invalid("must cover @authority (or @target-uri) and its Signature-Agent member")
	}
	agentURL, ok := ri.agent.agentURL(label)
	if !ok {
		return invalid("no usable Signature-Agent for %q", label)
	}

	sigItem, ok := sigs.Get(label).(sfv.Item)
	sig, _ := sigItem.Value.([]byte)
	if !ok || len(sig) != ed25519.SignatureSize {
		return invalid("missing or malformed Signature for %q", label)
	}
	base, err := ri.signatureBase(l)
	if err != nil {
		return Result{Outcome: Unverified, KeyID: keyid, Reason: err.Error()}
	}

	pub, id, err := v.Dirs.lookup(agentURL, keyid, directoryWait)
	if err != nil {
		if errors.Is(err, errBadURL) {
			return invalid("%v", err)
		}
		return Result{Outcome: Unverified, KeyID: keyid, Reason: err.Error()}
	}
	if !ed25519.Verify(pub, []byte(base), sig) {
		return invalid("bad signature")
	}

	// Replay protection, after the signature is known to be good so that
	// garbage cannot fill the memory. The nonce, when present, is the
	// replay key (scoped to agent and key); otherwise the signature itself.
	rk := sha256.New()
	if nonce, ok := l.Params.Get("nonce").(string); ok {
		fmt.Fprintf(rk, "n\x00%s\x00%s\x00%s", id, keyid, nonce)
	} else {
		fmt.Fprintf(rk, "s\x00%x", sig)
	}
	var key [32]byte
	copy(key[:], rk.Sum(nil))
	switch v.remember(key, time.Unix(expires, 0)) {
	case errReplayed:
		return invalid("signature or nonce replayed")
	case errFull:
		return Result{Outcome: Unverified, KeyID: keyid, Reason: "replay cache full"}
	}
	return Result{Outcome: Verified, Agent: id, KeyID: keyid}
}

var (
	errReplayed = errors.New("replayed")
	errFull     = errors.New("full")
)

func (v *Verifier) remember(key [32]byte, expires time.Time) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	if _, ok := v.seen[key]; ok {
		return errReplayed
	}
	if len(v.seen) >= maxReplayEntries && now.Sub(v.sweeps) >= time.Second {
		for k, exp := range v.seen {
			if !now.Before(exp) {
				delete(v.seen, k)
			}
		}
		v.sweeps = now
	}
	if len(v.seen) >= maxReplayEntries {
		return errFull // fail closed: never forget a live entry
	}
	v.seen[key] = expires
	return nil
}
