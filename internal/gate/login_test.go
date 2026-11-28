package gate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/lightning"
	"github.com/Choffolo/spikes402/internal/persist"
	"github.com/Choffolo/spikes402/pow"
)

type loginFixture struct {
	gate    *Gate
	now     time.Time
	calls   int
	succeed func(user string) bool // what the origin decides
	respond http.HandlerFunc       // if set, answers instead of the default origin
	lastCL  int64                  // ContentLength and TransferEncoding the origin saw
	lastTE  []string
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	return newLoginFixtureWith(t, [2]int{})
}

func newLoginFixtureWith(t *testing.T, surge [2]int) *loginFixture {
	t.Helper()
	f := &loginFixture{now: time.Unix(1_800_000_000, 0), succeed: func(string) bool { return false }}
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "PoW") || strings.Contains(r.Header.Get("Cookie"), loginPassCookie) {
			t.Error("login proof leaked to the origin")
		}
		f.calls++
		f.lastCL, f.lastTE = r.ContentLength, r.TransferEncoding
		if f.respond != nil {
			f.respond(w, r)
			return
		}
		r.ParseForm()
		if f.succeed(r.PostForm.Get("user")) {
			http.Redirect(w, r, "/home", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/login?failed=1", http.StatusFound)
	})
	f.gate = New(Options{
		PriceSats: 10, Protect: []string{"/premium/**"},
		PoW:          func(int64) pow.Params { return pow.Params{Algorithm: pow.SHA256d, Difficulty: 8} },
		ChallengeTTL: time.Minute, PassTTL: DefaultPassTTL,
		SecretKey: []byte(strings.Repeat("s", 32)), Lightning: lightning.NewFake(), Next: origin,
		ChallengesPerMinute: 1000, MaxInvoicesPerMinute: 6000, MaxPendingInvoices: 1000,
		LoginRoutes: []LoginRoute{{Path: "/login", UsernameField: "user",
			Success: LoginSuccess{Status: []int{302}, LocationPrefix: "/home"}}},
		LoginSurge: surge,
	})
	if err := CheckLoginRoutes(f.gate.opt.LoginRoutes); err != nil {
		t.Fatal(err)
	}
	f.gate.now = func() time.Time { return f.now }
	// Tiny puzzles (8 MiB, level-23 bits) so tests stay fast; same code path.
	f.gate.loginParams = func(level int) pow.Params {
		return pow.Params{Algorithm: pow.Argon2id, Difficulty: min(level-LoginBaseLevel, 2), MemoryMiB: 8}
	}
	f.gate.loginPre = 4
	return f
}

func loginReq(user, ip, auth string, cookies ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"user": {user}, "pass": {"x"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = ip + ":1234"
	r.Header.Set("Referer", "https://site.example/login") // a browser's, from the login page
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

func (f *loginFixture) do(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, r)
	return rec
}

func decodeChallenge(t *testing.T, rec *httptest.ResponseRecorder) loginChallenge {
	t.Helper()
	var ch loginChallenge
	if err := json.NewDecoder(rec.Body).Decode(&ch); err != nil || ch.Challenge == "" {
		t.Fatalf("no challenge in %d answer: %v", rec.Code, err)
	}
	return ch
}

func solveLogin(t *testing.T, ch loginChallenge) string {
	t.Helper()
	p := pow.Params{Algorithm: ch.Algorithm, Difficulty: ch.Difficulty, MemoryMiB: ch.MemoryMiB}
	n, err := p.Solve(context.Background(), ch.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := pow.Params{Algorithm: ch.PreAlgorithm, Difficulty: ch.PreDifficulty}.Solve(context.Background(), ch.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	return "PoW " + ch.Challenge + ":" + strconv.FormatUint(n, 10) + ":" + strconv.FormatUint(pre, 10)
}

// attempt runs one full API login (402, solve, retry) and returns the level paid.
func (f *loginFixture) attempt(t *testing.T, user, ip string) int {
	t.Helper()
	rec := f.do(loginReq(user, ip, ""))
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("no proof: got %d, want 402", rec.Code)
	}
	ch := decodeChallenge(t, rec)
	if rec := f.do(loginReq(user, ip, solveLogin(t, ch))); rec.Code != http.StatusFound {
		t.Fatalf("with proof: got %d, want the origin's 302: %s", rec.Code, rec.Body)
	}
	return ch.Level
}

func TestLoginNeedsProofAndFailsClosed(t *testing.T) {
	f := newLoginFixture(t)
	rec := f.do(loginReq("alice", "192.0.2.1", ""))
	if rec.Code != http.StatusPaymentRequired || f.calls != 0 {
		t.Fatalf("got %d with %d origin calls, want 402 and none", rec.Code, f.calls)
	}
	if ch := decodeChallenge(t, rec); ch.Level != LoginBaseLevel || ch.Algorithm != pow.Argon2id {
		t.Fatalf("challenge %+v", ch)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), `algorithm="argon2id"`) {
		t.Fatal("no PoW WWW-Authenticate header")
	}
	// Requests that cannot be checked never reach the origin.
	big := loginReq(strings.Repeat("a", loginMaxBody), "192.0.2.1", "")
	noUser := loginReq("", "192.0.2.1", "")
	odd := loginReq("alice", "192.0.2.1", "")
	odd.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	garbage := loginReq("alice", "192.0.2.1", "PoW garbage:1")
	for name, r := range map[string]*http.Request{"too large": big, "no username": noUser, "content type": odd, "bad proof": garbage} {
		if rec := f.do(r); rec.Code < 400 {
			t.Errorf("%s: got %d", name, rec.Code)
		}
	}
	if f.calls != 0 {
		t.Fatalf("origin called %d times", f.calls)
	}
	// Paused and the trailing-slash / case spelling change nothing.
	set := f.gate.Settings()
	set.Paused = true
	if err := f.gate.UpdateSettings(set); err != nil {
		t.Fatal(err)
	}
	r := loginReq("alice", "192.0.2.1", "")
	r.URL.Path = "/Login/"
	if rec := f.do(r); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("paused /Login/: got %d", rec.Code)
	}
	// Other methods on the path are not login attempts.
	if rec := f.do(httptest.NewRequest(http.MethodGet, "/login", nil)); rec.Code != http.StatusFound || f.calls != 1 {
		t.Fatalf("GET /login: got %d", rec.Code)
	}
}

