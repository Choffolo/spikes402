package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/admin"
	"github.com/Choffolo/spikes402/internal/config"
	"github.com/Choffolo/spikes402/internal/gate"
	"github.com/Choffolo/spikes402/internal/lightning"
)

// session sends lines to a server and returns the decoded responses.
func session(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	dec := json.NewDecoder(&out)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		resps = append(resps, m)
	}
	return resps
}

func TestProtocol(t *testing.T) {
	s := &Server{Name: "test", Version: "1", Tools: []Tool{
		{Name: "echo", InputSchema: map[string]any{"type": "object"}, Call: func(_ context.Context, a json.RawMessage) (string, error) { return string(a), nil }},
		{Name: "fail", InputSchema: map[string]any{"type": "object"}, Call: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("nope") }},
	}}
	r := session(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"x":1}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fail"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"missing"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/list"}`,
		`not json`,
		`{"jsonrpc":"2.0","id":"p","method":"ping"}`,
	)
	if len(r) != 8 { // the notification gets no answer
		t.Fatalf("%d responses: %v", len(r), r)
	}
	if res := r[0]["result"].(map[string]any); res["protocolVersion"] != "2025-06-18" || res["serverInfo"].(map[string]any)["name"] != "test" {
		t.Fatalf("%v", r[0])
	}
	if tools := r[1]["result"].(map[string]any)["tools"].([]any); len(tools) != 2 {
		t.Fatalf("%v", r[1])
	}
	if c := r[2]["result"].(map[string]any)["content"].([]any)[0].(map[string]any); c["text"] != `{"x":1}` {
		t.Fatalf("%v", r[2])
	}
	if res := r[3]["result"].(map[string]any); res["isError"] != true {
		t.Fatalf("%v", r[3])
	}
	for i, code := range map[int]float64{4: -32602, 5: -32601, 6: -32700} {
		if r[i]["error"].(map[string]any)["code"] != code {
			t.Errorf("%d: %v", i, r[i])
		}
	}
	if r[7]["id"] != "p" {
		t.Fatalf("%v", r[7])
	}
	r = session(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if r[0]["result"].(map[string]any)["protocolVersion"] != knownVersions[0] {
		t.Fatalf("%v", r[0])
	}
}

var (
	adminKey = strings.Repeat("A", 40)
	aiKey    = strings.Repeat("B", 40)
)

func adminAPI(t *testing.T, aiCanApply bool) (*httptest.Server, *gate.Gate) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "c.json")
	apply := "false"
	if aiCanApply {
		apply = "true"
	}
	js := `{"origin":"http://o","price_sats":10,"protect":["/p/**"],"admin":{"key":"` + adminKey + `","ai_key":"` + aiKey + `","ai_can_apply":` + apply + `},
		"withdraw":{"enabled":true,"destination":"me@wallet.example","password":"correct horse battery"},"lightning":{"backend":"fake"}}`
	os.WriteFile(file, []byte(js), 0o600)
	cfg, err := config.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	ln := lightning.NewFake()
	mon := admin.NewMonitor("hash", time.Now())
	g := gate.New(gate.Options{PriceSats: cfg.PriceSats, Protect: cfg.Protect, PoW: cfg.PoW, ChallengeTTL: time.Minute,
		SecretKey: []byte(strings.Repeat("s", 32)), Lightning: ln, Next: http.NotFoundHandler(),
		ChallengesPerMinute: 30, MaxInvoicesPerMinute: 30, MaxPendingInvoices: 30, Observer: mon.Observe})
	srv := httptest.NewServer(admin.New(admin.Options{Config: cfg, Gate: g, Monitor: mon, Node: ln, Withdrawer: ln}).Handler())
	t.Cleanup(srv.Close)
	return srv, g
}

func call(t *testing.T, tools []Tool, name, args string) (string, error) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool.Call(context.Background(), json.RawMessage(args))
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

func TestAdminTools(t *testing.T) {
	srv, g := adminAPI(t, false)
	tools := AdminTools(&AdminClient{BaseURL: srv.URL, Key: aiKey})
	for _, tool := range tools {
		if strings.Contains(tool.Name, "withdraw") || strings.Contains(tool.Description, "withdraw") {
			t.Fatalf("withdraw exposed to the AI: %s", tool.Name)
		}
	}
	for _, name := range []string{"get_status", "get_settings", "list_proposals"} {
		if out, err := call(t, tools, name, `{}`); err != nil || len(out) < 2 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if out, err := call(t, tools, "get_recent_visits", `{"since":0}`); err != nil || !strings.Contains(out, `"visits"`) {
		t.Fatalf("%s %v", out, err)
	}
	out, err := call(t, tools, "propose_settings_change", `{"changes":{"bot_policies":{"impostor":{"action":"pow-only"}}},"reason":"many fake GPTBots"}`)
	if err != nil || !strings.Contains(out, `"pending"`) {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := call(t, tools, "apply_settings_change", `{"changes":{"price_sats":1}}`); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("apply without opt-in: %v", err)
	}
	if g.Settings().PriceSats != 10 || len(g.Settings().Policies) != 0 {
		t.Fatal("settings changed without approval")
	}

	srv, g = adminAPI(t, true)
	tools = AdminTools(&AdminClient{BaseURL: srv.URL, Key: aiKey})
	if _, err := call(t, tools, "apply_settings_change", `{"changes":{"price_sats":1}}`); err == nil || g.Settings().PriceSats != 10 {
		t.Fatalf("AI must not lower the price: %v gate=%d", err, g.Settings().PriceSats)
	}
	if _, err := call(t, tools, "apply_settings_change", `{"changes":{"price_sats":15}}`); err != nil || g.Settings().PriceSats != 15 {
		t.Fatalf("apply with opt-in (stricter price): %v", err)
	}
}

func TestRefusesHumanKey(t *testing.T) {
	srv, _ := adminAPI(t, false)
	tools := AdminTools(&AdminClient{BaseURL: srv.URL, Key: adminKey})
	if _, err := call(t, tools, "get_status", `{}`); err == nil || !strings.Contains(err.Error(), "human admin key") {
		t.Fatalf("got %v", err)
	}
}
