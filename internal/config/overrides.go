package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Choffolo/spikes402/internal/gate"
)

// OverridesPath is the sidecar file next to the main config: for
// "config.json" it is "config.overrides.json". The main file is never
// rewritten; only these webmaster settings are stored here.
func OverridesPath(configFile string) string {
	dir, name := filepath.Split(configFile)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if ext == "" {
		ext = ".json"
	}
	return filepath.Join(dir, base+".overrides"+ext)
}

// overrideFile is the on-disk snapshot of dashboard-editable fields.
// Secrets, listen addresses, Lightning, proxies and node invoice limits
// are not present and are rejected if someone writes them here.
type overrideFile struct {
	PriceSats           int64                  `json:"price_sats"`
	Prices              []gate.PriceRule       `json:"prices"`
	Protect             []string               `json:"protect"`
	PowSecondsPerSat    float64                `json:"pow_seconds_per_sat"`
	ChallengesPerMinute int                    `json:"challenges_per_minute"`
	BotPolicies         map[string]gate.Policy `json:"bot_policies"`
	Paused              bool                   `json:"paused"`
}

func (o overrideFile) changes() Changes {
	prices := append([]gate.PriceRule(nil), o.Prices...)
	protect := append([]string(nil), o.Protect...)
	policies := o.BotPolicies
	if policies == nil {
		policies = map[string]gate.Policy{}
	}
	sps, cpm := o.PowSecondsPerSat, o.ChallengesPerMinute
	paused := o.Paused
	price := o.PriceSats
	return Changes{
		PriceSats:           &price,
		Prices:              &prices,
		Protect:             &protect,
		PowSecondsPerSat:    &sps,
		ChallengesPerMinute: &cpm,
		BotPolicies:         &policies,
		Paused:              &paused,
	}
}

func snapshot(c *Config) overrideFile {
	policies := c.BotPolicies
	if policies == nil {
		policies = map[string]gate.Policy{}
	}
	return overrideFile{
		PriceSats:           c.PriceSats,
		Prices:              append([]gate.PriceRule(nil), c.Prices...),
		Protect:             append([]string(nil), c.Protect...),
		PowSecondsPerSat:    c.PowSecondsPerSat,
		ChallengesPerMinute: c.ChallengesPerMinute,
		BotPolicies:         policies,
		Paused:              c.Paused,
	}
}

func applyOverridesFile(c *Config, path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	var o overrideFile
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	n, err := c.Apply(o.changes())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return n, nil
}

// WriteOverrides writes the dashboard-editable fields of c to path
// (mode 0600) using a same-directory rename so a crash cannot leave a
// half-written file. The main config file is not touched.
func WriteOverrides(path string, c *Config) error {
	if path == "" {
		return fmt.Errorf("overrides path is empty")
	}
	data, err := json.MarshalIndent(snapshot(c), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".overrides-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}