func TestLoginEscalatesAndResets(t *testing.T) {
	f := newLoginFixture(t)
	want := []int{23, 24, 25, 26, 27, 29, 31, 33, 33}
	for i, w := range want {
		if got := f.attempt(t, "alice", "192.0.2.1"); got != w {
			t.Fatalf("attempt %d: level %d, want %d", i, got, w)
		}
	}
	// Half an hour halves the pair counter: 9 failures weigh 4.5, rounded to 5 (+6).
	f.now = f.now.Add(30 * time.Minute)
	if got := f.attempt(t, "alice", "192.0.2.1"); got != 29 {
		t.Fatalf("after decay: level %d, want 29", got)
	}
	// A success resets the pair, but neither the account counter (so the
	// one-bit account term does not show when the real user logs in) nor
	// the network's own counter: both only decay.
	f.succeed = func(string) bool { return true }
	f.attempt(t, "alice", "192.0.2.1")
	f.succeed = func(string) bool { return false }
	net := f.gate.loginNetwork(loginReq("", "192.0.2.1", ""))
	if got := f.gate.login.Level(f.gate.loginAccount("alice"), net, f.now); got != LoginBaseLevel+loginAcctCap {
		t.Fatalf("after success: level %d, want the account term %d", got, LoginBaseLevel+loginAcctCap)
	}
	if got := f.gate.login.Level(f.gate.loginAccount("someone-else"), net, f.now); got != 26 {
		// The network still weighs 3 failures (9 halved twice, plus one): +3.
		t.Fatalf("after success, other account: level %d, want 26", got)
	}
}

func TestLoginCapsProtectVictims(t *testing.T) {
	f := newLoginFixture(t)
	// Many networks fail on alice: from a fresh network she pays at most +4.
	for i := range 10 {
		f.attempt(t, "alice", "198.51."+strconv.Itoa(i)+".7")
	}
	if got := f.attempt(t, "alice", "203.0.113.9"); got != LoginBaseLevel+loginAcctCap {
		t.Fatalf("victim level %d, want %d", got, LoginBaseLevel+loginAcctCap)
	}
	// One network fails on many accounts: a neighbour pays at most +6.
	for i := range 10 {
		f.attempt(t, "user"+strconv.Itoa(i), "192.0.2.50")
	}
	if got := f.attempt(t, "bob", "192.0.2.77"); got != LoginBaseLevel+loginNetCap {
		t.Fatalf("neighbour level %d, want %d", got, LoginBaseLevel+loginNetCap)
	}
}

func TestLoginProofIsOneUseAndBound(t *testing.T) {
	f := newLoginFixture(t)
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	proof := solveLogin(t, ch)
	for name, r := range map[string]*http.Request{
		"other account": loginReq("bob", "192.0.2.1", proof),
		"other network": loginReq("alice", "203.0.113.1", proof),
	} {
		if rec := f.do(r); rec.Code != http.StatusPaymentRequired {
			t.Errorf("%s: got %d", name, rec.Code)
		}
	}
	// Same /24: still the same network.
	if rec := f.do(loginReq("alice", "192.0.2.200", proof)); rec.Code != http.StatusFound {
		t.Fatalf("first use: got %d", rec.Code)
	}
	if rec := f.do(loginReq("alice", "192.0.2.1", proof)); rec.Code != http.StatusPaymentRequired || f.calls != 1 {
		t.Fatalf("replay: got %d, %d origin calls", rec.Code, f.calls)
	}
	// A stockpiled challenge from before the failure is below the level now.
	ch1 := decodeChallenge(t, f.do(loginReq("carol", "192.0.2.1", "")))
	ch2 := decodeChallenge(t, f.do(loginReq("carol", "192.0.2.1", "")))
	f.do(loginReq("carol", "192.0.2.1", solveLogin(t, ch1)))
	rec := f.do(loginReq("carol", "192.0.2.1", solveLogin(t, ch2)))
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), "below the current level") {
		t.Fatalf("stale level: got %d %s", rec.Code, rec.Body)
	}
	// Expired.
	ch3 := decodeChallenge(t, f.do(loginReq("dave", "192.0.2.1", "")))
	f.now = f.now.Add(loginChallengeTTL)
	if rec := f.do(loginReq("dave", "192.0.2.1", solveLogin(t, ch3))); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expired: got %d", rec.Code)
	}
}

