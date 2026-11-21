package lightning

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Destination kinds.
var (
	lnAddressRe = regexp.MustCompile(`^[a-z0-9._+-]{1,64}@[a-z0-9-]+(\.[a-z0-9-]+)+$`)
	btcAddrRe   = regexp.MustCompile(`^((bc1|tb1|bcrt1)[02-9ac-hj-np-z]{8,87}|[13mn2][1-9A-HJ-NP-Za-km-z]{25,34})$`)
)

// IsLightningAddress reports whether s looks like name@domain: a classic
// Lightning address (LNURL) or a BIP353 address that resolves to a BOLT12
// offer. phoenixd's /paylnaddress handles both (BIP353 first, then LNURL).
func IsLightningAddress(s string) bool { return lnAddressRe.MatchString(s) }

// NormalizeDestination trims spaces and the "₿" that wallets put in front of
// BIP353 addresses (₿name@domain), which is display only.
func NormalizeDestination(s string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "₿"))
}

// Phoenix's fee for an outgoing Lightning payment: 4 sat + 0.4 %.
const (
	RoutingFeeBaseSat = 4
	RoutingFeePPM     = 4_000 // parts per million of the amount
)

// EstimatedFeeSat is the fee to keep aside when sending amountSat to
// destination: the Lightning routing fee, rounded up. It is 0 for on-chain
// destinations, where the node checks the mining fee itself.
func EstimatedFeeSat(destination string, amountSat int64) int64 {
	if !IsLightningAddress(NormalizeDestination(destination)) || amountSat <= 0 {
		return 0
	}
	return RoutingFeeBaseSat + (amountSat*RoutingFeePPM+999_999)/1_000_000
}

// MaxWithdrawSat is the largest amount that, with its estimated fee, fits
// in balanceSat (0 if none does).
func MaxWithdrawSat(destination string, balanceSat int64) int64 {
	a := balanceSat
	if EstimatedFeeSat(destination, 1) > 0 {
		a = (balanceSat - RoutingFeeBaseSat) * 1_000_000 / (1_000_000 + RoutingFeePPM)
	}
	for a > 0 && a+EstimatedFeeSat(destination, a) > balanceSat {
		a--
	}
	return max(a, 0)
}

// ErrUncertain wraps a withdrawal error after which the money may or may
// not have left (a timeout or a broken answer): check the node before
// trying again.
var ErrUncertain = errors.New("outcome unknown")

// IsBitcoinAddress reports whether s looks like an on-chain address. The
// node checks it properly; this only catches obvious typos early.
func IsBitcoinAddress(s string) bool { return btcAddrRe.MatchString(s) }

// PhoenixdWithdrawer sends money out of a phoenixd node. It needs the
// FULL-ACCESS password (http-password in phoenix.conf), which can spend
// everything: it is only created when withdrawals are enabled.
//
// A Lightning address destination uses POST /paylnaddress; an on-chain
// address uses POST /sendtoaddress (a splice-out, paying FeerateSatByte).
type PhoenixdWithdrawer struct {
	URL            string
	FullPassword   string
	FeerateSatByte int64 // on-chain only
	HTTP           *http.Client
}

