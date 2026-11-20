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
	if c.Listen != ":8402" || c.PowDifficulty != 20 || c.ChallengeTTLSeconds != 600 {
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
		"no origin":      `{"price_sats":5,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"zero price":     `{"origin":"http://o","price_sats":0,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"no patterns":    `{"origin":"http://o","price_sats":5,"lightning":{"backend":"fake"}}`,
		"bad pattern":    `{"origin":"http://o","price_sats":5,"protect":["x"],"lightning":{"backend":"fake"}}`,
		"short secret":   `{"origin":"http://o","price_sats":5,"protect":["/x"],"secret_key":"short","lightning":{"backend":"fake"}}`,
		"typo":           `{"origin":"http://o","price_sat":5,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"no backend":     `{"origin":"http://o","price_sats":5,"protect":["/x"]}`,
		"phoenixd no pw": `{"origin":"http://o","price_sats":5,"protect":["/x"],"secret_key":"` + strings.Repeat("k", 32) + `","lightning":{"backend":"phoenixd"}}`,
	}
	for name, json := range cases {
		if _, err := load(t, json); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
