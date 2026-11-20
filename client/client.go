// Package client is a small library for bots that meet a Spikes402 gate.
//
// It sends a request; if the answer is 402 Payment Required it either
// solves the proof-of-work puzzle or pays the Lightning invoice through a
// Payer, then repeats the request with the proof.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Choffolo/spikes402/pow"
)

// Payer pays a BOLT11 invoice and returns the payment preimage in hex.
//
// TODO: add a real wallet (for example the bot's own phoenixd node via
// POST /payinvoice). A real Payer MUST decode the invoice and refuse to
// pay more than the bot's budget before paying.
type Payer interface {
	Pay(ctx context.Context, invoice string) (preimageHex string, err error)
}

// Mode chooses how the client answers a 402.
type Mode string

const (
	ModePoW Mode = "pow" // do the work
	ModePay Mode = "pay" // pay the invoice
)

// Client fetches URLs protected by Spikes402.
type Client struct {
	HTTP          *http.Client // defaults to http.DefaultClient
	Mode          Mode
	Payer         Payer // needed for ModePay
	MaxDifficulty int   // refuse harder puzzles; 0 means 24
	Logf          func(format string, args ...any)
}

// Challenge is one parsed WWW-Authenticate challenge.
type Challenge struct {
	Scheme string
	Params map[string]string
}

// ParseChallenges reads WWW-Authenticate headers such as
// `L402 macaroon="...", invoice="..."`. Values must be quoted and must
// not contain quotes, which holds for everything Spikes402 sends.
func ParseChallenges(h http.Header) []Challenge {
	var out []Challenge
	for _, v := range h.Values("WWW-Authenticate") {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
		c := Challenge{Scheme: scheme, Params: map[string]string{}}
		for _, part := range strings.Split(rest, ",") {
			k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok {
				c.Params[k] = strings.Trim(val, `"`)
			}
		}
		out = append(out, c)
	}
	return out
}

func find(cs []Challenge, scheme string) (Challenge, bool) {
	for _, c := range cs {
		if strings.EqualFold(c.Scheme, scheme) {
			return c, true
		}
	}
	return Challenge{}, false
}

// Get fetches url, answering a 402 challenge once if needed.
func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	resp, err := c.do(ctx, url, "")
	if err != nil || resp.StatusCode != http.StatusPaymentRequired {
		return resp, err
	}
	challenges := ParseChallenges(resp.Header)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	auth, err := c.answer(ctx, challenges)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, url, auth)
}

// answer turns a 402 challenge into an Authorization header value.
func (c *Client) answer(ctx context.Context, challenges []Challenge) (string, error) {
	switch c.Mode {
	case ModePoW:
		ch, ok := find(challenges, "PoW")
		if !ok {
			return "", errors.New("server offered no proof-of-work challenge")
		}
		difficulty, err := strconv.Atoi(ch.Params["difficulty"])
		if err != nil || ch.Params["algorithm"] != "sha256d" {
			return "", errors.New("unsupported proof-of-work challenge")
		}
		max := c.MaxDifficulty
		if max == 0 {
			max = 24
		}
		if difficulty > max {
			return "", fmt.Errorf("puzzle too hard: difficulty %d > limit %d", difficulty, max)
		}
		c.logf("402: solving proof-of-work (difficulty %d)...", difficulty)
		nonce, err := pow.Solve(ctx, ch.Params["challenge"], difficulty)
		if err != nil {
			return "", err
		}
		c.logf("402: solved, nonce=%d", nonce)
		return "PoW " + ch.Params["challenge"] + ":" + strconv.FormatUint(nonce, 10), nil

	case ModePay:
		ch, ok := find(challenges, "L402")
		if !ok {
			return "", errors.New("server offered no Lightning invoice")
		}
		if c.Payer == nil {
			return "", errors.New("no Payer configured")
		}
		c.logf("402: paying invoice %s", ch.Params["invoice"])
		preimage, err := c.Payer.Pay(ctx, ch.Params["invoice"])
		if err != nil {
			return "", fmt.Errorf("payment failed: %w", err)
		}
		c.logf("402: paid, preimage=%s", preimage)
		return "L402 " + ch.Params["macaroon"] + ":" + preimage, nil
	}
	return "", fmt.Errorf("unknown mode %q", c.Mode)
}

func (c *Client) do(ctx context.Context, url, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	h := c.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	return h.Do(req)
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}
