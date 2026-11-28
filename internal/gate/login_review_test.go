package gate

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// R6-1: a challenge lives long enough for a browser at its level (8x the
// 4-worker average, 2 min to 6 h), so raising a victim's level cannot make
// honest browsers miss the expiry; and a long life still opens no
// stockpile, because the first attempt makes the other proofs stale.
func TestLoginTTLScalesWithLevel(t *testing.T) {
	browser := func(level int) time.Duration { // the README's 4-worker column
		return time.Duration(float64(uint64(1)<<(level-loginLevelToArgon))*loginBrowserMs/4) * time.Millisecond
	}
	if loginTTL(LoginBaseLevel) != loginChallengeTTL || loginTTL(LoginMaxLevel) != loginMaxTTL {
		t.Fatalf("bounds: %v %v", loginTTL(LoginBaseLevel), loginTTL(LoginMaxLevel))
	}
	for lvl := LoginBaseLevel; lvl <= LoginMaxLevel; lvl++ {
		if ttl := loginTTL(lvl); ttl < 4*browser(lvl) || ttl < loginChallengeTTL || ttl > loginMaxTTL {
			t.Errorf("level %d: TTL %v for a %v browser average", lvl, ttl, browser(lvl))
		}
	}

	f := newLoginFixture(t)
	for range 6 { // level 31: a browser needs ~12 min on average
		f.attempt(t, "alice", "192.0.2.1")
	}
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	if ch.Level != 31 || !ch.ExpiresAt.Equal(f.now.Add(loginTTL(31)).UTC()) {
		t.Fatalf("level %d expires %v", ch.Level, ch.ExpiresAt)
	}
	spare := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	if rec := f.do(loginReq("alice", "192.0.2.1", solveLogin(t, ch))); rec.Code != http.StatusFound {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// The spare proof of the same level is now stale: no stockpile.
	if rec := f.do(loginReq("alice", "192.0.2.1", solveLogin(t, spare))); rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), "below the current level") {
		t.Fatalf("stockpiled proof: %d %s", rec.Code, rec.Body)
	}
	// A slow solve at the top level is still accepted.
	ch = decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	f.now = f.now.Add(loginTTL(ch.Level) - time.Second)
	if rec := f.do(loginReq("alice", "192.0.2.1", solveLogin(t, ch))); ch.Level != LoginMaxLevel || rec.Code != http.StatusFound {
		t.Fatalf("level %d proof solved in %v refused: %d %s", ch.Level, loginTTL(ch.Level), rec.Code, rec.Body)
	}
	if f.calls != 8 {
		t.Fatalf("origin calls %d", f.calls)
	}
}

