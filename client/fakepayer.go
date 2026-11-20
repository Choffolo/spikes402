package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// FakePayer "pays" invoices of a Spikes402 proxy that runs the fake
// Lightning backend, by calling its development-only endpoint.
// It moves no real money and only works against such a proxy.
type FakePayer struct {
	ProxyURL string // e.g. "http://localhost:8402"
	HTTP     *http.Client
}

// FakePayPath is where a proxy with the fake backend accepts payments.
const FakePayPath = "/.spikes402/fake-pay"

// Pay implements Payer.
func (p FakePayer) Pay(ctx context.Context, invoice string) (string, error) {
	form := url.Values{"invoice": {invoice}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.ProxyURL, "/")+FakePayPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h := p.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	resp, err := h.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fake pay: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return strings.TrimSpace(string(body)), nil
}
