package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Choffolo/spikes402/internal/lightning"
	"github.com/Choffolo/spikes402/internal/persist"
)

const (
	withdrawConfirmWindow = 2 * time.Minute
	withdrawMaxFailures   = 5
	withdrawLockout       = 15 * time.Minute
	withdrawTimeout       = 90 * time.Second
	maxWithdrawSats       = 2_100_000_000_000_000
)

// withdrawals implements the two-step withdrawal: prepare (password +
// amount) returns a one-time confirmation id valid for 2 minutes; confirm
// (id + the same amount + the password again) sends the money. The
// destination comes from the config file only.
// IntentStore is the on-disk withdrawal intent (persist.Intents).
type IntentStore interface {
	Load() (*persist.Intent, error)
	Save(*persist.Intent) error
}

type withdrawals struct {
	destination string
	pwHash      [32]byte
	store       IntentStore

	mu          sync.Mutex // also serialises withdrawals
	pending     *pendingWithdrawal
	failures    int
	firstFail   time.Time
	lockedUntil time.Time
	history     []WithdrawalRecord // last 20
	corrupt     bool               // state file could not be verified
}

type pendingWithdrawal struct {
	id      string
	amount  int64
	expires time.Time
}

// WithdrawalRecord is one withdrawal: sent (OK), failed, or Unknown (the
// node did not answer clearly; the admin must check it and resolve).
type WithdrawalRecord struct {
	Time        time.Time `json:"time"`
	AmountSat   int64     `json:"amount_sat"`
	Destination string    `json:"destination"`
	OK          bool      `json:"ok"`
	Unknown     bool      `json:"unknown,omitempty"`
	Result      string    `json:"result"`
}

// unresolvedLocked returns the withdrawal whose outcome is still unknown,
// if any; it must be called with mu held.
func (wd *withdrawals) unresolvedLocked() *WithdrawalRecord {
	for i := range wd.history {
		if wd.history[i].Unknown {
			return &wd.history[i]
		}
	}
	return nil
}

var errUnresolved = fail(http.StatusConflict, "a previous withdrawal has an unknown outcome: check the node's outgoing payments, then resolve it first")

func newWithdrawals(destination, password string, store IntentStore) *withdrawals {
	return &withdrawals{destination: destination, pwHash: sha256.Sum256([]byte(password)), store: store}
}

// checkPassword counts failures; it must be called with mu held.
func (wd *withdrawals) checkPasswordLocked(pw string, now time.Time) error {
	if now.Before(wd.lockedUntil) {
		return fail(http.StatusTooManyRequests, "withdrawals locked after too many wrong passwords; try again after %s", wd.lockedUntil.UTC().Format(time.RFC3339))
	}
	got := sha256.Sum256([]byte(pw))
	if subtle.ConstantTimeCompare(got[:], wd.pwHash[:]) == 1 {
		return nil
	}
	if now.Sub(wd.firstFail) > withdrawLockout {
		wd.failures, wd.firstFail = 0, now
	}
	wd.failures++
	wd.pending = nil
	log.Printf("admin: wrong withdraw password (%d of %d)", wd.failures, withdrawMaxFailures)
	if wd.failures >= withdrawMaxFailures {
		wd.lockedUntil = now.Add(withdrawLockout)
		wd.failures = 0
		log.Printf("admin: withdrawals locked for %s", withdrawLockout)
	}
	return fail(http.StatusForbidden, "wrong withdraw password")
}

func (s *Server) withdrawInfo(_ http.ResponseWriter, _ *http.Request, _ Role) (any, error) {
	if s.wd == nil {
		return map[string]any{"enabled": false}, nil
	}
	s.wd.mu.Lock()
	defer s.wd.mu.Unlock()
	out := map[string]any{"enabled": true, "destination": s.wd.destination, "history": append([]WithdrawalRecord{}, s.wd.history...)}
	if s.now().Before(s.wd.lockedUntil) {
		out["locked_until"] = s.wd.lockedUntil.UTC()
	}
	if u := s.wd.unresolvedLocked(); u != nil {
		out["unresolved"] = *u
	}
	return out, nil
}

