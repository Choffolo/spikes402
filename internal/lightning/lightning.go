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
