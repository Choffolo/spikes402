package lightning

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Phoenixd talks to a self-hosted phoenixd node (https://phoenix.acinq.co/server).
// phoenixd is a non-custodial Lightning node by ACINQ: the keys stay on
// the machine that runs it.
//
// Spikes402 only calls POST /createinvoice, which phoenixd allows with the
// limited-access password: that password can receive but not spend.
type Phoenixd struct {
	URL      string // e.g. "http://127.0.0.1:9740"
	Password string // http-password-limited-access from phoenix.conf
	HTTP     *http.Client
}

// CreateInvoice implements Backend.
func (p *Phoenixd) CreateInvoice(ctx context.Context, amountSat int64, memo string, expiry time.Duration) (Invoice, error) {
	if len(memo) > 128 { // phoenixd limit
		memo = memo[:128]
	}
	form := url.Values{
		"amountSat":   {strconv.FormatInt(amountSat, 10)},
		"description": {memo},
	}
	if expiry > 0 {
		form.Set("expirySeconds", strconv.FormatInt(int64(expiry/time.Second), 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.URL, "/")+"/createinvoice", strings.NewReader(form.Encode()))
	if err != nil {
		return Invoice{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("", p.Password)

	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Invoice{}, fmt.Errorf("phoenixd: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Invoice{}, fmt.Errorf("phoenixd: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Invoice{}, fmt.Errorf("phoenixd: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		PaymentHash string `json:"paymentHash"`
		Serialized  string `json:"serialized"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Invoice{}, fmt.Errorf("phoenixd: bad response: %w", err)
	}
	hash, err := hex.DecodeString(out.PaymentHash)
	if err != nil || len(hash) != 32 || out.Serialized == "" {
		return Invoice{}, fmt.Errorf("phoenixd: bad response: %q", body)
	}
	inv := Invoice{PaymentRequest: out.Serialized}
	copy(inv.PaymentHash[:], hash)
	return inv, nil
}
