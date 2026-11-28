package gate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
			Success: LoginSuccess{Status: []int{302}, LocationNotPrefix: "/login"}}},
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
	// A success resets the account, but not the network's own counter.
	f.succeed = func(string) bool { return true }
	f.attempt(t, "alice", "192.0.2.1")
	f.succeed = func(string) bool { return false }
	if got := f.gate.login.Level(f.gate.loginAccount("alice"), f.gate.loginNetwork(loginReq("", "192.0.2.1", "")), f.now); got != 26 {
		// The network still weighs 3 failures (9 halved twice, plus one): +3.
		t.Fatalf("after success: level %d, want 26", got)
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
	f.gate.loginPre = 4
	ch.PreDifficulty = 4
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
	ok := LoginSuccess{Status: []int{302}}
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
		if got := route.succeeded(302, loc); got != want {
			t.Errorf("%q: got %v, want %v", loc, got, want)
		}
	}
}
