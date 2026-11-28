package gate

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choffolo/spikes402/pow"
)

// jsHarness runs the embedded browser workers in Node with a stub "self":
// it prints the Argon2id hashes of the inputs, and the nonces the two
// workers' own search loops find.
const jsHarness = `
const fs = require("fs");
const [argonSrc, powSrc, inputs] = [fs.readFileSync(process.argv[2], "utf8"), fs.readFileSync(process.argv[3], "utf8"), JSON.parse(fs.readFileSync(process.argv[4], "utf8"))];
function load(src) { const self = {}; let found; self.postMessage = (m) => { if (m.nonce !== undefined) found = m.nonce; }; new Function("self", src)(self); return { self, nonce: () => found }; }
const a = load(argonSrc), p = load(powSrc);
const hex = (b) => Buffer.from(b).toString("hex");
const hashes = inputs.hashes.map((h) => hex(a.self.spikes402Argon2id(new TextEncoder().encode(h.challenge + ":" + h.nonce), h.memoryMiB * 1024)));
a.self.onmessage({ data: { challenge: inputs.solve, difficulty: 3, memoryMiB: 8, start: 0, step: 1 } });
p.self.onmessage({ data: { challenge: inputs.solve, difficulty: 12, start: 0 } });
console.log(JSON.stringify({ hashes, argonNonce: a.nonce(), preNonce: p.nonce() }));
`

// TestBrowserWorkersMatchGo checks that the JavaScript Argon2id solver
// computes byte-identical hashes to the Go verifier (pow.Params.Hash), and
// that nonces found by both browser workers pass the server's checks.
func TestBrowserWorkersMatchGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required in CI to test the browser solver")
		}
		t.Skip("node not installed")
	}
	f := newLoginFixture(t)
	token := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", ""))).Challenge
	type in struct {
		Challenge string `json:"challenge"`
		Nonce     uint64 `json:"nonce"`
		MemoryMiB int    `json:"memoryMiB"`
	}
	var hashes []in
	for _, c := range []string{"", "a", strings.Repeat("x", 118), strings.Repeat("y", 119), strings.Repeat("z", 120), strings.Repeat("é", 100), token} {
		for _, n := range []uint64{0, 7, 1 << 40} {
			hashes = append(hashes, in{c, n, 8})
		}
	}
	hashes = append(hashes, in{token, 12345, LoginArgonMemoryMiB})
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	inputs, _ := json.Marshal(map[string]any{"hashes": hashes, "solve": token})
	out, err := exec.Command(node, write("h.js", jsHarness), write("a.js", argon2idWorkerJS), write("p.js", powWorkerJS), write("in.json", string(inputs))).Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var got struct {
		Hashes     []string
		ArgonNonce *uint64
		PreNonce   *uint64
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Hashes) != len(hashes) {
		t.Fatalf("node output %q: %v", out, err)
	}
	for i, h := range hashes {
		want := pow.Params{Algorithm: pow.Argon2id, MemoryMiB: h.MemoryMiB}.Hash(h.Challenge, h.Nonce)
		if got.Hashes[i] != hex.EncodeToString(want[:]) {
			t.Errorf("%d bytes, nonce %d, %d MiB: JS %s, Go %x", len(h.Challenge), h.Nonce, h.MemoryMiB, got.Hashes[i], want)
		}
	}
	if got.ArgonNonce == nil || !(pow.Params{Algorithm: pow.Argon2id, Difficulty: 3, MemoryMiB: 8}).Check(token, *got.ArgonNonce) {
		t.Errorf("JS Argon2id worker nonce rejected by Go: %v", got.ArgonNonce)
	}
	if got.PreNonce == nil || !(pow.Params{Algorithm: pow.SHA256d, Difficulty: 12}).Check(token, *got.PreNonce) {
		t.Errorf("JS sha256d worker nonce rejected by Go: %v", got.PreNonce)
	}
}
