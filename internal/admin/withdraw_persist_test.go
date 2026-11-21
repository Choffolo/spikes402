package admin

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/persist"
)

func newWithdrawEnvWithStore(t *testing.T, store IntentStore) *env {
	t.Helper()
	e := newWithdrawEnv(t)
	cfg := e.srv.cfg
	e.srv = New(Options{Config: cfg, ConfigFile: e.file, Gate: e.gate, Monitor: e.mon, Node: e.ln, Withdrawer: e.ln, IntentStore: store})
	e.srv.now = func() time.Time { return e.now }
	e.h = e.srv.Handler()
	return e
}

func restartWithdraw(t *testing.T, e *env, store IntentStore) *env {
	t.Helper()
	cfg := e.srv.cfg
	ln := e.ln
	gate := e.gate
	mon := e.mon
	file := e.file
	now := e.now
	s := New(Options{Config: cfg, ConfigFile: file, Gate: gate, Monitor: mon, Node: ln, Withdrawer: ln, IntentStore: store})
	s.now = func() time.Time { return now }
	return &env{srv: s, h: s.Handler(), gate: gate, ln: ln, mon: mon, now: now, file: file}
}

func TestWithdrawCrashMidPayBlocks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	key := []byte(strings.Repeat("k", 32))
	store, err := persist.OpenIntents(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	e := newWithdrawEnvWithStore(t, store)
	if err := store.Save(&persist.Intent{
		AmountSat: 100, Destination: e.srv.wd.destination, Time: e.now.UTC(), State: persist.IntentPending,
	}); err != nil {
		t.Fatal(err)
	}

	e2 := restartWithdraw(t, e, store)
	if rec, body := e2.call("POST", "/api/withdraw/prepare", adminKey, map[string]any{"amount_sat": 50, "password": wdPass}); rec.Code != http.StatusConflict {
		t.Fatalf("restart after crash must block withdrawals: %d %v", rec.Code, body)
	}
	if n := len(e2.ln.Withdrawals()); n != 0 {
		t.Fatalf("node was paid %d time(s) after a crash mid-withdraw", n)
	}
	_, info := e2.call("GET", "/api/withdraw", adminKey, nil)
	if info["unresolved"] == nil {
		t.Fatalf("expected unresolved intent: %v", info)
	}

	// Admin checks the node (nothing was sent) and clears the block.
	if rec, _ := e2.call("POST", "/api/withdraw/resolve", adminKey, map[string]any{"sent": false, "password": wdPass}); rec.Code != 200 {
		t.Fatalf("resolve: %d", rec.Code)
	}
	if rec, _ := e2.call("POST", "/api/withdraw/prepare", adminKey, map[string]any{"amount_sat": 50, "password": wdPass}); rec.Code != 200 {
		t.Fatalf("unblocked: %d", rec.Code)
	}
}

func TestWithdrawReconcileAfterCrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	key := []byte(strings.Repeat("k", 32))
	store, err := persist.OpenIntents(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	e := newWithdrawEnvWithStore(t, store)
	// The money left, but the process died before recording the outcome.
	if _, err := e.ln.Withdraw(context.Background(), e.srv.wd.destination, 100); err != nil {
		t.Fatal(err)
	}
	paidAt := time.Now().UTC()
	if err := store.Save(&persist.Intent{
		AmountSat: 100, Destination: e.srv.wd.destination, Time: paidAt, State: persist.IntentPending,
	}); err != nil {
		t.Fatal(err)
	}

	e2 := restartWithdraw(t, e, store)
	e2.srv.ReconcileWithdrawals(context.Background())
	_, info := e2.call("GET", "/api/withdraw", adminKey, nil)
	if info["unresolved"] != nil {
		t.Fatalf("should have reconciled as sent: %v", info)
	}
	h := info["history"].([]any)[0].(map[string]any)
	if h["ok"] != true {
		t.Fatalf("history: %v", h)
	}
	// A new withdrawal is allowed (no double pay of the old one).
	if rec, _ := e2.call("POST", "/api/withdraw/prepare", adminKey, map[string]any{"amount_sat": 50, "password": wdPass}); rec.Code != 200 {
		t.Fatalf("unblocked after reconcile: %d", rec.Code)
	}
	if n := len(e2.ln.Withdrawals()); n != 1 {
		t.Fatalf("reconcile must not pay again: %d withdrawals", n)
	}
}

func TestWithdrawCorruptStateBlocks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	key := []byte(strings.Repeat("k", 32))
	if err := persist.Prepare(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "withdraw.intent"), []byte("deadbeef {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := persist.OpenIntents(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	e := newWithdrawEnvWithStore(t, store)
	if rec, _ := e.call("POST", "/api/withdraw/prepare", adminKey, map[string]any{"amount_sat": 50, "password": wdPass}); rec.Code != http.StatusConflict {
		t.Fatalf("corrupt state must block: %d", rec.Code)
	}
	e.srv.ReconcileWithdrawals(context.Background())
	if rec, _ := e.call("POST", "/api/withdraw/prepare", adminKey, map[string]any{"amount_sat": 50, "password": wdPass}); rec.Code != http.StatusConflict {
		t.Fatalf("reconcile must not fail open: %d", rec.Code)
	}
	if rec, _ := e.call("POST", "/api/withdraw/resolve", adminKey, map[string]any{"sent": false, "password": wdPass}); rec.Code != 200 {
		t.Fatalf("admin resolve: %d", rec.Code)
	}
	if rec, _ := e.call("POST", "/api/withdraw/prepare", adminKey, map[string]any{"amount_sat": 50, "password": wdPass}); rec.Code != 200 {
		t.Fatalf("unblocked: %d", rec.Code)
	}
}

func TestWithdrawIntentWrittenBeforePay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	key := []byte(strings.Repeat("k", 32))
	store, err := persist.OpenIntents(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	e := newWithdrawEnvWithStore(t, store)
	// Fail the node after the intent is written: the file must still be there.
	e.srv.withdrawer = &unsureWithdrawer{}
	rec, p := e.call("POST", "/api/withdraw/prepare", adminKey, map[string]any{"amount_sat": 100, "password": wdPass})
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec, _ = e.call("POST", "/api/withdraw/confirm", adminKey, map[string]any{"confirm_id": p["confirm_id"], "amount_sat": 100, "password": wdPass}); rec.Code != 504 {
		t.Fatalf("confirm: %d", rec.Code)
	}
	in, err := store.Load()
	if err != nil || in == nil || in.State != persist.IntentUnknown || in.AmountSat != 100 {
		t.Fatalf("intent after uncertain pay: %+v %v", in, err)
	}
}
