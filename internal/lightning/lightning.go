// Package lightning hides the Lightning node behind a tiny interface.
//
// Spikes402 only ever needs to create invoices: proof of payment is the
// preimage the payer gets back, which the gate checks on its own. The
// backends here only receive money and never hold anyone's keys.
package lightning

import (
	"context"
	"errors"
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

// IncomingPayment is the status of an invoice we created.
type IncomingPayment struct {
	PaymentHash [32]byte
	Preimage    [32]byte // set when Paid is true
	Paid        bool
	Expired     bool
	ReceivedSat int64
}

// IncomingLookup looks up a received payment by hash. The limited-access
// phoenixd password can do this (GET /payments/incoming/{paymentHash}).
// The fake backend implements it too, so tests and the demo can wait
// for a payment the same way.
type IncomingLookup interface {
	LookupIncoming(ctx context.Context, paymentHash [32]byte) (IncomingPayment, error)
}

// NodeInfo is what the admin dashboard shows about the node.
type NodeInfo struct {
	NodeID       string `json:"node_id"`
	Chain        string `json:"chain"`
	Version      string `json:"version"`
	Channels     int    `json:"channels"`
	BalanceSat   int64  `json:"balance_sat"`
	FeeCreditSat int64  `json:"fee_credit_sat"`
}

// Inspector reads the node's state. Implemented by the backends; needs no
// spending rights.
type Inspector interface {
	NodeInfo(ctx context.Context) (NodeInfo, error)
}

// Withdrawer sends funds from the node to a destination. This is the
// only code that can spend; it is kept apart from Backend on purpose and
// only built when withdrawals are enabled in the config file.
type Withdrawer interface {
	Withdraw(ctx context.Context, destination string, amountSat int64) (reference string, err error)
}

// OutgoingPayment is one payment already sent (or attempted) by the node.
type OutgoingPayment struct {
	PaymentID string
	AmountSat int64
	Paid      bool // true once the money has left
	Completed bool // true once the node has a final outcome (paid or failed)
}

// ErrNotFound is returned by LookupOutgoing when the node has no such payment.
var ErrNotFound = errors.New("outgoing payment not found")

// OutgoingLookup looks up outgoing payments, used to reconcile a withdrawal
// whose outcome was not saved (a crash mid-pay). Implemented by phoenixd
// and the fake backend.
type OutgoingLookup interface {
	LookupOutgoing(ctx context.Context, paymentID string) (OutgoingPayment, error)
	ListOutgoing(ctx context.Context, from, to time.Time) ([]OutgoingPayment, error)
}
