package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Choffolo/spikes402/internal/gate"
)

func TestOverridesPath(t *testing.T) {
	if got := OverridesPath("config.json"); got != "config.overrides.json" {
		t.Fatalf("got %q", got)
	}
	in := filepath.FromSlash("/etc/spikes402/config.json")
	want := filepath.FromSlash("/etc/spikes402/config.overrides.json")
	if got := OverridesPath(in); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestOverridesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "c.json")
	js := `{` + base + `,"lightning":{"backend":"fake"}}`
	if err := os.WriteFile(file, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if c.Paused || c.PriceSats != 5 {
		t.Fatalf("base: %+v", c)
	}
	price, paused := int64(9), true
	protect := []string{"/only/**"}
	pol := map[string]gate.Policy{"unknown": {Action: gate.ActionBlock}}
	n, err := c.Apply(Changes{PriceSats: &price, Paused: &paused, Protect: &protect, BotPolicies: &pol})
	if err != nil {
		t.Fatal(err)
	}
	over := OverridesPath(file)
	if err := WriteOverrides(over, n); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(over)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(file)
	if strings.Contains(string(raw), `"paused"`) || strings.Contains(string(raw), `/only/**`) {
		t.Fatal("main config was rewritten")
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PriceSats != 9 || !loaded.Paused || len(loaded.Protect) != 1 || loaded.Protect[0] != "/only/**" || loaded.BotPolicies["unknown"].Action != gate.ActionBlock {
		t.Fatalf("loaded: price=%d paused=%v protect=%v policies=%v", loaded.PriceSats, loaded.Paused, loaded.Protect, loaded.BotPolicies)
	}
	// Fields that are not dashboard-editable stay from the main file.
	if loaded.Listen != c.Listen || loaded.MaxInvoicesPerMinute != c.MaxInvoicesPerMinute {
		t.Fatal("non-editable fields changed")
	}
}

func TestOverridesRejectUnknownAndInvalid(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "c.json")
	if err := os.WriteFile(file, []byte(`{`+base+`,"lightning":{"backend":"fake"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	over := OverridesPath(file)
	if err := os.WriteFile(over, []byte(`{"price_sats":3,"secret_key":"steal-me-now-please-32chars-ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("secrets in overrides: %v", err)
	}
	if err := os.WriteFile(over, []byte(`{"price_sats":0,"protect":["/p/**"],"pow_seconds_per_sat":10,"challenges_per_minute":30}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("invalid price accepted")
	}
	if err := os.WriteFile(over, []byte(`{"lightning":{"backend":"phoenixd"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("lightning in overrides: %v", err)
	}
}

func TestOverridesMissingIsFine(t *testing.T) {
	c, err := load(t, `{`+base+`,"lightning":{"backend":"fake"}}`)
	if err != nil || c.PriceSats != 5 {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestOverridesLiveInStateDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "c.json")
	if err := os.WriteFile(file, []byte(`{`+base+`,"lightning":{"backend":"fake"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if c.OverridesFile() != filepath.Join(dir, "state", "config.overrides.json") {
		t.Fatalf("overrides path: %s", c.OverridesFile())
	}
	price, paused := int64(9), true
	n, err := c.Apply(Changes{PriceSats: &price, Paused: &paused})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteOverrides(n.OverridesFile(), n); err != nil {
		t.Fatal(err)
	}
	// A leftover legacy file must not win over the state_dir copy.
	legacy := OverridesPath(file)
	if err := os.WriteFile(legacy, []byte(`{"price_sats":1,"protect":["/p/**"],"pow_seconds_per_sat":10,"challenges_per_minute":30,"paused":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PriceSats != 9 || !loaded.Paused {
		t.Fatalf("state_dir should win: price=%d paused=%v", loaded.PriceSats, loaded.Paused)
	}
}

func TestLegacyOverridesStillLoad(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "c.json")
	if err := os.WriteFile(file, []byte(`{`+base+`,"lightning":{"backend":"fake"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := OverridesPath(file)
	if err := os.WriteFile(legacy, []byte(`{"price_sats":8,"protect":["/p/**"],"pow_seconds_per_sat":10,"challenges_per_minute":30,"paused":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PriceSats != 8 || !loaded.Paused {
		t.Fatalf("legacy overrides not applied: price=%d paused=%v", loaded.PriceSats, loaded.Paused)
	}
}
