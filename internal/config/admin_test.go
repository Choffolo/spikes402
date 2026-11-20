package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Choffolo/spikes402/internal/gate"
)

var (
	adminKey = strings.Repeat("a", 32)
	aiKey    = strings.Repeat("b", 32)
	secret   = strings.Repeat("k", 32)
)

const base = `"origin":"http://o","price_sats":5,"protect":["/x"]`

func TestAdminDefaults(t *testing.T) {
	c, err := load(t, `{`+base+`,"lightning":{"backend":"fake"}}`)
	if err != nil || c.AdminEnabled() || c.Withdraw.Enabled {
		t.Fatalf("%v %+v", err, c.Admin)
	}
	t.Setenv("SPIKES402_ADMIN_KEY", adminKey)
	c, err = load(t, `{`+base+`,"lightning":{"backend":"fake"}}`)
	if err != nil || !c.AdminEnabled() || c.Admin.Listen != "127.0.0.1:8404" || c.Admin.VisitorIPs != "hash" {
		t.Fatalf("%v %+v", err, c.Admin)
	}
}

func TestAdminInvalid(t *testing.T) {
	cases := map[string]string{
		"short key":          `"admin":{"key":"short"}`,
		"ai key without key": `"admin":{"ai_key":"` + aiKey + `"}`,
		"ai key = key":       `"admin":{"key":"` + adminKey + `","ai_key":"` + adminKey + `"}`,
		"apply without ai":   `"admin":{"key":"` + adminKey + `","ai_can_apply":true}`,
		"public no allow":    `"admin":{"key":"` + adminKey + `","listen":"0.0.0.0:8404"}`,
		"bad allow":          `"admin":{"key":"` + adminKey + `","listen":"0.0.0.0:8404","allowed_ips":["x"]}`,
		"bad privacy":        `"admin":{"key":"` + adminKey + `","visitor_ips":"all"}`,
		"key = secret":       `"secret_key":"` + adminKey + `","admin":{"key":"` + adminKey + `"}`,
	}
	for name, extra := range cases {
		if _, err := load(t, `{`+base+`,`+extra+`,"lightning":{"backend":"fake"}}`); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := load(t, `{`+base+`,"admin":{"key":"`+adminKey+`","listen":"0.0.0.0:8404","allowed_ips":["10.0.0.0/8"]},"lightning":{"backend":"fake"}}`); err != nil {
		t.Fatal(err)
	}
}

func TestWithdrawConfig(t *testing.T) {
	admin := `"admin":{"key":"` + adminKey + `","ai_key":"` + aiKey + `"}`
	phx := `"secret_key":"` + secret + `","lightning":{"backend":"phoenixd","phoenixd_password":"limitedpassword"}`
	pw := strings.Repeat("w", 16)
	bad := map[string]string{
		"no admin":         `"withdraw":{"enabled":true,"destination":"me@x.y","password":"` + pw + `"},"lightning":{"backend":"fake"}`,
		"bad destination":  admin + `,"withdraw":{"enabled":true,"destination":"lnbc1xyz","password":"` + pw + `"},"lightning":{"backend":"fake"}`,
		"short password":   admin + `,"withdraw":{"enabled":true,"destination":"me@x.y","password":"short"},"lightning":{"backend":"fake"}`,
		"password = admin": admin + `,"withdraw":{"enabled":true,"destination":"me@x.y","password":"` + adminKey + `"},"lightning":{"backend":"fake"}`,
		"password = ai":    admin + `,"withdraw":{"enabled":true,"destination":"me@x.y","password":"` + aiKey + `"},"lightning":{"backend":"fake"}`,
		"onchain no fee":   admin + `,"withdraw":{"enabled":true,"destination":"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq","password":"` + pw + `"},"lightning":{"backend":"fake"}`,
		"phoenixd no full": admin + `,"withdraw":{"enabled":true,"destination":"me@x.y","password":"` + pw + `"},` + phx,
		"full = limited":   admin + `,"withdraw":{"enabled":true,"destination":"me@x.y","password":"` + pw + `","phoenixd_full_password":"limitedpassword"},` + phx,
		"set but disabled": admin + `,"withdraw":{"destination":"me@x.y"},"lightning":{"backend":"fake"}`,
	}
	for name, extra := range bad {
		if _, err := load(t, `{`+base+`,`+extra+`}`); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	c, err := load(t, `{`+base+`,`+admin+`,"withdraw":{"enabled":true,"destination":"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq","password":"`+pw+`","phoenixd_full_password":"fullpassword","feerate_sat_vbyte":3},`+phx+`}`)
	if err != nil || !c.Withdraw.Enabled {
		t.Fatalf("%v", err)
	}
	// Spending secrets come from the environment only when enabled.
	t.Setenv("SPIKES402_WITHDRAW_PASSWORD", pw)
	t.Setenv("PHOENIXD_FULL_PASSWORD", "fullpassword")
	c, err = load(t, `{`+base+`,"lightning":{"backend":"fake"}}`)
	if err != nil || c.Withdraw.Password != "" || c.Withdraw.PhoenixdFullPassword != "" {
		t.Fatalf("%v %+v", err, c.Withdraw)
	}
	c, err = load(t, `{`+base+`,`+admin+`,"withdraw":{"enabled":true,"destination":"me@x.y"},`+phx+`}`)
	if err != nil || c.Withdraw.Password != pw || c.Withdraw.PhoenixdFullPassword != "fullpassword" {
		t.Fatalf("%v %+v", err, c.Withdraw)
	}
	// BIP353 addresses are often written with a leading "₿".
	c2, err := load(t, `{`+base+`,`+admin+`,"withdraw":{"enabled":true,"destination":" ₿me@x.y "},`+phx+`}`)
	if err != nil || c2.Withdraw.Destination != "me@x.y" {
		t.Fatalf("%v %q", err, c2.Withdraw.Destination)
	}
	pub := c.Public()
	if !pub.WithdrawEnabled || pub.WithdrawDestination != "me@x.y" {
		t.Fatalf("%+v", pub)
	}
}

func TestApply(t *testing.T) {
	c, err := load(t, `{`+base+`,"lightning":{"backend":"fake"}}`)
	if err != nil {
		t.Fatal(err)
	}
	price, sps, cpm := int64(7), 2.0, 5
	pol := map[string]gate.Policy{"impostor": {Action: gate.ActionPoWOnly}}
	n, err := c.Apply(Changes{PriceSats: &price, PowSecondsPerSat: &sps, ChallengesPerMinute: &cpm, BotPolicies: &pol})
	if err != nil {
		t.Fatal(err)
	}
	if n.PriceSats != 7 || n.PowSecondsPerSat != 2 || n.ChallengesPerMinute != 5 || n.BotPolicies["impostor"].PriceMultiplier != 1 {
		t.Fatalf("%+v", n)
	}
	if c.PriceSats != 5 || len(c.BotPolicies) != 0 || pol["impostor"].PriceMultiplier != 0 {
		t.Fatal("Apply changed the original or the input")
	}
	if s := n.GateSettings(); s.PriceSats != 7 || s.PoW(7) != n.PoW(7) {
		t.Fatalf("%+v", s)
	}
	zero, huge, nan, neg := int64(0), int64(2_000_000), 0.0, -1
	badPol := map[string]gate.Policy{"x": {Action: "nope"}}
	for name, ch := range map[string]Changes{
		"empty":      {},
		"zero":       {PriceSats: &zero},
		"huge":       {PriceSats: &huge},
		"zero sps":   {PowSecondsPerSat: &nan},
		"negative":   {ChallengesPerMinute: &neg},
		"bad policy": {BotPolicies: &badPol},
	} {
		if _, err := c.Apply(ch); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPublicHasNoSecrets(t *testing.T) {
	t.Setenv("SPIKES402_ADMIN_KEY", adminKey)
	c, err := load(t, `{`+base+`,"secret_key":"`+secret+`","lightning":{"backend":"fake"}}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c.Public())
	if strings.Contains(string(b), adminKey) || strings.Contains(string(b), secret) {
		t.Fatalf("secret in %s", b)
	}
}
