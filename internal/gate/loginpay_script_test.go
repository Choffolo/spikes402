package gate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/lightning"
)

// loginPayHarness runs the served login.js in Node with a stub DOM against
// a real gate: the username loses focus (challenge, "Pay instead" shows
// the price), the button is clicked (invoice), the invoice is paid with a
// WebLN wallet ("webln") or from outside while the page polls ("poll"),
// and the page must then submit the form holding a login pass.
const loginPayHarness = `
const [src, base, mode] = [require("fs").readFileSync(process.argv[2], "utf8"), process.argv[3], process.argv[4]];
const out = {};
const realFetch = fetch;
globalThis.fetch = async (u, o) => {
  const r = await realFetch(base + u, o);
  if (u.endsWith("login-redeem") && r.status === 204) out.cookie = r.headers.get("set-cookie");
  return r;
};
const payFake = async (invoice) => (await (await realFetch(base + "/pay", { method: "POST", body: new URLSearchParams({ invoice }) })).text()).trim();
function el(tag) {
  return { tag, children: [], attrs: {}, style: {}, hidden: false, textContent: "", listeners: {},
    setAttribute(k, v) { this.attrs[k] = v; }, getAttribute(k) { return this.attrs[k] ?? null; },
    appendChild(c) { this.children.push(c); return c; }, append(...c) { this.children.push(...c); },
    addEventListener(t, f) { (this.listeners[t] = this.listeners[t] || []).push(f); },
    click() { (this.listeners.click || []).forEach((f) => f({})); } };
}
const find = (e, tag) => e.children.find((c) => c.tag === tag) || e.children.map((c) => find(c, tag)).find(Boolean);
const input = el("input"); input.value = "alice";
const form = el("form"); form.elements = { user: input };
form.attrs = { action: "/login", method: "post" };
form.requestSubmit = () => { out.submitted = true; out.status = form.children[0].textContent; console.log(JSON.stringify(out)); process.exit(0); };
globalThis.window = globalThis;
globalThis.location = { href: base + "/login" };
globalThis.document = { currentScript: { getAttribute: (k) => (k === "data-field" ? "user" : null) }, querySelectorAll: () => [form], createElement: el };
globalThis.Worker = class { postMessage() {} terminate() {} };
globalThis.HTMLFormElement = function () {};
if (mode === "webln") window.webln = { enable: async () => {}, sendPayment: async (inv) => ({ preimage: await payFake(inv) }) };
const until = (f) => new Promise((ok, no) => { const t0 = Date.now(); (function w() { const v = f(); if (v) ok(v); else if (Date.now() - t0 > 15000) no(new Error("timeout")); else setTimeout(w, 20); })(); });
setTimeout(() => { console.log(JSON.stringify({ error: "timeout", ...out, status: form.children[0].textContent })); process.exit(1); }, 20000);
(async () => {
  new Function(src)();
  input.listeners.blur[0]();
  const btn = await until(() => !form.children[1].hidden && form.children[1]);
  out.payText = btn.textContent;
  btn.click();
  const box = form.children[2];
  const code = await until(() => find(box, "code"));
  out.qr = find(box, "img").src.slice(0, 22);
  if (mode === "webln") (await until(() => find(box, "button"))).click();
  else await payFake(code.textContent);
})().catch((e) => { console.log(JSON.stringify({ error: e.message, ...out })); process.exit(1); });
`

func TestLoginPayScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI to test login.js")
		}
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	h, js := write("h.js", loginPayHarness), write("login.js", loginJS)
	for _, mode := range []string{"webln", "poll"} {
		t.Run(mode, func(t *testing.T) {
			f := newPayFixture(t)
			f.gate.now = time.Now // the page polls in real time
			mux := http.NewServeMux()
			mux.Handle("/pay", f.gate.opt.Lightning.(*lightning.Fake).PayHandler())
			mux.Handle("/", f.gate)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			raw, err := exec.Command(node, h, js, srv.URL, mode).Output()
			var got struct {
				Error, PayText, QR, Cookie, Status string
				Submitted                          bool
			}
			if err2 := json.Unmarshal(raw, &got); err != nil || err2 != nil || got.Error != "" {
				t.Fatalf("node: %v %s", err, raw)
			}
			if got.PayText != "Pay 10 sats instead" || got.QR != "data:image/png;base64," || !got.Submitted {
				t.Fatalf("page: %+v", got)
			}
			pass, err := http.ParseSetCookie(got.Cookie)
			if err != nil || pass.Name != loginPassCookie {
				t.Fatalf("no login pass: %q", got.Cookie)
			}
			r := loginReq("alice", "127.0.0.1", "", pass)
			if rec := f.do(r); rec.Code != http.StatusFound || f.calls != 1 {
				t.Fatalf("login with the paid pass: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
			}
		})
	}
}
