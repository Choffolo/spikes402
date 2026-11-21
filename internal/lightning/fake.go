package lightning

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory backend for development and tests. Its invoices
// are not real Lightning invoices; they can only be "paid" with Pay,
// which hands back the preimage just like a real wallet would. Like a
// real invoice, each can be paid once and only until it expires.
type Fake struct {
	mu       sync.Mutex
	invoices map[string]fakeInvoice // payment hash (hex) -> invoice
	now      func() time.Time

	balance     int64            // sats received minus withdrawn
	withdrawals []FakeWithdrawal // the last 100
}

type fakeInvoice struct {
	preimage []byte
	amount   int64
	expires  time.Time
	paid     bool
}

// FakeWithdrawal records one Withdraw call.
type FakeWithdrawal struct {
	Destination string
	AmountSat   int64
	PaymentID   string
	Time        time.Time
	Paid        bool
	Completed   bool
}

// NewFake returns an empty fake backend.
func NewFake() *Fake {
	return &Fake{invoices: make(map[string]fakeInvoice), now: time.Now}
}

const fakePrefix = "lnfake"

// CreateInvoice implements Backend.
func (f *Fake) CreateInvoice(_ context.Context, amountSat int64, _ string, expiry time.Duration) (Invoice, error) {
	preimage := make([]byte, 32)
	if _, err := rand.Read(preimage); err != nil {
		return Invoice{}, err
	}
	hash := sha256.Sum256(preimage)
	hashHex := hex.EncodeToString(hash[:])

	f.mu.Lock()
	now := f.now()
	if len(f.invoices) >= 1024 { // keep memory bounded: forget expired invoices
		for k, inv := range f.invoices {
			if !now.Before(inv.expires) {
				delete(f.invoices, k)
			}
		}
	}
	f.invoices[hashHex] = fakeInvoice{preimage: preimage, amount: amountSat, expires: now.Add(expiry)}
	f.mu.Unlock()

	return Invoice{
		PaymentRequest: fmt.Sprintf("%s%dsat1%s", fakePrefix, amountSat, hashHex),
		PaymentHash:    hash,
	}, nil
}

// ErrUnknownInvoice is returned by Pay for invoices it did not create,
// that were already paid, or that expired.
var ErrUnknownInvoice = errors.New("fake lightning: unknown, paid or expired invoice")

// Pay "pays" a fake invoice and returns its preimage in hex.
func (f *Fake) Pay(paymentRequest string) (string, error) {
	if !strings.HasPrefix(paymentRequest, fakePrefix) || len(paymentRequest) < 64 {
		return "", ErrUnknownInvoice
	}
	hashHex := paymentRequest[len(paymentRequest)-64:]
	f.mu.Lock()
	defer f.mu.Unlock()
	inv, ok := f.invoices[hashHex]
	if !ok || inv.paid || !f.now().Before(inv.expires) {
		return "", ErrUnknownInvoice
	}
	inv.paid = true
	f.invoices[hashHex] = inv
	f.balance += inv.amount
	return hex.EncodeToString(inv.preimage), nil
}

// LookupIncoming implements IncomingLookup.
func (f *Fake) LookupIncoming(_ context.Context, paymentHash [32]byte) (IncomingPayment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inv, ok := f.invoices[hex.EncodeToString(paymentHash[:])]
	if !ok {
		return IncomingPayment{}, ErrNotFound
	}
	out := IncomingPayment{PaymentHash: paymentHash, Expired: !f.now().Before(inv.expires), Paid: inv.paid}
	if inv.paid {
		copy(out.Preimage[:], inv.preimage)
		out.ReceivedSat = inv.amount
		out.Expired = false
	}
	return out, nil
}

// NodeInfo implements Inspector.
func (f *Fake) NodeInfo(context.Context) (NodeInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return NodeInfo{NodeID: "fake", Chain: "fake", Version: "fake", BalanceSat: f.balance}, nil
}

// Withdraw implements Withdrawer: it only lowers the fake balance.
func (f *Fake) Withdraw(_ context.Context, destination string, amountSat int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if amountSat < 1 || amountSat > f.balance {
		return "", fmt.Errorf("fake lightning: insufficient balance (%d sat)", f.balance)
	}
	f.balance -= amountSat
	id := fmt.Sprintf("fake-%d", len(f.withdrawals)+1)
	f.withdrawals = append(f.withdrawals, FakeWithdrawal{
		Destination: destination, AmountSat: amountSat,
		PaymentID: id, Time: f.now(), Paid: true, Completed: true,
	})
	if len(f.withdrawals) > 100 {
		f.withdrawals = f.withdrawals[1:]
	}
	return fmt.Sprintf("fake withdrawal #%d", len(f.withdrawals)), nil
}

// LookupOutgoing implements OutgoingLookup.
func (f *Fake) LookupOutgoing(_ context.Context, paymentID string) (OutgoingPayment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.withdrawals {
		if w.PaymentID == paymentID {
			return fakeOutgoing(w), nil
		}
	}
	return OutgoingPayment{}, ErrNotFound
}

// ListOutgoing implements OutgoingLookup.
func (f *Fake) ListOutgoing(_ context.Context, from, to time.Time) ([]OutgoingPayment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []OutgoingPayment
	for _, w := range f.withdrawals {
		if !from.IsZero() && w.Time.Before(from) {
			continue
		}
		if !to.IsZero() && w.Time.After(to) {
			continue
		}
		out = append(out, fakeOutgoing(w))
	}
	return out, nil
}

func fakeOutgoing(w FakeWithdrawal) OutgoingPayment {
	return OutgoingPayment{PaymentID: w.PaymentID, AmountSat: w.AmountSat, Paid: w.Paid, Completed: w.Completed}
}

// Withdrawals returns the recorded withdrawals.
func (f *Fake) Withdrawals() []FakeWithdrawal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeWithdrawal(nil), f.withdrawals...)
}

// PayHandler is a DEVELOPMENT-ONLY HTTP endpoint that pays a fake invoice:
// POST with form field "invoice", get the preimage (hex) back as text.
// It is only mounted when the fake backend is selected.
func (f *Fake) PayHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		preimage, err := f.Pay(r.FormValue("invoice"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, preimage)
	})
}