// R6-1/5: a wrong nonce keeps its token spent for loginChallengeTTL only;
// a right one until the token expires. Login tokens have their own quota
// in the spent set, so they cannot fill the payers' room.
func TestLoginSpentEntries(t *testing.T) {
	f := newLoginFixture(t)
	for range 6 {
		f.attempt(t, "alice", "192.0.2.1")
	}
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	c, err := f.gate.sign.verify(ch.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	expiry := func(id string) int64 { return f.gate.spent.ids[compactID(loginSpentKey(id))].expires }
	parts := strings.Split(strings.TrimPrefix(solveLogin(t, ch), "PoW "), ":")
	n, _ := strconv.ParseUint(parts[1], 10, 64)
	p := c.powParams()
	bad := n + 1
	for p.Check(ch.Challenge, bad) {
		bad++
	}
	f.do(loginReq("alice", "192.0.2.1", "PoW "+ch.Challenge+":"+strconv.FormatUint(bad, 10)+":"+parts[2]))
	if got, want := expiry(c.ID), f.now.Add(loginChallengeTTL).Unix(); got != want {
		t.Fatalf("junk entry expires at %d, want %d", got, want)
	}
	ch2 := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	c2, _ := f.gate.sign.verify(ch2.Challenge)
	f.do(loginReq("alice", "192.0.2.1", solveLogin(t, ch2)))
	if got := expiry(c2.ID); got != c2.Expires || c2.Expires <= f.now.Add(loginChallengeTTL).Unix() {
		t.Fatalf("valid entry expires at %d, token at %d", got, c2.Expires)
	}

	s := newSpentSet()
	s.maxLogin = 2
	now := time.Unix(1_800_000_000, 0)
	for i := range 2 {
		if err := s.use(loginSpentKey(strings.Repeat(strconv.Itoa(i), 32)), now.Add(time.Hour), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.use(loginSpentKey(strings.Repeat("7", 32)), now.Add(time.Hour), now); err != errBusy {
		t.Fatalf("login over quota: %v", err)
	}
	if err := s.use(spentKey("L402", strings.Repeat("7", 32)), now.Add(time.Hour), now); err != nil {
		t.Fatalf("a payer refused because logins are full: %v", err)
	}
	s.del(compactID(loginSpentKey(strings.Repeat("0", 32))))
	if err := s.use(loginSpentKey(strings.Repeat("8", 32)), now.Add(time.Hour), now); err != nil || s.logins != 2 {
		t.Fatalf("quota after a delete: %v %d", err, s.logins)
	}
}

// R6-2: a stale proof is refused before a slot, an Argon2id check or the
// spent set are touched.
func TestLoginStaleBeforeArgon(t *testing.T) {
	f := newLoginFixture(t)
	stale := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	f.attempt(t, "alice", "192.0.2.1") // the level rises
	f.gate.loginSlots = newPrefixSlots(0)
	f.gate.loginArgon = newArgonBudget(0)
	for _, path := range []string{"/login", LoginRedeemPath} {
		var r *http.Request
		if path == "/login" {
			r = loginReq("alice", "192.0.2.1", solveLogin(t, stale))
		} else {
			r = httptest.NewRequest(http.MethodPost, LoginRedeemPath, nil)
			r.RemoteAddr = "192.0.2.1:1234"
			r.Header.Set("Authorization", solveLogin(t, stale))
		}
		rec := f.do(r)
		if rec.Code == http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "below the current level") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	c, _ := f.gate.sign.verify(stale.Challenge)
	if f.gate.spent.has(loginSpentKey(c.ID)) {
		t.Fatal("stale proof was spent")
	}
}

// R6-3: "#" and "?" in the decoded path never reach the origin, raw or
// encoded, over a real connection.
func TestLoginHashAndQuestionInPath(t *testing.T) {
	f := newLoginFixture(t)
	srv := httptest.NewServer(f.gate)
	defer srv.Close()
	body := "user=alice&pass=x"
	for _, target := range []string{"/login#a", "/login%23a", "/login%3Fa", "/login%2523a", "/premium/x%23y", "/free%3F"} {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn.Write([]byte("POST " + target + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\nConnection: close\r\n\r\n" + body))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusBadRequest || f.calls != 0 {
			t.Fatalf("%s: %d, origin calls %d", target, resp.StatusCode, f.calls)
		}
	}
	if why := checkPath(&url.URL{Path: "/c#/x"}, nil); why == "" {
		t.Fatal("checkPath allows #")
	}
}

// R6-4: every spelling an origin or its database may merge is one account.
func TestLoginAccountName(t *testing.T) {
	same := [][]string{
		{"alice", "Alice", " ALICE ", "ＡＬＩＣＥ", "Àlice", "al\u200Bice", "alı̇ce", "ALİCE", "alıce", "a\u0301lice", "al\u00ADice"},
		{"strasse", "straße", "STRASSE", "STRAẞE"},
		{"élise@example.com", "ELISE@EXAMPLE.COM", "e\u0301lise@example.com"},
		{"file", "ﬁle"},
	}
	for _, g := range same {
		for _, v := range g[1:] {
			if loginAccountName(v) != loginAccountName(g[0]) {
				t.Errorf("%q and %q are different accounts", v, g[0])
			}
		}
	}
	for _, d := range [][2]string{{"alice", "alice+x"}, {"alice", "a.lice"}, {"alice", "alicf"}} {
		if loginAccountName(d[0]) == loginAccountName(d[1]) {
			t.Errorf("%q and %q merged", d[0], d[1])
		}
	}
	// Failures under other spellings, from four networks, count for the
	// account: from a fifth network, "alice" now costs the account term.
	f := newLoginFixture(t)
	for i, u := range []string{"Àlice", "ＡＬＩＣＥ", "al\u200Bice", "ALİCE"} {
		f.attempt(t, u, "198.51."+strconv.Itoa(100+i)+".1")
	}
	if ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", ""))); ch.Level != LoginBaseLevel+loginAcctCap {
		t.Fatalf("level %d after 4 failures under other spellings", ch.Level)
	}
}

// R6 nits: no method override on the real route; a redirect to another
// host is never a success under location_prefix.
func TestLoginOverrideAndHost(t *testing.T) {
	f := newLoginFixture(t)
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	proof := solveLogin(t, ch)
	reqs := []*http.Request{}
	for _, h := range methodOverrideHeaders {
		r := loginReq("alice", "192.0.2.1", proof)
		r.Header.Set(h, "PUT")
		reqs = append(reqs, r)
	}
	q := loginReq("alice", "192.0.2.1", proof)
	q.URL.RawQuery = "_method=PUT"
	reqs = append(reqs, q)
	form := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("user=alice&pass=x&_METHOD=PUT"))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, body := range []string{`{"user":"alice","_method":"PUT"}`, `{"user":"alice","_method":["PUT"]}`} {
		j := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		j.Header.Set("Content-Type", "application/json")
		reqs = append(reqs, j)
	}
	reqs = append(reqs, form)
	for i, r := range reqs {
		r.RemoteAddr = "192.0.2.1:1234"
		r.Header.Set("Authorization", proof)
		if rec := f.do(r); rec.Code != http.StatusBadRequest || f.calls != 0 {
			t.Fatalf("override %d: %d, origin calls %d", i, rec.Code, f.calls)
		}
	}

	now := time.Unix(1_800_000_000, 0)
	route := LoginRoute{Path: "/login", Success: LoginSuccess{Status: []int{302}, LocationPrefix: "/home"}}
	ref := []string{"https://site.example/login"}
	for loc, want := range map[string]bool{
		"/home": true, "https://site.example/home": true, "https://SITE.example:8443/home": true,
		"https://evil.example/home": false, "//evil.example/home": false, "https://site.example.evil/home": false,
	} {
		if got := route.succeeded(&outcomeWriter{host: "site.example", status: 302, location: loc}, ref, nil, now); got != want {
			t.Errorf("%q: got %v, want %v", loc, got, want)
		}
	}
	// End to end: failures sent to another host under the prefix.
	g := newLoginFixture(t)
	g.respond = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/home", http.StatusFound)
	}
	for range 3 {
		g.attempt(t, "bob", "198.51.100.1")
	}
	if ch := decodeChallenge(t, g.do(loginReq("bob", "198.51.100.1", ""))); ch.Level != LoginBaseLevel+3 {
		t.Fatalf("cross-host redirects counted as successes: level %d", ch.Level)
	}
}