func (s *Server) withdrawPrepare(_ http.ResponseWriter, r *http.Request, _ Role) (any, error) {
	if s.wd == nil {
		return nil, fail(http.StatusNotFound, `withdrawals are disabled ("withdraw.enabled")`)
	}
	var in struct {
		AmountSat int64  `json:"amount_sat"`
		Password  string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	wd := s.wd
	wd.mu.Lock()
	defer wd.mu.Unlock()
	now := s.now()
	if err := wd.checkPasswordLocked(in.Password, now); err != nil {
		return nil, err
	}
	if wd.unresolvedLocked() != nil {
		return nil, errUnresolved
	}
	if in.AmountSat < 1 || in.AmountSat > maxWithdrawSats {
		return nil, fail(http.StatusBadRequest, "amount_sat must be at least 1")
	}
	fee := lightning.EstimatedFeeSat(wd.destination, in.AmountSat)
	if info, _ := s.nodeStatus(); info != nil && in.AmountSat+fee > info.BalanceSat {
		return nil, fail(http.StatusBadRequest, "amount + estimated routing fee (%d sat) is more than the balance (%d sat); you can withdraw at most %d sat",
			fee, info.BalanceSat, lightning.MaxWithdrawSat(wd.destination, info.BalanceSat))
	}
	id := make([]byte, 16)
	rand.Read(id)
	wd.pending = &pendingWithdrawal{id: hex.EncodeToString(id), amount: in.AmountSat, expires: now.Add(withdrawConfirmWindow)}
	return map[string]any{
		"confirm_id":  wd.pending.id,
		"amount_sat":  in.AmountSat,
		"fee_sat":     fee, // estimate; 0 for on-chain
		"destination": wd.destination,
		"expires_at":  wd.pending.expires.UTC(),
		"next":        "check the destination and amount, then confirm with the same amount and password",
	}, nil
}

func (s *Server) withdrawConfirm(_ http.ResponseWriter, r *http.Request, _ Role) (any, error) {
	if s.wd == nil {
		return nil, fail(http.StatusNotFound, `withdrawals are disabled ("withdraw.enabled")`)
	}
	var in struct {
		ConfirmID string `json:"confirm_id"`
		AmountSat int64  `json:"amount_sat"`
		Password  string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	wd := s.wd
	wd.mu.Lock()
	defer wd.mu.Unlock()
	now := s.now()
	if err := wd.checkPasswordLocked(in.Password, now); err != nil {
		return nil, err
	}
	p := wd.pending
	wd.pending = nil // one attempt per confirmation id
	if p == nil || subtle.ConstantTimeCompare([]byte(p.id), []byte(in.ConfirmID)) != 1 || !now.Before(p.expires) {
		return nil, fail(http.StatusConflict, "no matching withdrawal to confirm (expired or already used); start again")
	}
	if p.amount != in.AmountSat {
		return nil, fail(http.StatusConflict, "amount does not match the prepared withdrawal; start again")
	}
	if wd.unresolvedLocked() != nil {
		return nil, errUnresolved
	}
	intent := &persist.Intent{
		AmountSat: p.amount, Destination: wd.destination, Time: now.UTC(), State: persist.IntentPending,
	}
	if wd.store != nil {
		if err := wd.store.Save(intent); err != nil {
			log.Printf("admin: could not record withdrawal intent: %v", err)
			return nil, fail(http.StatusInternalServerError, "could not record withdrawal intent; not sending")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), withdrawTimeout)
	defer cancel()
	log.Printf("admin: withdrawing %d sat to %s", p.amount, wd.destination)
	ref, err := s.withdrawer.Withdraw(ctx, wd.destination, p.amount)
	rec := WithdrawalRecord{Time: now.UTC(), AmountSat: p.amount, Destination: wd.destination, OK: err == nil, Result: ref}
	if err != nil {
		rec.Result = truncate(err.Error(), 300)
	}
	if errors.Is(err, lightning.ErrUncertain) {
		rec.Unknown = true
		rec.Result = truncate("unknown — check the node: "+err.Error(), 300)
	}
	s.saveIntentOutcome(intent, &rec, ref)
	wd.history = append(wd.history, rec)
	if len(wd.history) > 20 {
		wd.history = wd.history[1:]
	}
	s.nodeMu.Lock()
	s.nodeAt = time.Time{} // refresh the balance
	s.nodeMu.Unlock()
	if rec.Unknown {
		log.Printf("admin: withdrawal outcome UNKNOWN: %s", rec.Result)
		return nil, fail(http.StatusGatewayTimeout, "%s; new withdrawals are blocked until you resolve it", rec.Result)
	}
	if err != nil {
		log.Printf("admin: withdrawal failed: %s", rec.Result)
		return nil, fail(http.StatusBadGateway, "withdrawal failed: %s", rec.Result)
	}
	log.Printf("admin: withdrawal sent: %s", ref)
	return rec, nil
}

// withdrawResolve records what the admin found on the node for a withdrawal
// with an unknown outcome ("sent": true or false), which unblocks new ones.
func (s *Server) withdrawResolve(_ http.ResponseWriter, r *http.Request, _ Role) (any, error) {
	if s.wd == nil {
		return nil, fail(http.StatusNotFound, `withdrawals are disabled ("withdraw.enabled")`)
	}
	var in struct {
		Sent     *bool  `json:"sent"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if in.Sent == nil {
		return nil, fail(http.StatusBadRequest, `"sent" (true or false) is required`)
	}
	wd := s.wd
	wd.mu.Lock()
	defer wd.mu.Unlock()
	if err := wd.checkPasswordLocked(in.Password, s.now()); err != nil {
		return nil, err
	}
	u := wd.unresolvedLocked()
	if u == nil {
		return nil, fail(http.StatusConflict, "no withdrawal with an unknown outcome")
	}
	u.Unknown, u.OK = false, *in.Sent
	u.Result = truncate(u.Result+map[bool]string{true: " — admin: sent", false: " — admin: not sent"}[*in.Sent], 300)
	if wd.store != nil {
		st := persist.IntentFailed
		if *in.Sent {
			st = persist.IntentSent
		}
		if err := wd.store.Save(&persist.Intent{
			AmountSat: u.AmountSat, Destination: u.Destination, Time: u.Time, State: st, Result: u.Result,
		}); err != nil {
			u.Unknown, u.OK = true, false
			log.Printf("admin: could not save withdrawal resolution: %v", err)
			return nil, fail(http.StatusInternalServerError, "could not save the resolution; withdrawals stay blocked")
		}
	}
	wd.corrupt = false
	log.Printf("admin: unknown withdrawal resolved as sent=%v", *in.Sent)
	return *u, nil
}

func (s *Server) saveIntentOutcome(intent *persist.Intent, rec *WithdrawalRecord, ref string) {
	if rec.Unknown {
		intent.State = persist.IntentUnknown
	} else if rec.OK {
		intent.State = persist.IntentSent
	} else {
		intent.State = persist.IntentFailed
	}
	intent.PaymentID = paymentIDFromRef(ref)
	intent.Result = rec.Result
	if s.wd.store == nil {
		return
	}
	if err := s.wd.store.Save(intent); err != nil {
		log.Printf("ERROR: withdrawal finished but the outcome could not be saved: %v; withdrawals stay blocked until reconciled", err)
		rec.Unknown = true
		rec.OK = false
		rec.Result = truncate("unknown — outcome not saved: "+err.Error(), 300)
	}
}

func paymentIDFromRef(ref string) string {
	// phoenixd Lightning: "payment <uuid> (routing fee N sat)"
	if strings.HasPrefix(ref, "payment ") {
		id := strings.TrimPrefix(ref, "payment ")
		if i := strings.IndexByte(id, ' '); i > 0 {
			id = id[:i]
		}
		return id
	}
	// on-chain: "txid <hex>"
	if strings.HasPrefix(ref, "txid ") {
		return strings.TrimSpace(strings.TrimPrefix(ref, "txid "))
	}
	return ""
}

func recordFromIntent(in *persist.Intent) WithdrawalRecord {
	rec := WithdrawalRecord{Time: in.Time, AmountSat: in.AmountSat, Destination: in.Destination, Result: in.Result}
	switch in.State {
	case persist.IntentSent:
		rec.OK = true
		if rec.Result == "" {
			rec.Result = "sent"
		}
	case persist.IntentFailed:
		if rec.Result == "" {
			rec.Result = "failed"
		}
	default:
		rec.Unknown = true
		if rec.Result == "" {
			rec.Result = "pending — check the node's outgoing payments, then resolve if needed"
		}
	}
	return rec
}

// loadIntent reads the on-disk intent into history. A corrupt or
// unreadable file blocks withdrawals (fail safe).
func (s *Server) loadIntent() {
	if s.wd == nil || s.wd.store == nil {
		return
	}
	in, err := s.wd.store.Load()
	if err != nil {
		log.Printf("ERROR: withdrawal state could not be verified: %v; withdrawals are blocked until an admin resolves this (check the node's outgoing payments first)", err)
		s.wd.history = []WithdrawalRecord{{
			Time:    s.now().UTC(),
			Result:  "withdrawal state could not be verified — check the node's outgoing payments, then resolve",
			Unknown: true,
		}}
		s.wd.corrupt = true
		return
	}
	if in == nil {
		return
	}
	s.wd.history = []WithdrawalRecord{recordFromIntent(in)}
}

// ReconcileWithdrawals looks up an unresolved intent on the node. A
// definite paid or failed payment is recorded; if nothing matches or the
// payment is still in flight, withdrawals stay blocked (fail safe).
func (s *Server) ReconcileWithdrawals(ctx context.Context) {
	if s.wd == nil {
		return
	}
	s.wd.mu.Lock()
	defer s.wd.mu.Unlock()
	u := s.wd.unresolvedLocked()
	if u == nil {
		return
	}
	if s.wd.store == nil {
		return
	}
	if s.wd.corrupt {
		log.Print("ERROR: withdrawal state is unreadable; withdrawals stay blocked until an admin resolves this")
		return
	}
	lookup, ok := s.withdrawer.(lightning.OutgoingLookup)
	if !ok {
		log.Print("admin: unresolved withdrawal on startup; the backend cannot look it up — withdrawals stay blocked until an admin resolves it")
		return
	}
	in, _ := s.wd.store.Load()
	if in == nil || !in.Unresolved() {
		return
	}
	pay, why := matchOutgoing(ctx, lookup, in)
	if pay == nil {
		log.Printf("admin: unresolved withdrawal still blocked: %s", why)
		return
	}
	if pay.Paid && pay.Completed {
		u.Unknown, u.OK = false, true
		u.Result = truncate("reconciled: payment "+pay.PaymentID+" sent", 300)
		in.State, in.PaymentID, in.Result = persist.IntentSent, pay.PaymentID, u.Result
	} else if !pay.Paid && pay.Completed {
		u.Unknown, u.OK = false, false
		u.Result = truncate("reconciled: payment "+pay.PaymentID+" failed", 300)
		in.State, in.PaymentID, in.Result = persist.IntentFailed, pay.PaymentID, u.Result
	} else {
		log.Printf("admin: withdrawal %s is still pending on the node; withdrawals stay blocked", pay.PaymentID)
		return
	}
	if err := s.wd.store.Save(in); err != nil {
		u.Unknown, u.OK = true, false
		log.Printf("ERROR: could not save reconciled withdrawal: %v; withdrawals stay blocked", err)
		return
	}
	log.Printf("admin: withdrawal reconciled as sent=%v (%s)", u.OK, u.Result)
}

func matchOutgoing(ctx context.Context, lookup lightning.OutgoingLookup, in *persist.Intent) (*lightning.OutgoingPayment, string) {
	if in.PaymentID != "" {
		pay, err := lookup.LookupOutgoing(ctx, in.PaymentID)
		if err == nil {
			return &pay, ""
		}
		if !errors.Is(err, lightning.ErrNotFound) {
			return nil, "could not query the node: " + err.Error()
		}
	}
	from := in.Time.Add(-2 * time.Minute)
	to := in.Time.Add(time.Hour)
	if n := time.Now().Add(time.Minute); n.After(to) {
		to = n
	}
	list, err := lookup.ListOutgoing(ctx, from, to)
	if err != nil {
		return nil, "could not list outgoing payments: " + err.Error()
	}
	var matches []lightning.OutgoingPayment
	for _, p := range list {
		if p.AmountSat == in.AmountSat {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return nil, "no matching outgoing payment; check the node, then resolve"
	case 1:
		pay := matches[0]
		return &pay, ""
	default:
		return nil, "several outgoing payments match the amount; check the node, then resolve"
	}
}