func TestLoginBrowserFlow(t *testing.T) {
	f := newLoginFixture(t)
	r := httptest.NewRequest(http.MethodPost, LoginChallengePath, strings.NewReader(`{"method":"POST","path":"/login","username":"Alice "}`))
	r.RemoteAddr = "192.0.2.1:1"
	rec := f.do(r)
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge: %d %s", rec.Code, rec.Body)
	}
	proof := solveLogin(t, decodeChallenge(t, rec))
	r = httptest.NewRequest(http.MethodPost, LoginRedeemPath, nil)
	r.RemoteAddr = "192.0.2.1:1"
	r.Header.Set("Authorization", proof)
	rec = f.do(r)
	if rec.Code != http.StatusNoContent || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("redeem: %d %s", rec.Code, rec.Body)
	}
	pass := rec.Result().Cookies()[0]
	if pass.Name != loginPassCookie || !pass.HttpOnly || pass.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", pass)
	}
	if rec := f.do(r); rec.Code != http.StatusForbidden {
		t.Fatalf("second redeem: got %d", rec.Code)
	}
	// Account names are compared case- and space-insensitively.
	if rec := f.do(loginReq("alice", "192.0.2.1", "", pass)); rec.Code != http.StatusFound || f.calls != 1 {
		t.Fatalf("login with pass: %d", rec.Code)
	}
	if rec := f.do(loginReq("alice", "192.0.2.1", "", pass)); rec.Code != http.StatusPaymentRequired || f.calls != 1 {
		t.Fatalf("pass reused: %d", rec.Code)
	}
	for _, p := range []string{LoginScriptPath, Argon2idWorkerPath} {
		rec := f.do(httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	if strings.Contains(loginJS, "__WORKER__") {
		t.Fatal("worker URL not filled in")
	}
}

func TestLoginBusyArgonIs503(t *testing.T) {
	f := newLoginFixture(t)
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	// A full API budget does not touch logins: they have their own.
	f.gate.argon.acquire(f.gate.argon.max, 0)
	if rec := f.do(loginReq("alice", "192.0.2.1", solveLogin(t, ch))); rec.Code != http.StatusFound {
		t.Fatalf("full API budget: got %d, want the origin's 302", rec.Code)
	}
	ch = decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	f.gate.loginArgon.acquire(f.gate.loginArgon.max, 0)
	if rec := f.do(loginReq("alice", "192.0.2.1", solveLogin(t, ch))); rec.Code != http.StatusServiceUnavailable || f.calls != 1 {
		t.Fatalf("got %d with %d origin calls, want 503 and no new call", rec.Code, f.calls)
	}
}

func TestLoginStateLimitsAndSurge(t *testing.T) {
	s := NewLoginState()
	now := time.Unix(1_800_000_000, 0)
	for range DefaultLoginSurge[0] {
		s.failed(now)
	}
	if got := s.Level("a", "n", now); got != LoginBaseLevel+1 {
		t.Fatalf("surge: level %d", got)
	}
	for range DefaultLoginSurge[1] - DefaultLoginSurge[0] {
		s.failed(now)
	}
	if got := s.Level("a", "n", now.Add(time.Minute)); got != LoginBaseLevel+2 {
		t.Fatalf("surge x10, next minute: level %d", got)
	}
	if got := s.Level("a", "n", now.Add(3*time.Minute)); got != LoginBaseLevel {
		t.Fatalf("surge over: level %d", got)
	}
	// Configured thresholds.
	f := newLoginFixtureWith(t, [2]int{2, 3})
	f.attempt(t, "a", "192.0.2.1")
	f.attempt(t, "b", "198.51.100.1")
	if got := f.gate.login.Level("c", "n", f.now); got != LoginBaseLevel+1 {
		t.Fatalf("configured surge: level %d", got)
	}
	// A full table forgets its lightest counters: the heavy ones stay,
	// newcomers are tracked from the base level.
	s = NewLoginState()
	s.max = 30
	for range 5 {
		s.admit("attacked", "botnet", LoginMaxLevel, now)
	}
	for i := 0; len(s.m) < s.max; i++ {
		s.admit("u"+strconv.Itoa(i), "n"+strconv.Itoa(i), LoginMaxLevel, now.Add(-time.Hour))
	}
	if got := s.Level("new", "fresh", now); got != LoginBaseLevel {
		t.Fatalf("full table: newcomer level %d, want base", got)
	}
	s.admit("new", "fresh", LoginMaxLevel, now)
	if len(s.m) > s.max {
		t.Fatalf("table grew to %d", len(s.m))
	}
	if got := s.Level("new", "fresh", now); got != LoginBaseLevel+1 {
		t.Fatalf("newcomer not tracked: level %d", got)
	}
	if got := s.Level("attacked", "botnet", now); got != LoginBaseLevel+6 {
		t.Fatalf("heavy counter evicted: level %d", got)
	}
}

// The level of an account seen from another network shows one bit.
func TestLoginAccountLevelShowsOneBit(t *testing.T) {
	s := NewLoginState()
	now := time.Unix(1_800_000_000, 0)
	for i := range 10 {
		s.admit("victim", "n"+strconv.Itoa(i/4), LoginMaxLevel, now) // at most 4 per network
		want := LoginBaseLevel
		if i+1 >= loginAcctShow {
			want += loginAcctCap
		}
		if got := s.Level("victim", "elsewhere", now); got != want {
			t.Fatalf("after %d failures: level %d, want %d", i+1, got, want)
		}
	}
}

func TestLoginPreCheckAndPrefixLimit(t *testing.T) {
	f := newLoginFixture(t)
	f.gate.loginPre = 20
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	if ch.PreAlgorithm != pow.SHA256d || ch.PreDifficulty != 20 {
		t.Fatalf("challenge %+v", ch)
	}
	// A wrong pre-check costs no Argon2id check and does not spend the token.
	f.gate.loginArgon.acquire(f.gate.loginArgon.max, 0)
	for _, cred := range []string{ch.Challenge + ":1:1", ch.Challenge + ":1", ch.Challenge + ":1:x"} {
		rec := f.do(loginReq("alice", "192.0.2.1", "PoW "+cred))
		if rec.Code != http.StatusPaymentRequired {
			t.Fatalf("%q: got %d, want 402 (not 503: no Argon2id check)", cred, rec.Code)
		}
	}
	f.gate.loginArgon.release(f.gate.loginArgon.max)
	if rec := f.do(loginReq("alice", "192.0.2.1", solveLogin(t, ch))); rec.Code != http.StatusFound {
		t.Fatalf("right proof after wrong pre-checks: got %d", rec.Code)
	}
	// Many addresses of one IPv6 /48 share one prefix allowance.
	g := newLoginFixture(t)
	g.gate.loginLim = newRateLimiter(1000)
	g.gate.loginPrefixLim = newRateLimiter(5)
	codes := map[int]int{}
	for i := range 8 {
		r := loginReq("alice", "", "")
		r.RemoteAddr = "[2001:db8:1:" + strconv.Itoa(i) + "::1]:1234"
		codes[g.do(r).Code]++
	}
	if codes[http.StatusPaymentRequired] != 5 || codes[http.StatusTooManyRequests] != 3 {
		t.Fatalf("one /48: %v", codes)
	}
	r := loginReq("alice", "", "")
	r.RemoteAddr = "[2001:db8:2::1]:1234"
	if rec := g.do(r); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("another /48: got %d", rec.Code)
	}
}

func TestLoginStateSnapshot(t *testing.T) {
	dir := t.TempDir()
	key := []byte(strings.Repeat("k", 32))
	now := time.Unix(1_800_000_000, 0)
	s := NewLoginState()
	for range 3 {
		s.admit("a", "n", LoginMaxLevel, now)
	}
	if err := s.Save(dir, key); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, loginStateFile))
	if strings.Contains(string(data), `"a"`) {
		t.Fatal("snapshot holds a raw key")
	}
	l, err := LoadLoginState(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := l.Level("a", "n", now), s.Level("a", "n", now); got != want || got != LoginBaseLevel+3 {
		t.Fatalf("loaded level %d, want %d", got, want)
	}
	if _, err := LoadLoginState(dir, []byte(strings.Repeat("x", 32))); !errors.Is(err, persist.ErrCorrupt) {
		t.Fatalf("wrong key: %v", err)
	}
	if l, err := LoadLoginState(t.TempDir(), key); err != nil || len(l.m) != 0 {
		t.Fatalf("missing file: %v", err)
	}
}

