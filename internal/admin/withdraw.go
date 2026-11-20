package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log"
	"net/http"
	"sync"
	"time"
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
type withdrawals struct {
	destination string
	pwHash      [32]byte

	mu          sync.Mutex // also serialises withdrawals
	pending     *pendingWithdrawal
	failures    int
	firstFail   time.Time
	lockedUntil time.Time
	history     []WithdrawalRecord // last 20
}

type pendingWithdrawal struct {
	id      string
	amount  int64
	expires time.Time
}

// WithdrawalRecord is one completed or failed withdrawal.
type WithdrawalRecord struct {
	Time        time.Time `json:"time"`
	AmountSat   int64     `json:"amount_sat"`
	Destination string    `json:"destination"`
	OK          bool      `json:"ok"`
	Result      string    `json:"result"`
}

func newWithdrawals(destination, password string) *withdrawals {
	return &withdrawals{destination: destination, pwHash: sha256.Sum256([]byte(password))}
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
	if in.AmountSat < 1 || in.AmountSat > maxWithdrawSats {
		return nil, fail(http.StatusBadRequest, "amount_sat must be at least 1")
	}
	if info, _ := s.nodeStatus(); info != nil && in.AmountSat > info.BalanceSat {
		return nil, fail(http.StatusBadRequest, "amount is more than the balance (%d sat)", info.BalanceSat)
	}
	id := make([]byte, 16)
	rand.Read(id)
	wd.pending = &pendingWithdrawal{id: hex.EncodeToString(id), amount: in.AmountSat, expires: now.Add(withdrawConfirmWindow)}
	return map[string]any{
		"confirm_id":  wd.pending.id,
		"amount_sat":  in.AmountSat,
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
	ctx, cancel := context.WithTimeout(context.Background(), withdrawTimeout)
	defer cancel()
	log.Printf("admin: withdrawing %d sat to %s", p.amount, wd.destination)
	ref, err := s.withdrawer.Withdraw(ctx, wd.destination, p.amount)
	rec := WithdrawalRecord{Time: now.UTC(), AmountSat: p.amount, Destination: wd.destination, OK: err == nil, Result: ref}
	if err != nil {
		rec.Result = truncate(err.Error(), 300)
	}
	wd.history = append(wd.history, rec)
	if len(wd.history) > 20 {
		wd.history = wd.history[1:]
	}
	s.nodeMu.Lock()
	s.nodeAt = time.Time{} // refresh the balance
	s.nodeMu.Unlock()
	if err != nil {
		log.Printf("admin: withdrawal failed: %s", rec.Result)
		return nil, fail(http.StatusBadGateway, "withdrawal failed: %s", rec.Result)
	}
	log.Printf("admin: withdrawal sent: %s", ref)
	return rec, nil
}
