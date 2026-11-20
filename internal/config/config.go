// Package config loads the Spikes402 JSON configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Choffolo/spikes402/pow"
)

// Config is the content of the configuration file. See config.example.json.
type Config struct {
	Listen              string    `json:"listen"`                // address to listen on, default ":8402"
	Origin              string    `json:"origin"`                // website to protect, e.g. "http://localhost:8080"
	PriceSats           int64     `json:"price_sats"`            // price per request in sats
	Protect             []string  `json:"protect"`               // path patterns that require payment
	PowDifficulty       int       `json:"pow_difficulty"`        // 0 = derived from price
	ChallengeTTLSeconds int       `json:"challenge_ttl_seconds"` // default 600
	SecretKey           string    `json:"secret_key"`            // or env SPIKES402_SECRET_KEY
	Lightning           Lightning `json:"lightning"`
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
	if c.PowDifficulty == 0 {
		c.PowDifficulty = pow.DifficultyForPrice(c.PriceSats)
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
	if c.PowDifficulty < 1 || c.PowDifficulty > pow.MaxDifficulty {
		return fmt.Errorf(`"pow_difficulty" must be between 1 and %d`, pow.MaxDifficulty)
	}
	if c.ChallengeTTLSeconds < 10 {
		return errors.New(`"challenge_ttl_seconds" must be at least 10`)
	}
	if c.SecretKey != "" && len(c.SecretKey) < 32 {
		return errors.New(`"secret_key" must be at least 32 characters (try: openssl rand -hex 32)`)
	}
	switch c.Lightning.Backend {
	case "fake":
	case "phoenixd":
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

// ChallengeTTL returns the challenge lifetime as a duration.
func (c *Config) ChallengeTTL() time.Duration {
	return time.Duration(c.ChallengeTTLSeconds) * time.Second
}
