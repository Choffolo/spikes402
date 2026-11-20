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
// which hands back the preimage just like a real wallet would.
type Fake struct {
	mu        sync.Mutex
	preimages map[string][]byte // payment hash (hex) -> preimage
}

// NewFake returns an empty fake backend.
func NewFake() *Fake {
	return &Fake{preimages: make(map[string][]byte)}
}

const fakePrefix = "lnfake"

// CreateInvoice implements Backend.
func (f *Fake) CreateInvoice(_ context.Context, amountSat int64, _ string, _ time.Duration) (Invoice, error) {
	preimage := make([]byte, 32)
	if _, err := rand.Read(preimage); err != nil {
		return Invoice{}, err
	}
	hash := sha256.Sum256(preimage)
	hashHex := hex.EncodeToString(hash[:])

	f.mu.Lock()
	f.preimages[hashHex] = preimage
	f.mu.Unlock()

	return Invoice{
		PaymentRequest: fmt.Sprintf("%s%dsat1%s", fakePrefix, amountSat, hashHex),
		PaymentHash:    hash,
	}, nil
}

// ErrUnknownInvoice is returned by Pay for invoices it did not create.
var ErrUnknownInvoice = errors.New("fake lightning: unknown invoice")

// Pay "pays" a fake invoice and returns its preimage in hex.
func (f *Fake) Pay(paymentRequest string) (string, error) {
	if !strings.HasPrefix(paymentRequest, fakePrefix) || len(paymentRequest) < 64 {
		return "", ErrUnknownInvoice
	}
	hashHex := paymentRequest[len(paymentRequest)-64:]
	f.mu.Lock()
	preimage, ok := f.preimages[hashHex]
	f.mu.Unlock()
	if !ok {
		return "", ErrUnknownInvoice
	}
	return hex.EncodeToString(preimage), nil
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