func TestCheckLoginSurge(t *testing.T) {
	for _, bad := range [][2]int{{0, 10}, {10, 10}, {100, 50}, {1, 200_000_000}} {
		if CheckLoginSurge(bad) == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if err := CheckLoginSurge(DefaultLoginSurge); err != nil {
		t.Fatal(err)
	}
}

func TestCheckLoginRoutes(t *testing.T) {
	ok := LoginSuccess{Status: []int{302}, SetCookie: "sid"}
	bad := []LoginRoute{
		{Path: "login", UsernameField: "u", Success: ok},
		{Path: "/a/../login", UsernameField: "u", Success: ok},
		{Path: "/login*", UsernameField: "u", Success: ok},
		{Path: "/.spikes402/x", UsernameField: "u", Success: ok},
		{Path: "/login", Success: ok},
		{Path: "/login", UsernameField: "u"},
		{Path: "/login", UsernameField: "u", Method: "GET", Success: ok},
		{Path: "/login", UsernameField: "u", Success: LoginSuccess{Status: []int{42}}},
	}
	for i, r := range bad {
		if CheckLoginRoutes([]LoginRoute{r}) == nil {
			t.Errorf("route %d accepted: %+v", i, r)
		}
	}
	good := []LoginRoute{{Path: "/login", UsernameField: "u", Success: ok}}
	if err := CheckLoginRoutes(good); err != nil || good[0].Method != http.MethodPost {
		t.Fatalf("%v %+v", err, good[0])
	}
}

// The browser worker must compute exactly the hash the server checks.
func TestArgon2idWorkerMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	worker := filepath.Join(dir, "worker.js")
	os.WriteFile(worker, []byte(argon2idWorkerJS), 0o600)
	harness := `globalThis.self = globalThis; require(process.argv[2]);
const out = [];
for (const [p, mib] of JSON.parse(process.argv[3])) out.push(Buffer.from(self.spikes402Argon2id(new TextEncoder().encode(p), mib * 1024)).toString("hex"));
self.postMessage = (m) => { if (m.nonce !== undefined) { out.push(m.nonce); console.log(JSON.stringify(out)); } };
self.onmessage({ data: { challenge: "tok", difficulty: 4, memoryMiB: 8, start: 0, step: 1 } });`
	h := filepath.Join(dir, "h.js")
	os.WriteFile(h, []byte(harness), 0o600)
	cases := [][2]any{{"abc:0", 8}, {"spikes402 login:123", 16}}
	arg, _ := json.Marshal(cases)
	got, err := exec.Command(node, h, worker, string(arg)).Output()
	if err != nil {
		t.Fatal(err)
	}
	var res []any
	if err := json.Unmarshal(got, &res); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	for i, c := range cases {
		p := pow.Params{Algorithm: pow.Argon2id, MemoryMiB: c[1].(int)}
		inp, nonce, _ := strings.Cut(c[0].(string), ":")
		n, _ := strconv.ParseUint(nonce, 10, 64)
		want := p.Hash(inp, n)
		if res[i] != hex.EncodeToString(want[:]) {
			t.Errorf("%s: worker %v, Go %x", c[0], res[i], want)
		}
	}
	nonce := uint64(res[len(cases)].(float64))
	if !(pow.Params{Algorithm: pow.Argon2id, Difficulty: 4, MemoryMiB: 8}).Check("tok", nonce) {
		t.Fatalf("worker nonce %d does not solve the puzzle", nonce)
	}
}

// Spellings of the login route that some origins accept must be checked
// too, and other methods on a login path never reach the origin unchecked.
func TestLoginRouteSpellingsAndMethods(t *testing.T) {
	f := newLoginFixture(t)
	for _, c := range []struct{ method, path, raw string }{
		{"post", "/login", ""}, {"Post", "/login", ""}, {"POST", "/login;jsessionid=1", ""},
		{"POST", "/login.", ""}, {"POST", "/login ", "/login%20"}, {"POST", "/LOGIN", ""}, {"POST", "/login/", ""},
		{"PUT", "/login", ""}, {"DELETE", "/login", ""}, {"PROPFIND", "/login", ""},
	} {
		r := loginReq("alice", "198.51.100.7", "")
		r.Method, r.URL.Path, r.URL.RawPath = c.method, c.path, c.raw
		rec := f.do(r)
		if f.calls != 0 || (rec.Code != http.StatusPaymentRequired && rec.Code != http.StatusMethodNotAllowed) {
			t.Fatalf("%s %q: got %d, origin called %d times", c.method, c.path, rec.Code, f.calls)
		}
	}
	for _, h := range methodOverrideHeaders {
		r := httptest.NewRequest(http.MethodGet, "/login?user=alice", nil)
		r.Header.Set(h, "POST")
		if rec := f.do(r); rec.Code != http.StatusMethodNotAllowed || f.calls != 0 {
			t.Fatalf("GET with %s: got %d", h, rec.Code)
		}
	}
	if rec := f.do(httptest.NewRequest(http.MethodGet, "/login", nil)); f.calls != 1 {
		t.Fatalf("GET of the login page should pass: %d", rec.Code)
	}
}

