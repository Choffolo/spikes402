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
	if c.Listen != "127.0.0.1:8402" || c.PowSecondsPerSat != 10 || c.PoW(c.PriceSats).Difficulty != 24 || c.ChallengeTTLSeconds != 600 ||
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
		"no origin":           `{"price_sats":5,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"zero price":          `{"origin":"http://o","price_sats":0,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"huge price":          `{"origin":"http://o","price_sats":1000001,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"bad prices pattern":  `{"origin":"http://o","price_sats":5,"protect":["/x"],"prices":[{"pattern":"x","price_sats":2}],"lightning":{"backend":"fake"}}`,
		"zero path price":     `{"origin":"http://o","price_sats":5,"protect":["/x"],"prices":[{"pattern":"/x","price_sats":0}],"lightning":{"backend":"fake"}}`,
		"huge path price":     `{"origin":"http://o","price_sats":5,"protect":["/x"],"prices":[{"pattern":"/x","price_sats":1000001}],"lightning":{"backend":"fake"}}`,
		"no patterns":         `{"origin":"http://o","price_sats":5,"lightning":{"backend":"fake"}}`,
		"bad pattern":         `{"origin":"http://o","price_sats":5,"protect":["x"],"lightning":{"backend":"fake"}}`,
		"short secret":        `{"origin":"http://o","price_sats":5,"protect":["/x"],"secret_key":"short","lightning":{"backend":"fake"}}`,
		"typo":                `{"origin":"http://o","price_sat":5,"protect":["/x"],"lightning":{"backend":"fake"}}`,
		"no backend":          `{"origin":"http://o","price_sats":5,"protect":["/x"]}`,
		"negative pow":        `{"origin":"http://o","price_sats":5,"protect":["/x"],"pow_seconds_per_sat":-1,"lightning":{"backend":"fake"}}`,
		"bad rate limit":      `{"origin":"http://o","price_sats":5,"protect":["/x"],"challenges_per_minute":-1,"lightning":{"backend":"fake"}}`,
		"header no proxies":   `{"origin":"http://o","price_sats":5,"protect":["/x"],"client_ip_header":"X-Forwarded-For","lightning":{"backend":"fake"}}`,
		"preserve no proxies": `{"origin":"http://o","price_sats":5,"protect":["/x"],"preserve_host":true,"lightning":{"backend":"fake"}}`,
		"bad proxy":           `{"origin":"http://o","price_sats":5,"protect":["/x"],"trusted_proxies":["nope"],"lightning":{"backend":"fake"}}`,
		"phoenixd no pw":      `{"origin":"http://o","price_sats":5,"protect":["/x"],"secret_key":"` + strings.Repeat("k", 32) + `","lightning":{"backend":"phoenixd"}}`,
		"bad algorithm":       `{"origin":"http://o","price_sats":5,"protect":["/x"],"pow_algorithm":"scrypt","lightning":{"backend":"fake"}}`,
		"argon memory bomb":   `{"origin":"http://o","price_sats":5,"protect":["/x"],"pow_algorithm":"argon2id","argon2_memory_mib":4096,"lightning":{"backend":"fake"}}`,
		"argon too little":    `{"origin":"http://o","price_sats":5,"protect":["/x"],"pow_algorithm":"argon2id","argon2_memory_mib":1,"lightning":{"backend":"fake"}}`,
		"argon too hard":      `{"origin":"http://o","price_sats":5,"protect":["/x"],"pow_algorithm":"argon2id","pow_difficulty":30,"lightning":{"backend":"fake"}}`,
		"memory w/o argon":    `{"origin":"http://o","price_sats":5,"protect":["/x"],"argon2_memory_mib":16,"lightning":{"backend":"fake"}}`,
		"argon over budget":   `{"origin":"http://o","price_sats":5,"protect":["/x"],"pow_algorithm":"argon2id","argon2_memory_mib":64,"argon2_max_memory_mib":32,"lightning":{"backend":"fake"}}`,
		"argon budget huge":   `{"origin":"http://o","price_sats":5,"protect":["/x"],"pow_algorithm":"argon2id","argon2_max_memory_mib":99999,"lightning":{"backend":"fake"}}`,
		"negative pow diff":   `{"origin":"http://o","price_sats":5,"protect":["/x"],"pow_difficulty":-1,"lightning":{"backend":"fake"}}`,
		"bad policy":          `{"origin":"http://o","price_sats":5,"protect":["/x"],"bot_policies":{"impostor":{"action":"ban"}},"lightning":{"backend":"fake"}}`,
		"bad multiplier":      `{"origin":"http://o","price_sats":5,"protect":["/x"],"bot_policies":{"impostor":{"action":"pay","price_multiplier":-2}},"lightning":{"backend":"fake"}}`,
		"bot without name":    `{"origin":"http://o","price_sats":5,"protect":["/x"],"known_bots":[{"user_agent":"x"}],"lightning":{"backend":"fake"}}`,
		"bot list over http":  `{"origin":"http://o","price_sats":5,"protect":["/x"],"known_bots":[{"name":"x","user_agent":"x","ip_ranges_url":"http://x"}],"lightning":{"backend":"fake"}}`,
		"dev mode in prod":    `{"origin":"http://o","price_sats":5,"protect":["/x"],"web_bot_auth_dev_mode":true,"secret_key":"` + strings.Repeat("k", 32) + `","lightning":{"backend":"phoenixd","phoenixd_password":"p"}}`,
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
	if d := c.PoW(c.PriceSats).Difficulty; d != 24 { // 10 sats x 1 s x 2e6 hashes/s = 2^24.25
		t.Fatalf("got %d", d)
	}
	c, err = load(t, `{"origin":"http://o","price_sats":10,"protect":["/x"],"pow_difficulty":12,"lightning":{"backend":"fake"}}`)
	if err != nil || c.PoW(c.PriceSats).Difficulty != 12 {
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

func TestArgon2id(t *testing.T) {
	c, err := load(t, `{"origin":"http://o","price_sats":1,"protect":["/x"],"pow_algorithm":"argon2id","lightning":{"backend":"fake"}}`)
	if err != nil {
		t.Fatal(err)
	}
	p := c.PoW(1)
	if p.Algorithm != "argon2id" || p.MemoryMiB != 16 || p.Difficulty != 10 { // 10 s / (16 MiB / 1500 MiB/s) = 2^9.9
		t.Fatalf("got %+v", p)
	}
	if p := c.PoW(10); p.Difficulty != 13 { // ten times the price, ~2^3.3 more work
		t.Fatalf("got %+v", p)
	}
	if c.Argon2MaxMemoryMiB != 256 {
		t.Fatalf("default budget %d, want 256", c.Argon2MaxMemoryMiB)
	}
}

func TestBotDefaults(t *testing.T) {
	c, err := load(t, `{"origin":"http://o","price_sats":1,"protect":["/x"],"lightning":{"backend":"fake"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !c.IdentifyBotsOn() || len(c.Bots()) == 0 || len(c.BotPolicies) != 0 || c.WebBotAuthDevMode {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	c, err = load(t, `{"origin":"http://o","price_sats":1,"protect":["/x"],"identify_bots":false,
		"known_bots":[{"name":"MyBot","user_agent":"MyBot","reverse_dns":["example.org"]}],
		"bot_policies":{"impostor":{"action":"pow-only"},"verified":{"action":"pay","price_multiplier":2}},"lightning":{"backend":"fake"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.IdentifyBotsOn() || c.Bots()[0].Name != "MyBot" || c.BotPolicies["impostor"].PriceMultiplier != 1 || c.BotPolicies["verified"].PriceMultiplier != 2 {
		t.Fatalf("got %+v", c)
	}
}

func TestDangerousConfigRefused(t *testing.T) {
	const b = `"origin":"http://o","price_sats":5,"protect":["/x/**"],"lightning":{"backend":"fake"}`
	for name, extra := range map[string]string{
		"trust every IPv4":        `"client_ip_header":"X-Forwarded-For","trusted_proxies":["0.0.0.0/0"]`,
		"trust every IPv6":        `"client_ip_header":"X-Forwarded-For","trusted_proxies":["10.0.0.1","::/0"]`,
		"admin open to all":       `"admin":{"key":"` + strings.Repeat("a", 32) + `","listen":"0.0.0.0:8404","allowed_ips":["0.0.0.0/0"]}`,
		"fake backend, public":    `"listen":":8402"`,
		"fake backend, LAN":       `"listen":"192.168.1.5:8402"`,
		"fake backend, all v6":    `"listen":"[::]:8402"`,
		"trusted agents, no host": `"web_bot_auth_trusted_agents":["example.com"]`,
		"bad trusted agent":       `"web_bot_auth_trusted_agents":["https://example.com/x"],"public_hosts":["mysite.example"]`,
		"bad public host":         `"public_hosts":["mysite.example:443"]`,
		"zero connections":        `"max_connections":-1`,
	} {
		if _, err := load(t, `{`+b+`,`+extra+`}`); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The explicit override accepts the dangerous ones.
	for _, extra := range []string{`"trusted_proxies":["0.0.0.0/0"],"client_ip_header":"X-Real-IP"`, `"listen":":8402"`} {
		if _, err := load(t, `{`+b+`,"unsafe_allow_dangerous_config":true,`+extra+`}`); err != nil {
			t.Errorf("override: %v", err)
		}
	}
	// Normal settings stay accepted, and the fake backend defaults to localhost.
	c, err := load(t, `{`+b+`,"client_ip_header":"X-Forwarded-For","trusted_proxies":["10.0.0.0/8","::1"],"public_hosts":["MySite.Example."],"web_bot_auth_trusted_agents":["Bots.Example"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8402" || c.PublicHosts[0] != "mysite.example" || c.WebBotAuthTrustedAgents[0] != "bots.example" ||
		c.MaxConnections != 10_000 || c.ReadTimeoutSeconds != 60 || c.WriteTimeoutSeconds != 300 || c.ProtectCaseSensitive {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestPathPrices(t *testing.T) {
	c, err := load(t, `{"origin":"http://o","price_sats":5,"protect":["/articles/**","/premium/**"],`+
		`"prices":[{"pattern":"/premium/special/**","price_sats":1},{"pattern":"/premium/**","price_sats":20}],`+
		`"lightning":{"backend":"fake"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.PriceSats != 5 || len(c.Prices) != 2 || c.Prices[0].PriceSats != 1 || c.Prices[1].Pattern != "/premium/**" {
		t.Fatalf("got %+v", c.Prices)
	}
	if pub := c.Public(); len(pub.Prices) != 2 {
		t.Fatalf("public prices: %+v", pub.Prices)
	}
}

func TestPriceRuleLimits(t *testing.T) {
	raw := `{"origin":"http://o","price_sats":5,"protect":["/x"],"prices":[` +
		strings.Repeat(`{"pattern":"/x","price_sats":1},`, maxPriceRules) +
		`{"pattern":"/x","price_sats":1}],"lightning":{"backend":"fake"}}`
	if _, err := load(t, raw); err == nil {
		t.Fatal("accepted too many price rules")
	}
	if _, err := load(t, `{"origin":"http://o","price_sats":5,"protect":["/x"],"prices":[{"pattern":"/[","price_sats":2}],"lightning":{"backend":"fake"}}`); err == nil {
		t.Fatal("accepted a bad prices glob")
	}
}
