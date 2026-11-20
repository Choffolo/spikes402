// Package lightning hides the Lightning node behind a tiny interface.
//
// Spikes402 only ever needs to create invoices: proof of payment is the
// preimage the payer gets back, which the gate checks on its own. The
// backends here only receive money and never hold anyone's keys.
package lightning

import (
	"context"
	"time"
)

// Invoice is a BOLT11 payment request and the hash it commits to.
type Invoice struct {
	PaymentRequest string   // e.g. "lnbc100n1..."
	PaymentHash    [32]byte // sha256(preimage)
}

// Backend creates invoices on a Lightning node.
type Backend interface {
	CreateInvoice(ctx context.Context, amountSat int64, memo string, expiry time.Duration) (Invoice, error)
}
