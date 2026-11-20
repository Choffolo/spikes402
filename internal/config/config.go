// Package config loads the Spikes402 JSON configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Choffolo/spikes402/internal/botid"
	"github.com/Choffolo/spikes402/internal/gate"
	"github.com/Choffolo/spikes402/pow"
)

// Config is the content of the configuration file. See config.example.json.
type Config struct {
	Listen               string    `json:"listen"`                  // address to listen on, default ":8402"
	Origin               string    `json:"origin"`                  // website to protect, e.g. "http://localhost:8080"
	PriceSats            int64     `json:"price_sats"`              // price per request in sats
	Protect              []string  `json:"protect"`                 // path patterns that require payment
	PowSecondsPerSat     float64   `json:"pow_seconds_per_sat"`     // CPU-core seconds of work per sat, default 10
	PowDifficulty        int       `json:"pow_difficulty"`          // 0 = derived from price and pow_seconds_per_sat
	PowAlgorithm         string    `json:"pow_algorithm"`           // "sha256d" (default) or "argon2id"
	Argon2MemoryMiB      int       `json:"argon2_memory_mib"`       // argon2id memory per attempt, default 16
	ChallengeTTLSeconds  int       `json:"challenge_ttl_seconds"`   // default 600
	ChallengesPerMinute  int       `json:"challenges_per_minute"`   // per client, default 30
	MaxInvoicesPerMinute int       `json:"max_invoices_per_minute"` // all clients together, default 300
	MaxPendingInvoices   int       `json:"max_pending_invoices"`    // default 10000
	ClientIPHeader       string    `json:"client_ip_header"`        // e.g. "X-Forwarded-For", default "" (use the TCP address)
	TrustedProxies       []string  `json:"trusted_proxies"`         // IPs/CIDRs allowed to set client_ip_header
	SecretKey            string    `json:"secret_key"`              // or env SPIKES402_SECRET_KEY
	Lightning            Lightning `json:"lightning"`

	// Bot identification. Policies are off by default: everyone pays the same.
	IdentifyBots      *bool                  `json:"identify_bots"`         // default true
	KnownBots         []botid.Bot            `json:"known_bots"`            // default: botid.DefaultBots
	BotPolicies       map[string]gate.Policy `json:"bot_policies"`          // default: none
	WebBotAuthDevMode bool                   `json:"web_bot_auth_dev_mode"` // accept http/localhost key directories (fake backend only)
}

// Lightning selects and configures the Lightning backend.
type Lightning struct {
	Backend          string `json:"backend"`           // "fake" or "phoenixd"
	PhoenixdURL      string `json:"phoenixd_url"`      // default "http://127.0.0.1:9740"
	PhoenixdPassword string `json:"phoenixd_password"` // or env PHOENIXD_PASSWORD
}

