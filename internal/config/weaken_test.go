package config

import (
	"strings"
	"testing"

	"github.com/Choffolo/spikes402/internal/gate"
)

func testCfg(t *testing.T) *Config {
	t.Helper()
	c, err := load(t, `{`+base+`,"lightning":{"backend":"fake"}}`)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPowSecondsFloor(t *testing.T) {
	if _, err := load(t, `{`+base+`,"pow_seconds_per_sat":0.000001,"lightning":{"backend":"fake"}}`); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("tiny pow_seconds_per_sat accepted: %v", err)
	}
	c, err := load(t, `{`+base+`,"pow_seconds_per_sat":0.000001,"unsafe_allow_dangerous_config":true,"lightning":{"backend":"fake"}}`)
	if err != nil || c.PowSecondsPerSat != 0.000001 {
		t.Fatalf("unsafe should allow a lower value: %v %+v", err, c)
	}
	if _, err := load(t, `{`+base+`,"pow_seconds_per_sat":1,"lightning":{"backend":"fake"}}`); err != nil {
		t.Fatalf("1 second per sat should be accepted: %v", err)
	}
}

func TestWeakensProtection(t *testing.T) {
	cur := testCfg(t) // price 5, protect from base, sps default 10
	price := int64(3)
	n, err := cur.Apply(Changes{PriceSats: &price})
	if err != nil {
		t.Fatal(err)
	}
	if why := WeakensProtection(cur, n); len(why) == 0 {
		t.Fatal("lowering the price should weaken")
	}
	up := int64(20)
	n, err = cur.Apply(Changes{PriceSats: &up})
	if err != nil {
		t.Fatal(err)
	}
	if why := WeakensProtection(cur, n); len(why) != 0 {
		t.Fatalf("raising the price should not weaken: %v", why)
	}
	protect := []string{"/elsewhere/**"}
	n, err = cur.Apply(Changes{Protect: &protect})
	if err != nil {
		t.Fatal(err)
	}
	if why := WeakensProtection(cur, n); len(why) == 0 {
		t.Fatal("replacing protect patterns should weaken")
	}
	free := map[string]gate.Policy{"unknown": {Action: gate.ActionFree}}
	n, err = cur.Apply(Changes{BotPolicies: &free})
	if err != nil {
		t.Fatal(err)
	}
	if why := WeakensProtection(cur, n); len(why) == 0 {
		t.Fatal("free policy should weaken")
	}
	paused := true
	n, err = cur.Apply(Changes{Paused: &paused})
	if err != nil {
		t.Fatal(err)
	}
	if why := WeakensProtection(cur, n); len(why) == 0 {
		t.Fatal("pausing should weaken")
	}
	sps := 1.0
	n, err = cur.Apply(Changes{PowSecondsPerSat: &sps})
	if err != nil {
		t.Fatal(err)
	}
	if why := WeakensProtection(cur, n); len(why) == 0 {
		t.Fatal("lowering pow_seconds_per_sat should weaken")
	}
	block := map[string]gate.Policy{"unknown": {Action: gate.ActionBlock}}
	n, err = cur.Apply(Changes{BotPolicies: &block})
	if err != nil {
		t.Fatal(err)
	}
	if why := WeakensProtection(cur, n); len(why) != 0 {
		t.Fatalf("block is stricter: %v", why)
	}
}