// The origin must not be able to read another username than the one
// the counters see.
func TestLoginUsernameAmbiguityRefused(t *testing.T) {
	f := newLoginFixture(t)
	form := "application/x-www-form-urlencoded"
	for _, c := range []struct{ ct, query, body, enc string }{
		{form, "", "user=decoy&user=alice&pass=x", ""},
		{form, "", "user=decoy&User=alice&pass=x", ""},
		{form, "", "user=decoy&user[]=alice&pass=x", ""},
		{form, "user=alice", "user=decoy&pass=x", ""},
		{form, "", "user=decoy&pass=x", "gzip"},
		{form + "; charset=utf-16", "", "user=decoy&pass=x", ""},
		{"application/json", "", `{"user":"decoy","user":"alice"}`, ""},
		{"application/json", "", `{"user":"decoy","USER":"alice"}`, ""},
		{"application/json", "", `{"user":"decoy"} {"user":"alice"}`, ""},
		{"application/json", "", `{"USER":"alice"}`, ""},
	} {
		target := "/login"
		if c.query != "" {
			target += "?" + c.query
		}
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(c.body))
		r.Header.Set("Content-Type", c.ct)
		if c.enc != "" {
			r.Header.Set("Content-Encoding", c.enc)
		}
		if rec := f.do(r); rec.Code != http.StatusBadRequest || f.calls != 0 {
			t.Fatalf("%+v: got %d, want 400", c, rec.Code)
		}
	}
	r := loginReq("alice", "198.51.100.7", "")
	r.Header.Add("Content-Type", "application/json")
	if rec := f.do(r); rec.Code != http.StatusBadRequest {
		t.Fatalf("two Content-Type headers: got %d", rec.Code)
	}
	for _, ct := range []string{form, form + "; charset=UTF-8"} {
		r := loginReq("alice", "198.51.100.7", "")
		r.Header.Set("Content-Type", ct)
		if rec := f.do(r); rec.Code != http.StatusPaymentRequired {
			t.Fatalf("%s: got %d, want 402", ct, rec.Code)
		}
	}
	r = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"user":"alice","pass":"x","extra":{"user":"nested"}}`))
	r.Header.Set("Content-Type", "application/json")
	if rec := f.do(r); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("plain JSON login: got %d, want 402", rec.Code)
	}
}

func TestLoginRelativeFailureRedirect(t *testing.T) {
	route := LoginRoute{Path: "/login", Success: LoginSuccess{Status: []int{302}, LocationNotPrefix: "/login"}}
	for loc, want := range map[string]bool{"/home": true, "login?failed=1": false, "./login": false, "/login?e=1": false, "https://x.example/login": false, "": true} {
		if got := route.succeeded(&outcomeWriter{status: 302, location: loc}, nil, nil, time.Now()); got != want {
			t.Errorf("%q: got %v, want %v", loc, got, want)
		}
	}
}

// B1: field names a Unicode case-insensitive decoder folds onto the
// username field ("ſ" for "s", the Kelvin sign for "k") are refused.
func TestLoginUnicodeFieldFolding(t *testing.T) {
	if loginFieldKey("uſer") != loginFieldKey("user") || loginFieldKey("\u212Aey") != loginFieldKey("key") || loginFieldKey("USER") != loginFieldKey("user") {
		t.Fatal("folding")
	}
	f := newLoginFixture(t)
	for _, c := range []struct{ ct, body string }{
		{"application/json", `{"user":"attacker","uſer":"victim"}`},
		{"application/json", `{"user":"attacker","u\u017fer":"victim"}`},
		{"application/x-www-form-urlencoded", "user=attacker&u%C5%BFer=victim&pass=x"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(c.body))
		r.Header.Set("Content-Type", c.ct)
		if rec := f.do(r); rec.Code != http.StatusBadRequest || f.calls != 0 {
			t.Fatalf("%s: got %d, want 400", c.body, rec.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/login?u%C5%BFer=victim", nil)
	if rec := f.do(r); rec.Code != http.StatusMethodNotAllowed || f.calls != 0 {
		t.Fatalf("GET with a folded username in the query: got %d", rec.Code)
	}
	// R3-3: the dotless ı (U+0131) and dotted İ (U+0130) merge with i/I.
	for _, k := range []string{"emaıl", "EMAİL", "Email", "EMAIL", "emaİl"} {
		if loginFieldKey(k) != loginFieldKey("email") {
			t.Errorf("%q does not fold to email", k)
		}
	}
	f.gate.opt.LoginRoutes[0].UsernameField = "uid"
	for _, c := range []struct{ ct, body string }{
		{"application/json", `{"uid":"attacker","uıd":"victim"}`},
		{"application/json", `{"uid":"attacker","UİD":"victim"}`},
		{"application/x-www-form-urlencoded", "uid=attacker&u%C4%B1d=victim&pass=x"},
		{"application/x-www-form-urlencoded", "uid=attacker&U%C4%B0D=victim&pass=x"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(c.body))
		r.Header.Set("Content-Type", c.ct)
		if rec := f.do(r); rec.Code != http.StatusBadRequest || f.calls != 0 {
			t.Fatalf("%s: got %d, want 400", c.body, rec.Code)
		}
	}
}

// B2, B3 and case: nothing that could be a login reaches the origin
// without a proof.
func TestLoginNearPathsAndSafeMethods(t *testing.T) {
	f := newLoginFixture(t)
	f.gate.opt.CaseSensitive = true // login routes ignore it
	refused := func(name string, r *http.Request, want int) {
		t.Helper()
		if rec := f.do(r); rec.Code != want || f.calls != 0 {
			t.Fatalf("%s: got %d, want %d (origin calls %d)", name, rec.Code, want, f.calls)
		}
	}
	for _, p := range []string{"/login.json", "/login.do", "/login/x", "/Login.JSON", "/login/x/y"} {
		r := loginReq("alice", "192.0.2.1", "")
		r.URL.Path = p
		refused("POST "+p, r, http.StatusMethodNotAllowed)
	}
	r := loginReq("alice", "192.0.2.1", "")
	r.URL.Path = "/LOGIN"
	refused("POST /LOGIN with case_sensitive", r, http.StatusPaymentRequired)
	body := httptest.NewRequest(http.MethodGet, "/login", strings.NewReader("user=alice&pass=x"))
	chunked := httptest.NewRequest(http.MethodGet, "/login", strings.NewReader("user=alice"))
	chunked.ContentLength, chunked.TransferEncoding = -1, []string{"chunked"}
	headBody := httptest.NewRequest(http.MethodHead, "/login/x", strings.NewReader("user=alice"))
	for name, r := range map[string]*http.Request{
		"GET with a body":           body,
		"GET chunked":               chunked,
		"HEAD with a body":          headBody,
		"GET with user in query":    httptest.NewRequest(http.MethodGet, "/login?user=alice&pass=x", nil),
		"GET with User in query":    httptest.NewRequest(http.MethodGet, "/login.json?User=alice", nil),
		"OPTIONS with user[] query": httptest.NewRequest(http.MethodOptions, "/login?user[]=alice", nil),
		"GET with a bad query":      httptest.NewRequest(http.MethodGet, "/login?%zz", nil),
	} {
		refused(name, r, http.StatusMethodNotAllowed)
	}
	for _, target := range []string{"/login", "/login?next=/home", "/login/x", "/login.css", "/loginx"} {
		before := f.calls
		if f.do(httptest.NewRequest(http.MethodGet, target, nil)); f.calls != before+1 {
			t.Fatalf("GET %s should reach the origin", target)
		}
	}
	if f.do(httptest.NewRequest(http.MethodPost, "/loginx", strings.NewReader("a=b"))); f.calls == 0 {
		t.Fatal("POST /loginx is not near /login")
	}
	if err := CheckLoginRoutes([]LoginRoute{{Path: "/", UsernameField: "u", Success: LoginSuccess{Status: []int{302}, SetCookie: "sid"}}}); err == nil {
		t.Fatal(`accepted "/" as a login path`)
	}
}

// SF2: rotating /64s inside one /48 is one network.
func TestLoginIPv6NetworkIs48(t *testing.T) {
	f := newLoginFixture(t)
	r1, r2 := loginReq("alice", "", ""), loginReq("alice", "", "")
	r1.RemoteAddr, r2.RemoteAddr = "[2001:db8:7:1::1]:1", "[2001:db8:7:2::1]:1"
	if f.gate.loginNetwork(r1) != f.gate.loginNetwork(r2) {
		t.Fatal("two /64s of one /48 are two networks")
	}
	ch := decodeChallenge(t, f.do(r1))
	r := loginReq("alice", "", solveLogin(t, ch))
	r.RemoteAddr = "[2001:db8:7:3::9]:1"
	if rec := f.do(r); rec.Code != http.StatusFound {
		t.Fatalf("proof from another /64 of the /48: got %d", rec.Code)
	}
	r = loginReq("alice", "", "")
	r.RemoteAddr = "[2001:db8:7:4::1]:1"
	if ch := decodeChallenge(t, f.do(r)); ch.Level != LoginBaseLevel+1 {
		t.Fatalf("failure not counted for the /48: level %d", ch.Level)
	}
}

// SF3: per-network Argon2id slots, the wide limiter, a harder pre-check
// while the budget is full, pre-checked junk counted, bad pre-checks on
// the redeem path counted.
func TestLoginBudgetSaturation(t *testing.T) {
	f := newLoginFixture(t)
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	f.gate.loginSlots.acquire("192.0.2.0/24")
	f.gate.loginSlots.acquire("192.0.2.0/24")
	if rec := f.do(loginReq("alice", "192.0.2.1", solveLogin(t, ch))); rec.Code != http.StatusServiceUnavailable || f.calls != 0 {
		t.Fatalf("network out of slots: got %d", rec.Code)
	}
	ch2 := decodeChallenge(t, f.do(loginReq("bob", "198.51.100.1", "")))
	if rec := f.do(loginReq("bob", "198.51.100.1", solveLogin(t, ch2))); rec.Code != http.StatusFound {
		t.Fatalf("another network: got %d", rec.Code)
	}
	f.gate.loginSlots.release("192.0.2.0/24")
	f.gate.loginSlots.release("192.0.2.0/24")
	if len(f.gate.loginSlots.n) != 0 {
		t.Fatal("slots leak")
	}

	// A full budget makes new challenges ask for a harder pre-check.
	ch = decodeChallenge(t, f.do(loginReq("carol", "203.0.113.1", "")))
	f.gate.loginArgon.acquire(f.gate.loginArgon.max, 0)
	f.do(loginReq("carol", "203.0.113.1", solveLogin(t, ch)))
	f.gate.loginArgon.release(f.gate.loginArgon.max)
	hard := decodeChallenge(t, f.do(loginReq("carol", "203.0.113.1", "")))
	if hard.PreDifficulty != f.gate.loginPre+loginPreBoost {
		t.Fatalf("pre-check while busy: %d bits", hard.PreDifficulty)
	}
	// A pre-check solved at the old difficulty only is refused.
	argon, _ := pow.Params{Algorithm: hard.Algorithm, Difficulty: hard.Difficulty, MemoryMiB: hard.MemoryMiB}.Solve(context.Background(), hard.Challenge)
	easyPre := uint64(0)
	for !(pow.Params{Algorithm: pow.SHA256d, Difficulty: f.gate.loginPre}).Check(hard.Challenge, easyPre) ||
		(pow.Params{Algorithm: pow.SHA256d, Difficulty: hard.PreDifficulty}).Check(hard.Challenge, easyPre) {
		easyPre++
	}
	cred := "PoW " + hard.Challenge + ":" + strconv.FormatUint(argon, 10) + ":" + strconv.FormatUint(easyPre, 10)
	if rec := f.do(loginReq("carol", "203.0.113.1", cred)); rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), "pre-check") {
		t.Fatalf("easy pre-check on a hard challenge: %d %s", rec.Code, rec.Body)
	}
	f.now = f.now.Add(loginBusyFor * time.Second)
	if ch := decodeChallenge(t, f.do(loginReq("carol", "203.0.113.1", ""))); ch.PreDifficulty != f.gate.loginPre {
		t.Fatalf("pre-check after the busy minute: %d bits", ch.PreDifficulty)
	}

	// A right pre-check with a wrong nonce counts against the network.
	g := newLoginFixture(t)
	g.gate.loginParams = func(level int) pow.Params {
		return pow.Params{Algorithm: pow.Argon2id, Difficulty: 12, MemoryMiB: 8}
	}
	ch = decodeChallenge(t, g.do(loginReq("dave", "192.0.2.1", "")))
	pre, _ := pow.Params{Algorithm: pow.SHA256d, Difficulty: ch.PreDifficulty}.Solve(context.Background(), ch.Challenge)
	nonce := uint64(0)
	for (pow.Params{Algorithm: pow.Argon2id, Difficulty: 12, MemoryMiB: 8}).Check(ch.Challenge, nonce) {
		nonce++
	}
	g.do(loginReq("dave", "192.0.2.1", "PoW "+ch.Challenge+":"+strconv.FormatUint(nonce, 10)+":"+strconv.FormatUint(pre, 10)))
	net := g.gate.loginNetwork(loginReq("", "192.0.2.1", ""))
	if got := g.gate.login.Level(g.gate.loginAccount("erin"), net, g.now); got != LoginBaseLevel+1 {
		t.Fatalf("junk with a right pre-check: network level %d", got)
	}

	// Bad pre-checks on the redeem path use up the client's allowance.
	h := newLoginFixture(t)
	h.gate.loginPre = 20
	h.gate.loginLim = newRateLimiter(3)
	r := httptest.NewRequest(http.MethodPost, LoginChallengePath, strings.NewReader(`{"path":"/login","username":"a"}`))
	r.RemoteAddr = "192.0.2.9:1"
	ch = decodeChallenge(t, h.do(r))
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, LoginRedeemPath, nil)
		r.RemoteAddr = "192.0.2.9:1"
		r.Header.Set("Authorization", "PoW "+ch.Challenge+":1:1")
		h.do(r)
	}
	r = httptest.NewRequest(http.MethodPost, LoginChallengePath, strings.NewReader(`{"path":"/login","username":"a"}`))
	r.RemoteAddr = "192.0.2.9:1"
	if rec := h.do(r); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after bad pre-checks on redeem: got %d, want 429", rec.Code)
	}

	// One /16 shares the wide allowance across its /24s.
	w := newLoginFixture(t)
	w.gate.loginLim, w.gate.loginPrefixLim, w.gate.loginWideLim = newRateLimiter(100), newRateLimiter(100), newRateLimiter(3)
	codes := map[int]int{}
	for i := range 5 {
		codes[w.do(loginReq("alice", "10.20."+strconv.Itoa(i)+".1", "")).Code]++
	}
	if codes[http.StatusPaymentRequired] != 3 || codes[http.StatusTooManyRequests] != 2 {
		t.Fatalf("one /16: %v", codes)
	}
}

// SF5: success needs the positive criteria when set; an unwritten answer
// and a redirect the client steered are failures.
func TestLoginSuccessCriteria(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	route := LoginRoute{Path: "/login", Success: LoginSuccess{Status: []int{302, 200}, LocationNotPrefix: "/login"}}
	ok := func(r LoginRoute, o *outcomeWriter, steer ...string) bool { return r.succeeded(o, nil, steer, now) }
	if ok(route, &outcomeWriter{}) {
		t.Fatal("unwritten answer counted as success")
	}
	if !ok(route, &outcomeWriter{status: 302, location: "/home"}) {
		t.Fatal("plain success")
	}
	for _, steer := range []string{"https://site.example/home", "/home", "home", "/Home/"} {
		if ok(route, &outcomeWriter{status: 302, location: "/home"}, steer) {
			t.Fatalf("redirect steered by %q counted as success", steer)
		}
	}
	pre := route
	pre.Success.LocationPrefix = "/account"
	if ok(pre, &outcomeWriter{status: 302, location: "/home"}) || ok(pre, &outcomeWriter{status: 200}) || pre.succeeded(&outcomeWriter{status: 302, location: "/account/me"}, []string{"https://site.example/login"}, nil, now) == false {
		t.Fatal("location_prefix")
	}
	ck := route
	ck.Success.SetCookie = "auth"
	for _, c := range []struct {
		cookies []string
		want    bool
	}{
		{nil, false},
		{[]string{"other=1"}, false},
		{[]string{"auth="}, false},
		{[]string{"auth=x; Max-Age=0"}, false},
		{[]string{"auth=x; Expires=Thu, 01 Jan 1970 00:00:00 GMT"}, false},
		{[]string{"a=1", "auth=x; Path=/; HttpOnly"}, true},
	} {
		if got := ok(ck, &outcomeWriter{status: 302, location: "/home", cookies: c.cookies}, "/home"); got != c.want {
			t.Errorf("set_cookie %v: got %v", c.cookies, got)
		}
	}
	// End to end: an origin sending failed logins back to the Referer.
	f := newLoginFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, r.Referer(), http.StatusFound) }
	for range 3 {
		ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
		r := loginReq("alice", "192.0.2.1", solveLogin(t, ch))
		r.Header.Set("Referer", "https://site.example/dashboard")
		f.do(r)
	}
	if ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", ""))); ch.Level != LoginBaseLevel+3 {
		t.Fatalf("Referer-steered redirects reset the counters: level %d", ch.Level)
	}
	// An origin that writes nothing.
	f.respond = func(http.ResponseWriter, *http.Request) {}
	ch := decodeChallenge(t, f.do(loginReq("bob", "198.51.100.1", "")))
	f.do(loginReq("bob", "198.51.100.1", solveLogin(t, ch)))
	if ch := decodeChallenge(t, f.do(loginReq("bob", "198.51.100.1", ""))); ch.Level != LoginBaseLevel+1 {
		t.Fatalf("unwritten answer: level %d", ch.Level)
	}
	if CheckLoginRoutes([]LoginRoute{{Path: "/l", UsernameField: "u", Success: LoginSuccess{Status: []int{302}, SetCookie: "a b"}}}) == nil ||
		CheckLoginRoutes([]LoginRoute{{Path: "/l", UsernameField: "u", Success: LoginSuccess{Status: []int{302}, LocationPrefix: "x"}}}) == nil {
		t.Fatal("bad success criteria accepted")
	}
}

// R3-1: a 3xx success needs set_cookie or location_prefix, and with
// location_prefix the Referer/"next" heuristic is skipped, so honest
// logins that go back to where the user was are successes.
func TestLoginRedirectSuccessNeedsPositiveCriterion(t *testing.T) {
	for _, s := range []LoginSuccess{
		{Status: []int{302}},
		{Status: []int{200, 303}, LocationNotPrefix: "/login"},
		{Status: []int{399}},
	} {
		if CheckLoginRoutes([]LoginRoute{{Path: "/l", UsernameField: "u", Success: s}}) == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	for _, s := range []LoginSuccess{
		{Status: []int{302}, SetCookie: "sid"},
		{Status: []int{303}, LocationPrefix: "/app"},
		{Status: []int{200}},
		{Status: []int{200, 400}},
	} {
		if err := CheckLoginRoutes([]LoginRoute{{Path: "/l", UsernameField: "u", Success: s}}); err != nil {
			t.Errorf("refused %+v: %v", s, err)
		}
	}
	now := time.Unix(1_800_000_000, 0)
	route := LoginRoute{Path: "/login", Success: LoginSuccess{Status: []int{302}, LocationPrefix: "/app"}}
	if !route.succeeded(&outcomeWriter{status: 302, location: "/app/inbox"}, []string{"https://site.example/login"}, []string{"/app/inbox", "https://site.example/app/inbox"}, now) {
		t.Fatal("location_prefix: a login that follows \"next\" counted as failure")
	}
	// The Referer check stays, location_prefix or not.
	for _, ref := range []string{"https://x/app/inbox", "/app/inbox/", "/APP/inbox"} {
		if route.succeeded(&outcomeWriter{status: 302, location: "/app/inbox"}, []string{ref}, nil, now) {
			t.Fatalf("location_prefix: a redirect to the Referer %q counted as success", ref)
		}
	}
	// End to end: the origin sends honest logins back to "next".
	f := newLoginFixture(t)
	f.respond = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Query().Get("next"), http.StatusFound)
	}
	for range 3 {
		ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
		r := loginReq("alice", "192.0.2.1", solveLogin(t, ch))
		r.URL.RawQuery = "next=/home/inbox"
		f.do(r)
	}
	if ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", ""))); ch.Level != LoginBaseLevel {
		t.Fatalf("honest logins to \"next\" counted as failures: level %d", ch.Level)
	}
}

// R3-2: proofs with a right pre-check but a wrong nonce count on their own
// counter, worth at most +1 level: 8 of them no longer push a /24
// neighbour up by +6. They still use up the rate limits.
func TestLoginJunkProofsCapped(t *testing.T) {
	f := newLoginFixture(t)
	hard := pow.Params{Algorithm: pow.Argon2id, Difficulty: 12, MemoryMiB: 8}
	f.gate.loginParams = func(int) pow.Params { return hard }
	junk := func(ip string) int {
		ch := decodeChallenge(t, f.do(loginReq("dave", ip, "")))
		pre, _ := pow.Params{Algorithm: pow.SHA256d, Difficulty: ch.PreDifficulty}.Solve(context.Background(), ch.Challenge)
		nonce := uint64(0)
		for hard.Check(ch.Challenge, nonce) {
			nonce++
		}
		return f.do(loginReq("dave", ip, "PoW "+ch.Challenge+":"+strconv.FormatUint(nonce, 10)+":"+strconv.FormatUint(pre, 10))).Code
	}
	for range 8 {
		if code := junk("192.0.2.1"); code != http.StatusPaymentRequired {
			t.Fatalf("junk proof: %d", code)
		}
	}
	neighbour := f.gate.loginNetwork(loginReq("", "192.0.2.77", ""))
	if got := f.gate.login.Level(f.gate.loginAccount("erin"), neighbour, f.now); got != LoginBaseLevel+loginJunkCap {
		t.Fatalf("8 junk proofs pushed a /24 neighbour to level %d, want base+%d", got, loginJunkCap)
	}
	f.gate.loginLim = newRateLimiter(2)
	junk("198.51.100.1")
	if rec := f.do(loginReq("dave", "198.51.100.1", "")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("junk proofs not rate-limited: %d", rec.Code)
	}
}

// R4: the reviewer's PoC. location_prefix /account; the origin sends
// failed logins back to the Referer (Laravel's back()); the attacker sends
// a Referer under /account. Failures must still raise the level.
func TestLoginLocationPrefixRefererPoC(t *testing.T) {
	f := newLoginFixture(t)
	f.gate.opt.LoginRoutes[0].Success = LoginSuccess{Status: []int{302}, LocationPrefix: "/account"}
	if err := CheckLoginRoutes(f.gate.opt.LoginRoutes); err != nil {
		t.Fatal(err)
	}
	f.respond = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, r.Referer(), http.StatusFound) }
	prev := 0
	for i := range 6 {
		ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
		if i > 0 && ch.Level <= prev {
			t.Fatalf("attempt %d: level %d did not rise (was %d)", i+1, ch.Level, prev)
		}
		prev = ch.Level
		r := loginReq("alice", "192.0.2.1", solveLogin(t, ch))
		r.Header.Set("Referer", "https://x/account/")
		if rec := f.do(r); rec.Code != http.StatusFound {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	if ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", ""))); ch.Level <= LoginBaseLevel+5 {
		t.Fatalf("level after 6 Referer-steered failures: %d", ch.Level)
	}
}

// R5: with location_prefix, a redirect needs one absolute http(s)
// Referer. The reviewer's two cases (no Referer, the origin sending the
// failure to the session's previous URL under the prefix; an empty
// Referer) escalate; an honest login with a proper Referer succeeds.
func TestLoginLocationPrefixNeedsReferer(t *testing.T) {
	f := newLoginFixture(t) // location_prefix "/home"
	// Laravel's url()->previous(): the Referer, else the session's last page.
	f.respond = func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if f.succeed(r.PostForm.Get("user")) {
			http.Redirect(w, r, "/home", http.StatusFound)
			return
		}
		back := r.Referer()
		if back == "" {
			back = "/home/settings"
		}
		http.Redirect(w, r, back, http.StatusFound)
	}
	for i, ref := range [][]string{nil, {""}} {
		ip, user := fmt.Sprintf("203.0.%d.1", 113+i), fmt.Sprintf("victim%d", i)
		prev := 0
		for n := range 6 {
			ch := decodeChallenge(t, f.do(loginReq(user, ip, "")))
			if n > 0 && ch.Level <= prev {
				t.Fatalf("Referer %q, attempt %d: level %d did not rise", ref, n+1, ch.Level)
			}
			prev = ch.Level
			r := loginReq(user, ip, solveLogin(t, ch))
			r.Header["Referer"] = ref
			f.do(r)
		}
	}
	f.succeed = func(u string) bool { return u == "bob" }
	for range 3 {
		if lvl := f.attempt(t, "bob", "198.51.100.1"); lvl != LoginBaseLevel {
			t.Fatalf("honest login with a Referer: level %d", lvl)
		}
	}
	now := time.Unix(1_800_000_000, 0)
	route := LoginRoute{Path: "/login", Success: LoginSuccess{Status: []int{302}, LocationPrefix: "/home"}}
	for _, ref := range [][]string{nil, {""}, {" "}, {"/login"}, {"site.example/login"}, {"ftp://site.example/login"}, {"https:///login"}, {"https://a/login", "https://b/login"}} {
		if route.succeeded(&outcomeWriter{status: 302, location: "/home"}, ref, nil, now) {
			t.Errorf("Referer %q accepted", ref)
		}
	}
	for _, ref := range []string{"https://site.example/login", "http://site.example/", "HTTPS://Site.Example/login?x=1"} {
		if !route.succeeded(&outcomeWriter{status: 302, location: "/home"}, []string{ref}, nil, now) {
			t.Errorf("Referer %q refused", ref)
		}
	}
	// set_cookie needs no Referer; a non-redirect answer neither.
	ck := LoginRoute{Path: "/login", Success: LoginSuccess{Status: []int{302}, LocationPrefix: "/home", SetCookie: "auth"}}
	if !ck.succeeded(&outcomeWriter{status: 302, location: "/home", cookies: []string{"auth=1"}}, nil, nil, now) {
		t.Error("set_cookie")
	}
}

// SF6: quickselect agrees with sorting.
func TestNthSmallest(t *testing.T) {
	for n := 1; n < 200; n += 7 {
		a := make([]float64, n)
		for i := range a {
			a[i] = float64((i * 7919) % 13) // many ties
		}
		sorted := append([]float64(nil), a...)
		slices.Sort(sorted)
		for k := 0; k < n; k++ {
			b := append([]float64(nil), a...)
			if got := nthSmallest(b, k); got != sorted[k] {
				t.Fatalf("n=%d k=%d: %v, want %v", n, k, got, sorted[k])
			}
		}
	}
}

// Nits: the origin gets the checked body with its real length; clients
// without an IP share one bucket.
func TestLoginForwardingAndNoIP(t *testing.T) {
	f := newLoginFixture(t)
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	r := loginReq("alice", "192.0.2.1", solveLogin(t, ch))
	n := r.ContentLength
	r.ContentLength, r.TransferEncoding = -1, []string{"chunked"}
	if rec := f.do(r); rec.Code != http.StatusFound || f.lastCL != n || len(f.lastTE) != 0 {
		t.Fatalf("forwarded with length %d, TE %v (want %d, none): %d", f.lastCL, f.lastTE, n, rec.Code)
	}
	g := newLoginFixture(t)
	g.gate.loginLim = newRateLimiter(2)
	codes := map[int]int{}
	for i := range 3 {
		r := loginReq("alice", "", "")
		r.RemoteAddr = "unix-socket-" + strconv.Itoa(i)
		codes[g.do(r).Code]++
	}
	if codes[http.StatusTooManyRequests] != 1 {
		t.Fatalf("clients without an IP: %v", codes)
	}
}
