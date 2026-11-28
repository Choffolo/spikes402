package gate

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
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

// R6-1/5, R7-2: a token is checked at most twice: after a wrong nonce,
// one retry, no sooner than loginRetryAfter; then it stays spent until it
// expires. Nobody but its holder, from its network, can use up the retry.
// Login tokens have their own quota in the spent set, so they cannot fill
// the payers' room; junk entries are capped and forgotten first.
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
	right := solveLogin(t, ch)
	parts := strings.Split(strings.TrimPrefix(right, "PoW "), ":")
	n, _ := strconv.ParseUint(parts[1], 10, 64)
	p := c.powParams()
	bad := n + 1
	for p.Check(ch.Challenge, bad) {
		bad++
	}
	wrong := "PoW " + ch.Challenge + ":" + strconv.FormatUint(bad, 10) + ":" + parts[2]
	send := func(ip, auth string) string {
		rec := f.do(loginReq("alice", ip, auth))
		return strconv.Itoa(rec.Code) + " " + rec.Body.String()
	}
	// Someone else (another network) cannot burn the token.
	if got := send("198.51.100.1", wrong); !strings.Contains(got, errLoginBinding.Error()) {
		t.Fatalf("token from another network: %s", got)
	}
	if f.gate.spent.has(loginSpentKey(c.ID)) {
		t.Fatal("token spent by another network")
	}
	if got := send("192.0.2.1", wrong); !strings.Contains(got, errBadProof.Error()) {
		t.Fatalf("first wrong nonce: %s", got)
	}
	if got := send("192.0.2.1", right); !strings.Contains(got, errReplay.Error()) {
		t.Fatalf("retry before loginRetryAfter: %s", got)
	}
	f.now = f.now.Add(loginRetryAfter)
	if got := send("192.0.2.1", wrong); !strings.Contains(got, errBadProof.Error()) {
		t.Fatalf("the one retry: %s", got)
	}
	for _, d := range []time.Duration{loginRetryAfter, time.Hour} {
		f.now = f.now.Add(d)
		if got := send("192.0.2.1", right); !strings.Contains(got, errReplay.Error()) {
			t.Fatalf("a third check after %v: %s", d, got)
		}
	}
	if e := f.gate.spent.ids[compactID(loginSpentKey(c.ID))]; e.expires != c.Expires || e.retries != 2 || f.gate.spent.junk != 1 {
		t.Fatalf("entry %+v, junk %d", e, f.gate.spent.junk)
	}
	// A retry with the right nonce gets through and is no longer junk.
	g := newLoginFixture(t)
	for range 6 {
		g.attempt(t, "alice", "192.0.2.1")
	}
	ch2 := decodeChallenge(t, g.do(loginReq("alice", "192.0.2.1", "")))
	c2, _ := g.gate.sign.verify(ch2.Challenge)
	right2 := solveLogin(t, ch2)
	p2 := strings.Split(strings.TrimPrefix(right2, "PoW "), ":")
	n2, _ := strconv.ParseUint(p2[1], 10, 64)
	bad2 := n2 + 1
	for c2.powParams().Check(ch2.Challenge, bad2) {
		bad2++
	}
	g.do(loginReq("alice", "192.0.2.1", "PoW "+ch2.Challenge+":"+strconv.FormatUint(bad2, 10)+":"+p2[2]))
	g.now = g.now.Add(loginRetryAfter)
	if rec := g.do(loginReq("alice", "192.0.2.1", right2)); rec.Code != http.StatusFound {
		t.Fatalf("right nonce on the retry: %d %s", rec.Code, rec.Body)
	}
	if e := g.gate.spent.ids[compactID(loginSpentKey(c2.ID))]; e.retries != 0 || e.released || g.gate.spent.junk != 0 {
		t.Fatalf("right retry entry %+v, junk %d", e, g.gate.spent.junk)
	}

	// Junk entries are capped; forgetting one never forgets a right proof.
	j := newSpentSet()
	j.maxJunk = 2
	now0 := time.Unix(1_800_000_000, 0)
	id := func(i int) string { return loginSpentKey(strings.Repeat(strconv.Itoa(i), 32)) }
	j.beginLogin(id(9), now0.Add(time.Hour), now0, loginRetryAfter) // a right proof
	for i := range 5 {
		j.beginLogin(id(i), now0.Add(time.Hour), now0, loginRetryAfter)
		j.wrongLogin(id(i), now0)
	}
	if j.junk != 2 || !j.has(id(9)) || j.logins != 3 {
		t.Fatalf("junk %d, logins %d, right kept %v", j.junk, j.logins, j.has(id(9)))
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

// R6-3, R7-1: "#" and "?" in the decoded path are refused, raw or
// encoded, when the path cut there is near a login route (any case, the
// near variants) or protected differently; elsewhere they are served, so
// a site without login routes keeps "/wiki/C%23".
func TestLoginHashAndQuestionInPath(t *testing.T) {
	f := newLoginFixture(t)
	srv := httptest.NewServer(f.gate)
	defer srv.Close()
	raw := func(method, target string) int {
		body := "user=alice&pass=x"
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.Write([]byte(method + " " + target + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\nConnection: close\r\n\r\n" + body))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	for _, target := range []string{"/login#a", "/login%23a", "/login%3Fa", "/login%2523a", "/LOGIN%23a", "/Login.json%3Fx", "/login/x%23y", "/login;v%23", "/login.%3F", "/log%23in/../login"} {
		for _, m := range []string{"POST", "GET"} {
			if code := raw(m, target); code != http.StatusBadRequest || f.calls != 0 {
				t.Fatalf("%s %s: %d, origin calls %d", m, target, code, f.calls)
			}
		}
	}
	f.respond = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }
	for _, target := range []string{"/wiki/C%23", "/free%3F", "/logins%23"} {
		if code := raw("GET", target); code != http.StatusOK {
			t.Fatalf("GET %s: %d", target, code)
		}
	}
	// A deployment without login routes serves C%23 and only refuses a
	// cut that changes what is protected.
	g := newFixture(t)
	if rec := g.get("/wiki/C%23", "", ""); rec.Code != http.StatusOK || rec.Body.String() != "content of /wiki/C#" {
		t.Fatalf("no login routes, /wiki/C%%23: %d %s", rec.Code, rec.Body)
	}
	if rec := g.get("/premium/x%23y", "", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("/premium/x%%23y: %d", rec.Code)
	}
	if rec := g.get("/doc.pdf%3Fx", "", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("/doc.pdf%%3Fx (the cut is protected, the whole is not): %d", rec.Code)
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
	wide := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("user=alice&pass=x&_\uFF2DETHOD=PUT"))
	wide.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqs = append(reqs, wide)
	zw := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"user":"alice","_me\u200bthod":"PUT"}`))
	zw.Header.Set("Content-Type", "application/json")
	reqs = append(reqs, zw)
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
		if got := route.succeeded(&outcomeWriter{hosts: []string{"site.example"}, status: 302, location: loc}, ref, nil, now); got != want {
			t.Errorf("%q: got %v, want %v", loc, got, want)
		}
	}
	// public_hosts: www and apex are one site.
	www := &outcomeWriter{hosts: []string{"example.com", "www.example.com"}, status: 302}
	for loc, want := range map[string]bool{"https://www.example.com/home": true, "https://WWW.example.com:443/home": true, "https://evil.example/home": false} {
		www.location = loc
		if got := route.succeeded(www, ref, nil, now); got != want {
			t.Errorf("public_hosts %q: got %v, want %v", loc, got, want)
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

// R8-2: a cut must not change the price: "#" and "?" are refused on any
// path that is protected or priced before or after the cut.
func TestHashCutCannotChangePrice(t *testing.T) {
	f := newFixture(t)
	opt := f.gate.opt
	opt.Protect = []string{"/premium/**"}
	opt.Prices = []PriceRule{{Pattern: "/premium/*.jpg", PriceSats: 1}, {Pattern: "/premium/**", PriceSats: 100}}
	opt.LoginRoutes = nil
	f.gate = New(opt)
	f.gate.now = func() time.Time { return f.now }
	for _, target := range []string{"/premium/report.pdf%23.jpg", "/premium/report.pdf%3F.jpg", "/premium/report.pdf%2523.jpg", "/PREMIUM/report.pdf%23.jpg"} {
		if rec := f.get(target, "", ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", target, rec.Code)
		}
	}
	if rec := f.get("/premium/report.pdf", "", ""); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("/premium/report.pdf: %d", rec.Code)
	}
	if rec := f.get("/wiki/C%23", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("/wiki/C%%23: %d", rec.Code)
	}
}

// R8-1: the reviewer's sequence. K's retry is in progress when J's wrong
// try needs room: K must not be forgotten, and K's right proof must not
// be replayable. And rightLogin re-spends a missing entry.
func TestLoginJunkCapKeepsRetryInProgress(t *testing.T) {
	s := newSpentSet()
	s.maxJunk = 1
	now := time.Unix(1_800_000_000, 0)
	exp := now.Add(time.Hour)
	K, J := loginSpentKey(strings.Repeat("a", 32)), loginSpentKey(strings.Repeat("b", 32))
	if err := s.beginLogin(K, exp, now, loginRetryAfter); err != nil {
		t.Fatal(err)
	}
	s.wrongLogin(K, now)
	now = now.Add(loginRetryAfter)
	if err := s.beginLogin(K, exp, now, loginRetryAfter); err != nil { // K's retry starts
		t.Fatal(err)
	}
	if err := s.beginLogin(J, exp, now, loginRetryAfter); err != nil {
		t.Fatal(err)
	}
	s.wrongLogin(J, now) // the cap is full: must not evict K
	if !s.has(K) {
		t.Fatal("a retry in progress was forgotten")
	}
	s.rightLogin(K, exp, now) // K's retry was right
	if err := s.beginLogin(K, exp, now.Add(loginRetryAfter), loginRetryAfter); err != errReplay {
		t.Fatalf("replay of K's right proof: %v", err)
	}
	// Belt and braces: a missing entry is spent again by rightLogin.
	L := loginSpentKey(strings.Repeat("c", 32))
	s.rightLogin(L, exp, now)
	if err := s.beginLogin(L, exp, now, loginRetryAfter); err != errReplay {
		t.Fatalf("replay after a missing entry: %v", err)
	}
}

// R8-3: X-Original-Method and X-Forwarded-Method are refused on the route
// and on its near paths.
func TestLoginOriginalAndForwardedMethod(t *testing.T) {
	f := newLoginFixture(t)
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	proof := solveLogin(t, ch)
	for _, h := range []string{"X-Original-Method", "X-Forwarded-Method", "x-forwarded-method"} {
		r := loginReq("alice", "192.0.2.1", proof)
		r.Header.Set(h, "PUT")
		if rec := f.do(r); rec.Code != http.StatusBadRequest || f.calls != 0 {
			t.Fatalf("POST with %s: %d, origin calls %d", h, rec.Code, f.calls)
		}
		g := httptest.NewRequest(http.MethodGet, "/login", nil)
		g.Header.Set(h, "POST")
		if rec := f.do(g); rec.Code != http.StatusMethodNotAllowed || f.calls != 0 {
			t.Fatalf("GET with %s: %d", h, rec.Code)
		}
	}
	if rec := f.do(loginReq("alice", "192.0.2.1", proof)); rec.Code != http.StatusFound {
		t.Fatalf("the same proof without the header: %d", rec.Code)
	}
}

// R8-4: letters that do not decompose are spelled out.
func TestLoginAccountNameLetters(t *testing.T) {
	for _, g := range [][]string{
		{"aesir", "æsir", "ÆSIR"}, {"oeuvre", "œuvre", "Œuvre"}, {"soren", "søren", "SØREN"},
		{"lodz", "łódź", "ŁÓDŹ"}, {"dorde", "đorđe", "Đorđe"}, {"eddi", "eðði", "EÐÐI"},
		{"thor", "þor", "Þor"}, {"strasse", "straße"}, {"hamid", "ħamid"},
	} {
		for _, v := range g[1:] {
			if loginAccountName(v) != loginAccountName(g[0]) {
				t.Errorf("%q and %q are different accounts", v, g[0])
			}
		}
	}
}

// R9: the request's Host is never this site. An origin that echoes the
// Host (preserve_host, or a framework trusting X-Forwarded-Host) into
// Location cannot turn an attacker's "Host: evil" into a success, with or
// without public_hosts; only public_hosts and the origin's own host count.
func TestLoginAttackerHostEchoedIntoLocation(t *testing.T) {
	echo := func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
			host = xfh
		}
		http.Redirect(w, r, "https://"+host+"/home", http.StatusFound)
	}
	run := func(name string, hosts []string, user, host, xfh string, wantLevel int) {
		t.Helper()
		f := newLoginFixture(t)
		f.gate.loginHosts = hosts
		f.respond = echo
		for range 3 {
			ch := decodeChallenge(t, f.do(loginReq(user, "192.0.2.1", "")))
			r := loginReq(user, "192.0.2.1", solveLogin(t, ch))
			r.Host = host
			if xfh != "" {
				r.Header.Set("X-Forwarded-Host", xfh)
			}
			f.do(r)
		}
		if ch := decodeChallenge(t, f.do(loginReq(user, "192.0.2.1", ""))); ch.Level != wantLevel {
			t.Errorf("%s: level %d, want %d", name, ch.Level, wantLevel)
		}
	}
	failed := LoginBaseLevel + 3
	run("no public_hosts, attacker Host", nil, "alice", "evil.example", "", failed)
	run("no public_hosts, attacker X-Forwarded-Host", nil, "alice", "example.com", "evil.example", failed)
	run("origin host only, attacker Host", []string{"127.0.0.1:8080"}, "alice", "evil.example", "", failed)
	run("public_hosts, attacker Host", []string{"example.com"}, "alice", "evil.example", "", failed)
	run("public_hosts, attacker X-Forwarded-Host", []string{"example.com"}, "alice", "example.com", "evil.example", failed)
	run("public_hosts, honest Host", []string{"example.com"}, "alice", "example.com", "", LoginBaseLevel)
	run("origin host, rewritten Host", []string{"127.0.0.1:8080"}, "alice", "127.0.0.1:8080", "", LoginBaseLevel)

	// Relative Locations need no host list.
	now := time.Unix(1_800_000_000, 0)
	route := LoginRoute{Path: "/login", Success: LoginSuccess{Status: []int{302}, LocationPrefix: "/home"}}
	ref := []string{"https://site.example/login"}
	for loc, want := range map[string]bool{"/home": true, "home": true, "https://site.example/home": false, "https://:443/home": false} {
		if got := route.succeeded(&outcomeWriter{status: 302, location: loc}, ref, nil, now); got != want {
			t.Errorf("no hosts, %q: got %v, want %v", loc, got, want)
		}
	}
}