// Load reads, completes and validates a configuration file. Secrets may
// come from the environment so they never have to be written in the file.
func Load(file string) (*Config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields() // catch typos in option names
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if v := os.Getenv("SPIKES402_SECRET_KEY"); v != "" {
		c.SecretKey = v
	}
	if v := os.Getenv("PHOENIXD_PASSWORD"); v != "" {
		c.Lightning.PhoenixdPassword = v
	}
	if err := c.complete(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &c, nil
}

func (c *Config) complete() error {
	if c.Listen == "" {
		c.Listen = ":8402"
	}
	if c.ChallengeTTLSeconds == 0 {
		c.ChallengeTTLSeconds = 600
	}
	if c.PowSecondsPerSat == 0 {
		c.PowSecondsPerSat = 10
	}
	if c.ChallengesPerMinute == 0 {
		c.ChallengesPerMinute = 30
	}
	if c.MaxInvoicesPerMinute == 0 {
		c.MaxInvoicesPerMinute = 300
	}
	if c.MaxPendingInvoices == 0 {
		c.MaxPendingInvoices = 10_000
	}
	if c.Lightning.Backend == "phoenixd" && c.Lightning.PhoenixdURL == "" {
		c.Lightning.PhoenixdURL = "http://127.0.0.1:9740"
	}

	u, err := url.Parse(c.Origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New(`"origin" must be an http(s) URL`)
	}
	if c.PriceSats < 1 {
		return errors.New(`"price_sats" must be at least 1`)
	}
	if len(c.Protect) == 0 {
		return errors.New(`"protect" needs at least one path pattern, e.g. "/premium/**"`)
	}
	for _, p := range c.Protect {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf(`protect pattern %q must start with "/"`, p)
		}
		if _, err := path.Match(strings.TrimSuffix(p, "/**"), "/"); err != nil {
			return fmt.Errorf("protect pattern %q: %w", p, err)
		}
	}
	if c.PowSecondsPerSat < 0 {
		return errors.New(`"pow_seconds_per_sat" must be positive`)
	}
	if c.PowAlgorithm == "" {
		c.PowAlgorithm = pow.SHA256d
	}
	if c.PowAlgorithm == pow.Argon2id && c.Argon2MemoryMiB == 0 {
		c.Argon2MemoryMiB = 16
	}
	switch c.PowAlgorithm {
	case pow.SHA256d:
		if c.Argon2MemoryMiB != 0 {
			return errors.New(`"argon2_memory_mib" only applies with "pow_algorithm": "argon2id"`)
		}
		if c.PowDifficulty < 0 || c.PowDifficulty > pow.MaxDifficulty {
			return fmt.Errorf(`"pow_difficulty" must be between 1 and %d`, pow.MaxDifficulty)
		}
	case pow.Argon2id:
		if c.Argon2MemoryMiB < pow.MinArgonMemoryMiB || c.Argon2MemoryMiB > pow.MaxArgonMemoryMiB {
			return fmt.Errorf(`"argon2_memory_mib" must be between %d and %d`, pow.MinArgonMemoryMiB, pow.MaxArgonMemoryMiB)
		}
		if c.PowDifficulty < 0 || c.PowDifficulty > pow.MaxArgonDifficulty {
			return fmt.Errorf(`"pow_difficulty" must be between 1 and %d with argon2id`, pow.MaxArgonDifficulty)
		}
	default:
		return errors.New(`"pow_algorithm" must be "sha256d" or "argon2id"`)
	}
	for k, p := range c.BotPolicies {
		if p.PriceMultiplier == 0 {
			p.PriceMultiplier = 1
			c.BotPolicies[k] = p
		}
		if err := p.Validate(); err != nil {
			return fmt.Errorf("bot_policies %q: %w", k, err)
		}
	}
	for _, b := range c.KnownBots {
		if b.Name == "" || b.UserAgent == "" {
			return errors.New(`each of "known_bots" needs a "name" and a "user_agent"`)
		}
		if b.IPRangesURL != "" && !strings.HasPrefix(b.IPRangesURL, "https://") {
			return fmt.Errorf("known_bots %q: ip_ranges_url must be https", b.Name)
		}
	}
	if c.ChallengeTTLSeconds < 10 {
		return errors.New(`"challenge_ttl_seconds" must be at least 10`)
	}
	if c.ChallengesPerMinute < 1 || c.MaxInvoicesPerMinute < 1 || c.MaxPendingInvoices < 1 {
		return errors.New(`"challenges_per_minute", "max_invoices_per_minute" and "max_pending_invoices" must be at least 1`)
	}
	if _, err := c.TrustedProxyNets(); err != nil {
		return err
	}
	if c.ClientIPHeader != "" && len(c.TrustedProxies) == 0 {
		return errors.New(`"client_ip_header" needs "trusted_proxies": the addresses of the proxies allowed to set it`)
	}
	if c.SecretKey != "" && len(c.SecretKey) < 32 {
		return errors.New(`"secret_key" must be at least 32 characters (try: openssl rand -hex 32)`)
	}
	switch c.Lightning.Backend {
	case "fake":
	case "phoenixd":
		if c.WebBotAuthDevMode {
			return errors.New(`"web_bot_auth_dev_mode" is for local testing only and cannot be used with a real Lightning backend`)
		}
		if c.SecretKey == "" {
			return errors.New(`"secret_key" (or SPIKES402_SECRET_KEY) is required with a real Lightning backend`)
		}
		if c.Lightning.PhoenixdPassword == "" {
			return errors.New(`phoenixd needs "phoenixd_password" (or PHOENIXD_PASSWORD)`)
		}
	default:
		return errors.New(`"lightning.backend" must be "fake" or "phoenixd"`)
	}
	return nil
}

// TrustedProxyNets parses trusted_proxies ("10.0.0.1", "10.0.0.0/8", "::1").
func (c *Config) TrustedProxyNets() ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, p := range c.TrustedProxies {
		if !strings.Contains(p, "/") {
			if ip := net.ParseIP(p); ip != nil && ip.To4() != nil {
				p += "/32"
			} else {
				p += "/128"
			}
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("trusted_proxies: %q is not an IP address or CIDR range", p)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// IdentifyBotsOn reports whether bot identification is enabled.
func (c *Config) IdentifyBotsOn() bool {
	return c.IdentifyBots == nil || *c.IdentifyBots
}

// Bots returns the list of known bots to check.
func (c *Config) Bots() []botid.Bot {
	if c.KnownBots != nil {
		return c.KnownBots
	}
	return botid.DefaultBots
}

// PoW returns the puzzle for a price: the fixed pow_difficulty if set,
// otherwise derived from the price and pow_seconds_per_sat.
func (c *Config) PoW(priceSats int64) pow.Params {
	if c.PowDifficulty > 0 {
		return pow.Params{Algorithm: c.PowAlgorithm, Difficulty: c.PowDifficulty, MemoryMiB: c.Argon2MemoryMiB}
	}
	return pow.ForPrice(c.PowAlgorithm, c.Argon2MemoryMiB, priceSats, c.PowSecondsPerSat)
}

// ChallengeTTL returns the challenge lifetime as a duration.
func (c *Config) ChallengeTTL() time.Duration {
	return time.Duration(c.ChallengeTTLSeconds) * time.Second
}
