package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, json string) (*Config, error) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(f, []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(f)
}

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8402" || c.PowSecondsPerSat != 10 || c.PowDifficulty != 24 || c.ChallengeTTLSeconds != 600 ||
		c.ChallengesPerMinute != 30 || c.MaxInvoicesPerMinute != 300 || c.MaxPendingInvoices != 10_000 || c.ClientIPHeader != "" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestEnvSecrets(t *testing.T) {
	t.Setenv("SPIKES402_SECRET_KEY", strings.Repeat("k", 32))
	t.Setenv("PHOENIXD_PASSWORD", "pw")
	c, err := load(t, `{"origin":"http://o","price_sats":5,"protect":["/x/**"],"lightning":{"backend":"phoenixd"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Lightning.PhoenixdPassword != "pw" || c.Lightning.PhoenixdURL != "http://127.0.0.1:9740" {
		t.Fatalf("unexpected: %+v", c.Lightning)
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string]string{
		"no origin":         `{"price_sats":5,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"zero price":        `{"origin":"http://o","price_sats":0,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"no patterns":       `{"origin":"http://o","price_sats":5,"lightning":{"backend":"fake"}}`,
		"bad pattern":       `{"origin":"http://o","price_sats":5,"protect":["x"],"lightning":{"backend":"fake"}}`,
		"short secret":      `{"origin":"http://o","price_sats":5,"protect":["/x"],"secret_key":"short","lightning":{"backend":"fake"}}`,
		"typo":              `{"origin":"http://o","price_sat":5,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"no backend":        `{"origin":"http://o","price_sats":5,"protect":["/x"]}`,
		"negative pow":      `{"origin":"http://o","price_sats":5,"protect":["/x"],"pow_seconds_per_sat":-1,"lightning":{"backend":"fake"}}`,
		"bad rate limit":    `{"origin":"http://o","price_sats":5,"protect":["/x"],"challenges_per_minute":-1,"lightning":{"backend":"fake"}}`,
		"header no proxies": `{"origin":"http://o","price_sats":5,"protect":["/x"],"client_ip_header":"X-Forwarded-For","lightning":{"backend":"fake"}}`,
		"bad proxy":         `{"origin":"http://o","price_sats":5,"protect":["/x"],"trusted_proxies":["nope"],"lightning":{"backend":"fake"}}`,
		"phoenixd no pw":    `{"origin":"http://o","price_sats":5,"protect":["/x"],"secret_key":"` + strings.Repeat("k", 32) + `","lightning":{"backend":"phoenixd"}}`,
	}
	for name, json := range cases {
		if _, err := load(t, json); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestPowFromPrice(t *testing.T) {
	c, err := load(t, `{"origin":"http://o","price_sats":10,"protect":["/x"],"pow_seconds_per_sat":1,"lightning":{"backend":"fake"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.PowDifficulty != 24 { // 10 sats x 1 s x 2e6 hashes/s = 2^24.25
		t.Fatalf("got %d", c.PowDifficulty)
	}
	c, err = load(t, `{"origin":"http://o","price_sats":10,"protect":["/x"],"pow_difficulty":12,"lightning":{"backend":"fake"}}`)
	if err != nil || c.PowDifficulty != 12 {
		t.Fatalf("explicit difficulty: %v %+v", err, c)
	}
}

func TestTrustedProxies(t *testing.T) {
	c, err := load(t, `{"origin":"http://o","price_sats":5,"protect":["/x"],"client_ip_header":"X-Forwarded-For",
		"trusted_proxies":["127.0.0.1","::1","10.0.0.0/8"],"lightning":{"backend":"fake"}}`)
	if err != nil {
		t.Fatal(err)
	}
	nets, _ := c.TrustedProxyNets()
	if len(nets) != 3 || nets[0].String() != "127.0.0.1/32" || nets[1].String() != "::1/128" || nets[2].String() != "10.0.0.0/8" {
		t.Fatalf("got %v", nets)
	}
}