// Withdraw implements Withdrawer.
func (p *PhoenixdWithdrawer) Withdraw(ctx context.Context, destination string, amountSat int64) (string, error) {
	destination = NormalizeDestination(destination)
	form := url.Values{"amountSat": {strconv.FormatInt(amountSat, 10)}, "address": {destination}}
	path := "/paylnaddress"
	if !IsLightningAddress(destination) {
		if !IsBitcoinAddress(destination) {
			return "", errors.New("withdraw: destination is neither a Lightning nor a Bitcoin address")
		}
		path = "/sendtoaddress"
		form.Set("feerateSatByte", strconv.FormatInt(p.FeerateSatByte, 10))
	} else {
		form.Set("message", "Spikes402 withdrawal")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.URL, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("", p.FullPassword)
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return "", fmt.Errorf("phoenixd: %w", err) // never reached the node
		}
		return "", fmt.Errorf("%w: phoenixd: %v", ErrUncertain, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	text := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("phoenixd: HTTP %d: %.200s", resp.StatusCode, text)
	}
	if path == "/sendtoaddress" {
		// phoenixd answers 200 with either the transaction id or an error text.
		if b, err := hex.DecodeString(text); err == nil && len(b) == 32 {
			return "txid " + text, nil
		}
		return "", fmt.Errorf("phoenixd: %.200s", text)
	}
	var sent struct {
		PaymentID string `json:"paymentId"`
		FeeSat    int64  `json:"routingFeeSat"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(body, &sent); err != nil || sent.PaymentID == "" {
		if sent.Reason != "" {
			return "", fmt.Errorf("phoenixd: payment failed: %.200s", sent.Reason)
		}
		return "", fmt.Errorf("%w: phoenixd: unexpected answer: %.200s", ErrUncertain, text)
	}
	return fmt.Sprintf("payment %s (routing fee %d sat)", sent.PaymentID, sent.FeeSat), nil
}

// LookupOutgoing implements OutgoingLookup: GET /payments/outgoing/{paymentId}.
// A 204 means the node has no such payment (ErrNotFound).
func (p *PhoenixdWithdrawer) LookupOutgoing(ctx context.Context, paymentID string) (OutgoingPayment, error) {
	if paymentID == "" {
		return OutgoingPayment{}, ErrNotFound
	}
	status, body, err := p.get(ctx, "/payments/outgoing/"+url.PathEscape(paymentID), nil)
	if err != nil {
		return OutgoingPayment{}, err
	}
	if status == http.StatusNoContent {
		return OutgoingPayment{}, ErrNotFound
	}
	if status != http.StatusOK {
		return OutgoingPayment{}, fmt.Errorf("phoenixd: HTTP %d: %.200s", status, body)
	}
	return parseOutgoing(body)
}

// ListOutgoing implements OutgoingLookup: GET /payments/outgoing?all=true
// in the time window (Unix milliseconds).
func (p *PhoenixdWithdrawer) ListOutgoing(ctx context.Context, from, to time.Time) ([]OutgoingPayment, error) {
	var out []OutgoingPayment
	const page = 50
	for offset := 0; offset < 500; offset += page {
		q := url.Values{
			"all":    {"true"},
			"limit":  {strconv.Itoa(page)},
			"offset": {strconv.Itoa(offset)},
		}
		if !from.IsZero() {
			q.Set("from", strconv.FormatInt(from.UnixMilli(), 10))
		}
		if !to.IsZero() {
			q.Set("to", strconv.FormatInt(to.UnixMilli(), 10))
		}
		status, body, err := p.get(ctx, "/payments/outgoing", q)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("phoenixd: HTTP %d: %.200s", status, body)
		}
		var raw []json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("phoenixd: unexpected answer: %.200s", body)
		}
		for _, item := range raw {
			pay, err := parseOutgoing(item)
			if err != nil {
				continue
			}
			out = append(out, pay)
		}
		if len(raw) < page {
			break
		}
	}
	return out, nil
}

func (p *PhoenixdWithdrawer) get(ctx context.Context, path string, query url.Values) (int, []byte, error) {
	u := strings.TrimRight(p.URL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth("", p.FullPassword)
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("phoenixd: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}

func parseOutgoing(body []byte) (OutgoingPayment, error) {
	var raw struct {
		PaymentID   string `json:"paymentId"`
		IsPaid      bool   `json:"isPaid"`
		Sent        int64  `json:"sent"`
		CompletedAt *int64 `json:"completedAt"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.PaymentID == "" {
		return OutgoingPayment{}, fmt.Errorf("phoenixd: unexpected answer: %.200s", body)
	}
	return OutgoingPayment{
		PaymentID: raw.PaymentID,
		AmountSat: raw.Sent,
		Paid:      raw.IsPaid,
		Completed: raw.CompletedAt != nil && *raw.CompletedAt > 0,
	}, nil
}
