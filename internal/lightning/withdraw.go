package lightning

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// IsLightningAddress reports whether s looks like name@domain.
func IsLightningAddress(s string) bool { return lnAddressRe.MatchString(s) }

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
		return "", fmt.Errorf("phoenixd: %w", err)
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
		return "", fmt.Errorf("phoenixd: unexpected answer: %.200s", text)
	}
	return fmt.Sprintf("payment %s (routing fee %d sat)", sent.PaymentID, sent.FeeSat), nil
}
