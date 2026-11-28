package gate

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Choffolo/spikes402/pow"
	"golang.org/x/text/unicode/norm"
)

// Login protection: every attempt on a configured login route costs an
// Argon2id proof-of-work, always, for everyone, and each failure makes the
// next attempt cost more. Nothing is ever blocked; the wall only gets more
// expensive. Login routes fail closed: a request that cannot be checked
// never reaches the origin. See "Login protection" in the README.
//
// The level is in sha256d-equivalent bits: one browser Argon2id attempt at
// 16 MiB costs about as much as 2^16 sha256d attempts, so the puzzle has
// level-16 Argon2id bits. Each level doubles the work.
const (
	LoginBaseLevel      = 23
	LoginMaxLevel       = 33
	LoginArgonMemoryMiB = 16
	loginLevelToArgon   = 16
	loginAcctCap        = 4 // most extra levels failures from anywhere can add to one account
	loginNetCap         = 6 // most extra levels failures from one network can add
	loginJunkCap        = 1 // most extra levels junk proofs from one network can add
	// An account's own failures add nothing until they round to
	// loginAcctShow, then loginAcctCap at once: anyone may ask for a
	// challenge for any username, so the level shows only that one bit.
	loginAcctShow = 4

	// Pre-check: before any Argon2id check, the proof must carry a
	// sha256d solution of this many bits for the same token. It costs the
	// client about 2^18 hashes (~0.15 s in one browser worker, more than
	// the server's Argon2id check in native code) and the server one hash,
	// so junk proofs cannot buy Argon2id checks for free.
	LoginPreBits = 18
	// Logins have their own Argon2id memory budget (8 checks at once), so
	// a flood of login proofs never starves the API puzzle, or the reverse.
	loginArgonMaxMiB = 128
	// Per network prefix (/24 IPv4, /48 IPv6), challenges and checks
	// together, on top of the per-client challenges_per_minute; and per
	// wider prefix (/16 IPv4, /40 IPv6).
	loginPrefixFactor = 4
	loginWideFactor   = 32
	// At most this many Argon2id checks at once for one /24 or /48, so
	// one network cannot fill the login budget alone.
	loginPrefixSlots = 2
	// While the login budget has been full in the last minute, new
	// challenges ask for this many more pre-check bits (16x the work).
	loginPreBoost = 4
	loginBusyFor  = 60      // seconds
	loginNoIPKey  = "no-ip" // one shared bucket for clients without an IP

	// A challenge lives long enough for a slow browser to solve it (see
	// loginTTL): at least loginChallengeTTL, at most loginMaxTTL.
	loginChallengeTTL = 120 * time.Second
	loginMaxTTL       = 6 * time.Hour
	loginTTLFactor    = 8  // times the expected solve time of a 4-worker browser
	loginBrowserMs    = 86 // one 16 MiB Argon2id attempt in one browser worker (V8, measured)
	loginPassTTL      = 60 * time.Second
	loginRetryAfter   = 2 * time.Minute // a token's one retry after a wrong nonce, no sooner
	loginMaxBody      = 16 << 10
	loginMaxUsername  = 256
	loginPassCookie   = passPrefix + "login"

	LoginChallengePath = ReservedPrefix + "login-challenge"
	LoginRedeemPath    = ReservedPrefix + "login-redeem"
	LoginScriptPath    = ReservedPrefix + "login.js"
	Argon2idWorkerPath = ReservedPrefix + "argon2id-worker.js"
)

// loginSteps are the extra levels after 0, 1, 2, ... 7+ failures.
var loginSteps = [...]int{0, 1, 2, 3, 4, 6, 8, 10}

// LoginRoute declares one login endpoint. Routes are configured, never
// guessed: a wrong guess would be a hole in the wall.
type LoginRoute struct {
	Method        string       `json:"method,omitempty"` // default "POST"
	Path          string       `json:"path"`             // exact path, e.g. "/login"
	UsernameField string       `json:"username_field"`   // form or top-level JSON field
	Success       LoginSuccess `json:"success"`
	Price         *LoginPrice  `json:"price,omitempty"` // paying instead of the proof-of-work; overrides login_price
}

// LoginSuccess says how the origin answers a successful login. Any other
// answer counts as a failure.
type LoginSuccess struct {
	Status            []int  `json:"status"`
	LocationNotPrefix string `json:"location_not_prefix,omitempty"` // a redirect here is a failure, e.g. "/login"
	LocationPrefix    string `json:"location_prefix,omitempty"`     // if set, only a redirect here is a success, e.g. "/account"
	SetCookie         string `json:"set_cookie,omitempty"`          // if set, only an answer setting this cookie is a success
}

// CheckLoginRoutes validates routes and fills in defaults.
func CheckLoginRoutes(routes []LoginRoute) error {
	if len(routes) > 20 {
		return errors.New(`"login_routes" can list at most 20 routes`)
	}
	for i := range routes {
		r := &routes[i]
		if r.Method == "" {
			r.Method = http.MethodPost
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
		default:
			return fmt.Errorf("login_routes[%d].method must be POST, PUT or PATCH", i)
		}
		if !strings.HasPrefix(r.Path, "/") || r.Path == "/" || strings.HasPrefix(r.Path, ReservedPrefix) ||
			strings.ContainsAny(r.Path, "*?#[]\\") || checkPath(&url.URL{Path: r.Path}, nil, nil) != "" {
			return fmt.Errorf("login_routes[%d].path %q must be an exact canonical path outside %s", i, r.Path, ReservedPrefix)
		}
		if r.UsernameField == "" || len(r.UsernameField) > 64 {
			return fmt.Errorf("login_routes[%d].username_field is required (at most 64 characters)", i)
		}
		if len(r.Success.Status) == 0 {
			return fmt.Errorf("login_routes[%d].success.status is required", i)
		}
		for _, s := range r.Success.Status {
			if s < 100 || s > 599 {
				return fmt.Errorf("login_routes[%d].success.status %d is not an HTTP status", i, s)
			}
		}
		if p := r.Success.LocationNotPrefix; p != "" && !strings.HasPrefix(p, "/") {
			return fmt.Errorf("login_routes[%d].success.location_not_prefix must start with /", i)
		}
		if p := r.Success.LocationPrefix; p != "" && !strings.HasPrefix(p, "/") {
			return fmt.Errorf("login_routes[%d].success.location_prefix must start with /", i)
		}
		if r.Success.SetCookie == "" && r.Success.LocationPrefix == "" && slices.ContainsFunc(r.Success.Status, func(s int) bool { return s >= 300 && s < 400 }) {
			// A redirect alone cannot tell success from failure: the origin
			// may send both to a place the client chose ("next", Referer).
			return fmt.Errorf("login_routes[%d].success: with a 3xx status, set \"set_cookie\" or \"location_prefix\"", i)
		}
		if err := CheckLoginPrice(r.Price); err != nil {
			return fmt.Errorf("login_routes[%d].price: %v", i, err)
		}
		if c := r.Success.SetCookie; c != "" && (len(c) > 128 || strings.ContainsAny(c, " \t;,=\"()<>@:/?[]{}\\") || strings.ContainsFunc(c, func(r rune) bool { return r < 0x21 || r > 0x7e })) {
			return fmt.Errorf("login_routes[%d].success.set_cookie must be a cookie name", i)
		}
	}
	return nil
}

// loginRoute returns the route for a method and path, or nil. Paths are
// compared in the canonical form used for protect (matchKey), always
// without letter case: a trailing slash, ";params", trailing dots and
// spaces and case do not matter, because some origins serve "/Login;x."
// as "/login". The method is compared without case, because some
// frameworks upper-case it ("post" is POST to them).
func (g *Gate) loginRoute(method, p string) *LoginRoute {
	method = strings.ToUpper(method)
	key := matchKey(p, false)
	for i := range g.opt.LoginRoutes {
		r := &g.opt.LoginRoutes[i]
		if r.Method == method && matchKey(r.Path, false) == key {
			return r
		}
	}
	return nil
}

// nearLoginRoutes returns the routes p is, or that an origin could serve
// p as: with a format suffix ("/login.json", "/login.do") or below it
// ("/login/x").
func (g *Gate) nearLoginRoutes(p string) []*LoginRoute {
	key := matchKey(p, false)
	var near []*LoginRoute
	for i := range g.opt.LoginRoutes {
		rk := matchKey(g.opt.LoginRoutes[i].Path, false)
		if key == rk || strings.HasPrefix(key, rk+".") || strings.HasPrefix(key, rk+"/") {
			near = append(near, &g.opt.LoginRoutes[i])
		}
	}
	return near
}

// methodOverrideHeaders let some frameworks treat a request as another
// method than the one it was sent with.
var methodOverrideHeaders = []string{"X-HTTP-Method-Override", "X-HTTP-Method", "X-Method-Override", "X-Original-Method", "X-Forwarded-Method"}

// loginPathPassThrough reports whether a request on or near a login path
// (see nearLoginRoutes) that is not itself a route may reach the origin
// unchecked: only a GET, HEAD or OPTIONS that cannot carry a login, i.e.
// no body, no method override, and no username field in the query (PHP's
// $_REQUEST, Flask's request.values read logins from it). Anything else
// is refused (fail-closed).
func loginPathPassThrough(r *http.Request, near []*LoginRoute) bool {
	switch strings.ToUpper(r.Method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		return false
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		return false
	}
	for _, h := range methodOverrideHeaders {
		if len(r.Header.Values(h)) > 0 {
			return false
		}
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false
	}
	for k := range q {
		for _, route := range near {
			if loginFieldKey(k) == loginFieldKey(route.UsernameField) {
				return false
			}
		}
	}
	return true
}

func loginResource(r *LoginRoute) string { return hashResource(r.Method + " " + r.Path) }

// loginID is a keyed hash (hex, 16 bytes): what the counters and tokens
// know about an account or a network, instead of the value itself.
func (g *Gate) loginID(kind, v string) string {
	m := hmac.New(sha256.New, g.opt.SecretKey)
	m.Write([]byte("spikes402 login " + kind + "\x00" + v))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

func (g *Gate) loginAccount(username string) string {
	return g.loginID("account", loginAccountName(username))
}

// loginLetterFold spells out letters that do not decompose but that
// accent-insensitive collations and transliterations read as Latin ones.
// Keys are foldRune forms, so either case matches.
var loginLetterFold = func() map[rune]string {
	m := map[rune]string{}
	for k, v := range map[rune]string{
		'ß': "ss", 'æ': "ae", 'œ': "oe", 'ø': "o", 'ł': "l", 'đ': "d", 'ð': "d",
		'þ': "th", 'ħ': "h", 'ŧ': "t", 'ŀ': "l", 'ĸ': "k", 'ŋ': "n",
	} {
		m[foldRune(k)] = v
	}
	return m
}()

// loginAccountName is the spelling of a username the counters use: every
// spelling an origin or its database may treat as the same account gives
// the same name, so an attacker cannot split one account's failures over
// several spellings. It decomposes (NFKD: full-width letters, ligatures,
// "ﬁ"), drops combining marks (accents, so "Élise" is "elise", as in
// accent-insensitive collations like MySQL's utf8mb4_0900_ai_ci) and
// invisible format characters (zero-width spaces, soft hyphens), trims
// spaces and folds letter case (foldRune), spelling out letters such as
// "ß", "æ", "ø" and "ł" (loginLetterFold). Merging
// too much only makes two spellings share counters. It does not strip
// "+tag" from email addresses or dots in Gmail names: that is up to each
// site.
func loginAccountName(username string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(norm.NFKD.String(username)) {
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector):
		case loginLetterFold[foldRune(r)] != "":
			for _, x := range loginLetterFold[foldRune(r)] {
				b.WriteRune(foldRune(x))
			}
		default:
			b.WriteRune(foldRune(r))
		}
	}
	return b.String()
}

// loginNetwork is the client's /24 (IPv4) or /48 (IPv6): wide enough to
// survive a phone changing address, and to catch an attacker rotating
// addresses inside one allocation.
func (g *Gate) loginNetwork(r *http.Request) string {
	return g.loginID("network", g.loginPrefix(r, 24, 48))
}

// loginPrefix is the client's IPv4 /v4 or IPv6 /v6 prefix, or one shared
// bucket when the client has no IP address.
func (g *Gate) loginPrefix(r *http.Request, v4, v6 int) string {
	ip := g.clientIP(r)
	switch {
	case ip == nil:
		return loginNoIPKey
	case ip.To4() != nil:
		return ip.To4().Mask(net.CIDRMask(v4, 32)).String() + "/" + strconv.Itoa(v4)
	default:
		return ip.Mask(net.CIDRMask(v6, 128)).String() + "/" + strconv.Itoa(v6)
	}
}

type loginChallenge struct {
	Error           string    `json:"error,omitempty"`
	Problem         string    `json:"problem,omitempty"`
	Challenge       string    `json:"challenge"`
	Algorithm       string    `json:"algorithm"`
	Difficulty      int       `json:"difficulty"`
	MemoryMiB       int       `json:"memory_mib"`
	PreAlgorithm    string    `json:"precheck_algorithm"`
	PreDifficulty   int       `json:"precheck_difficulty"`
	Level           int       `json:"level"`
	ExpiresAt       time.Time `json:"expires_at"`
	ExpectedSeconds float64   `json:"expected_seconds_one_cpu_core"`
	Usage           string    `json:"usage"`
	PriceSats       int64     `json:"price_sats,omitempty"`  // paying instead costs this now
	PayInstead      string    `json:"pay_instead,omitempty"` // where to ask for an invoice
}

func loginPoW(level int) pow.Params {
	return pow.Params{Algorithm: pow.Argon2id, Difficulty: level - loginLevelToArgon, MemoryMiB: LoginArgonMemoryMiB}
}

// loginTTL is how long a challenge of this level stays valid:
// loginTTLFactor times the average solve time of a 4-worker browser,
// within [loginChallengeTTL, loginMaxTTL]. A slower phone (2 workers,
// slower cores) still gets about twice its average. A long life opens no
// stockpile: a proof is bound to its account, network and level and is
// refused as soon as the level is higher, which the first attempt makes it.
func loginTTL(level int) time.Duration {
	if level <= loginLevelToArgon {
		return loginChallengeTTL
	}
	ms := float64(uint64(1)<<min(level-loginLevelToArgon, 40)) * loginBrowserMs / 4 * loginTTLFactor
	return min(max(time.Duration(ms)*time.Millisecond, loginChallengeTTL), loginMaxTTL)
}

// newLoginChallenge signs a token for one attempt by acct from net.
func (g *Gate) newLoginChallenge(route *LoginRoute, acct, net string) (loginChallenge, error) {
	now := g.now()
	level := g.login.Level(acct, net, now)
	p := g.loginParams(level)
	pre := g.loginPre
	if now.Unix() < g.loginBusyUntil.Load() {
		pre += loginPreBoost
	}
	id := make([]byte, 16)
	rand.Read(id) // never fails (crypto/rand)
	exp := now.Add(loginTTL(level))
	token, err := g.sign.sign(claims{
		ID: hex.EncodeToString(id), Resource: loginResource(route), Expires: exp.Unix(),
		PowBits: p.Difficulty, PowAlg: p.Algorithm, PowMem: p.MemoryMiB,
		Kind: "login", Acct: acct, Net: net, Level: level, Pre: pre,
	})
	if err != nil {
		return loginChallenge{}, err
	}
	ch := loginChallenge{
		Challenge: token, Algorithm: p.Algorithm, Difficulty: p.Difficulty, MemoryMiB: p.MemoryMiB,
		PreAlgorithm: pow.SHA256d, PreDifficulty: pre,
		Level: level, ExpiresAt: exp.UTC(), ExpectedSeconds: float64(int(p.ExpectedSeconds()*10)) / 10,
		Usage: `find nonce so that Argon2id(password = challenge + ":" + nonce, salt = "spikes402-pow-v1", time = 1, memory = memory_mib MiB, threads = 1, 32 bytes) has "difficulty" leading zero bits, and pre so that SHA256(SHA256(challenge + ":" + pre)) has "precheck_difficulty" leading zero bits, then repeat the login with "Authorization: PoW <challenge>:<nonce>:<pre>"`,
	}
	if ch.PriceSats = g.loginPrice(route, level); ch.PriceSats > 0 {
		ch.PayInstead = LoginInvoicePath
	}
	return ch, nil
}

var (
	errLoginBinding = errors.New("proof is for another account, network or login route")
	errLoginLevel   = errors.New("proof is below the current level; get a new challenge")
	errBadPre       = errors.New("missing or wrong sha256d pre-check")
)

// checkLoginClaims verifies a login token or pass: its kind, expiry and
// binding. It does not spend it.
func (g *Gate) checkLoginClaims(value, kind, resHash, acct, net string) (claims, error) {
	c, err := g.sign.verify(value)
	if err != nil || c.Kind != kind {
		return claims{}, errBadToken
	}
	if g.now().Unix() >= c.Expires {
		return claims{}, errExpired
	}
	if c.Resource != resHash || c.Net != net || (acct != "" && c.Acct != acct) {
		return claims{}, errLoginBinding
	}
	return c, nil
}

// checkLoginWork verifies "<token>:<nonce>:<pre>" for a login challenge
// and spends the token, right or wrong: one Argon2id check per token,
// and after a wrong nonce one retry at most (see spentSet.beginLogin).
// Before that check come the cheap ones: the signature, binding,
// level (a proof below the current level is refused at once), spent set
// and the sha256d pre-check, then the rate limits. The Argon2id
// check needs one of its network's few slots and room in the login
// memory budget; otherwise the answer is 503, never a pass. A right
// pre-check with a wrong nonce counts as junk from the network (worth at
// most +1 level, see LoginState.junkProof).
func (g *Gate) checkLoginWork(r *http.Request, cred, resHash, acct, net string) (claims, error) {
	token, rest, ok := strings.Cut(strings.TrimSpace(cred), ":")
	nonceStr, preStr, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 {
		return claims{}, errBadToken
	}
	c, err := g.checkLoginClaims(token, "login", resHash, acct, net)
	if err != nil {
		return claims{}, err
	}
	now := g.now()
	// A proof below the level required now is refused before any costly
	// step: no slot, no Argon2id check, nothing spent.
	if c.Level < g.login.Level(c.Acct, net, now) {
		return claims{}, errLoginLevel
	}
	nonce, err := strconv.ParseUint(nonceStr, 10, 64)
	pre, err2 := strconv.ParseUint(preStr, 10, 64)
	p := c.powParams()
	if err != nil || err2 != nil || p != g.loginParams(c.Level) || p.Validate() != nil || c.Pre < g.loginPre {
		return claims{}, errBadProof
	}
	key := loginSpentKey(c.ID)
	if g.spent.has(key) {
		return claims{}, errReplay
	}
	if !(pow.Params{Algorithm: pow.SHA256d, Difficulty: c.Pre}).Check(token, pre) {
		return claims{}, errBadPre
	}
	if ok, wait := g.loginAllow(r, now); !ok {
		return claims{}, &busyError{http.StatusTooManyRequests, wait, "too many requests"}
	}
	slot := g.loginPrefix(r, 24, 48)
	if !g.loginSlots.acquire(slot) {
		return claims{}, &busyError{http.StatusServiceUnavailable, time.Second, "busy checking proofs of work from your network, retry shortly"}
	}
	defer g.loginSlots.release(slot)
	if !g.loginArgon.acquire(p.MemoryMiB, argonAcquireWait) {
		g.loginBusyUntil.Store(now.Unix() + loginBusyFor)
		return claims{}, &busyError{http.StatusServiceUnavailable, time.Second, "busy checking proofs of work, retry shortly"}
	}
	defer g.loginArgon.release(p.MemoryMiB)
	// Spent before the check, so one token never buys two checks at once,
	// and at most two ever: after a wrong nonce, one retry, no sooner than
	// loginRetryAfter (see spentSet.beginLogin). Only the holder of the
	// token, from its network, gets this far, so nobody else can use up
	// an honest user's retry.
	if err := g.spent.beginLogin(key, time.Unix(c.Expires, 0), now, loginRetryAfter); err != nil {
		return claims{}, err
	}
	if !p.Check(token, nonce) {
		g.spent.wrongLogin(key, now)
		g.login.junkProof(net, now)
		return claims{}, errBadProof
	}
	g.spent.rightLogin(key, time.Unix(c.Expires, 0), now)
	return c, nil
}

// loginSpentKey is a login token's entry in the spent set. Login entries
// have their own quota there (maxLoginSpent), so logins can never fill
// the room payers need.
func loginSpentKey(id string) string { return "login:" + id }

// loginAllow counts one login challenge or check against the client's
// limit, its /24 or /48's and its /16 or /40's.
func (g *Gate) loginAllow(r *http.Request, now time.Time) (bool, time.Duration) {
	client := loginNoIPKey
	if g.clientIP(r) != nil {
		client = g.client(r)
	}
	coarse := g.clientCoarse(r)
	if ok, wait := g.loginLim.allow(client, coarse, now); !ok {
		return false, wait
	}
	if ok, wait := g.loginPrefixLim.allow(g.loginPrefix(r, 24, 48), coarse, now); !ok {
		return false, wait
	}
	return g.loginWideLim.allow(g.loginPrefix(r, 16, 40), coarse, now)
}

// prefixSlots counts Argon2id checks in progress per network.
type prefixSlots struct {
	mu  sync.Mutex
	n   map[string]int
	max int
}

func newPrefixSlots(max int) *prefixSlots { return &prefixSlots{n: map[string]int{}, max: max} }

func (s *prefixSlots) acquire(k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n[k] >= s.max {
		return false
	}
	s.n[k]++
	return true
}

func (s *prefixSlots) release(k string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n[k]--; s.n[k] <= 0 {
		delete(s.n, k)
	}
}

// serveLoginChallenge answers POST /.spikes402/login-challenge
// {"method", "path", "username"} with a challenge for that account.
func (g *Gate) serveLoginChallenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", protectedCacheControl)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct{ Method, Path, Username string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Method == "" {
		req.Method = http.MethodPost
	}
	route := g.loginRoute(strings.ToUpper(req.Method), req.Path)
	if route == nil || req.Username == "" || len(req.Username) > loginMaxUsername {
		http.Error(w, "not a login route, or no username", http.StatusBadRequest)
		return
	}
	g.writeLoginChallenge(w, r, route, g.loginAccount(req.Username), http.StatusOK, "")
}

func (g *Gate) writeLoginChallenge(w http.ResponseWriter, r *http.Request, route *LoginRoute, acct string, status int, problem string) {
	if ok, wait := g.loginAllow(r, g.now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	g.sendLoginChallenge(w, r, route, acct, status, problem)
}

// sendLoginChallenge is writeLoginChallenge for a request already counted
// against the login rate limits.
func (g *Gate) sendLoginChallenge(w http.ResponseWriter, r *http.Request, route *LoginRoute, acct string, status int, problem string) {
	ch, err := g.newLoginChallenge(route, acct, g.loginNetwork(r))
	if err != nil {
		http.Error(w, "internal error", http.StatusServiceUnavailable)
		return
	}
	if status == http.StatusServiceUnavailable {
		ch.Error, ch.PriceSats, ch.PayInstead = problem, 0, ""
	}
	if status == http.StatusPaymentRequired {
		ch.Error, ch.Problem = "login proof-of-work required", problem
		w.Header().Add("WWW-Authenticate", fmt.Sprintf(`PoW challenge=%q, difficulty="%d", algorithm="argon2id", memory-mib="%d", precheck-difficulty="%d"`, ch.Challenge, ch.Difficulty, ch.MemoryMiB, ch.PreDifficulty))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", protectedCacheControl)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ch)
}

// serveLoginRedeem answers POST /.spikes402/login-redeem with
// "Authorization: PoW <token>:<nonce>:<pre>" or, for a paid login
// invoice, "L402 <token>[:<preimage>]": a valid proof or payment becomes
// a one-use login pass, a cookie the browser sends with the login form.
func (g *Gate) serveLoginRedeem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", protectedCacheControl)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scheme, cred, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	paid := strings.EqualFold(scheme, "L402")
	if !paid && !strings.EqualFold(scheme, "PoW") {
		http.Error(w, errScheme.Error(), http.StatusBadRequest)
		return
	}
	if paid {
		// Polled every few seconds while the payer pays: the claim limit.
		if ok, wait := g.claimLim.allow(g.client(r), g.clientCoarse(r), g.now()); !ok {
			w.Header().Set("Retry-After", itoaWait(wait))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
	}
	// The token names its route; it must still be a configured one.
	tok, _, _ := strings.Cut(strings.TrimSpace(cred), ":")
	c0, err := g.sign.verify(tok)
	var route *LoginRoute
	for i := range g.opt.LoginRoutes {
		if err == nil && loginResource(&g.opt.LoginRoutes[i]) == c0.Resource {
			route = &g.opt.LoginRoutes[i]
		}
	}
	if route == nil {
		http.Error(w, errBadToken.Error(), http.StatusForbidden)
		return
	}
	net := g.loginNetwork(r)
	var c claims
	if paid {
		c, err = g.checkLoginPay(cred, c0.Resource, net, g.now())
		if errors.Is(err, errUnpaid) {
			w.Header().Set("Retry-After", "2")
			g.writeClaim(w, r, http.StatusAccepted, claimBody{Status: "unpaid"})
			return
		}
		if errors.Is(err, errLoginLevel) {
			g.writeStalePayment(w, route, c.Acct, net)
			return
		}
		if err == nil {
			g.observe(r, g.classify(r), OutcomePaid, c.PriceSats)
		}
	} else {
		c, err = g.checkLoginWork(r, cred, c0.Resource, "", net)
	}
	if err != nil {
		if errors.Is(err, errBadPre) {
			g.loginAllow(r, g.now()) // cheap to send, but still counted
		}
		writeLoginError(w, err, http.StatusForbidden)
		return
	}
	id := make([]byte, 16)
	rand.Read(id)
	pass, err := g.sign.sign(claims{ID: hex.EncodeToString(id), Resource: c.Resource, Expires: g.now().Add(loginPassTTL).Unix(),
		Kind: "loginpass", Acct: c.Acct, Net: c.Net, Level: c.Level})
	if err != nil {
		http.Error(w, "internal error", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: loginPassCookie, Value: pass, Path: "/", MaxAge: int(loginPassTTL / time.Second),
		HttpOnly: true, Secure: g.scheme(r) == "https", SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func writeLoginError(w http.ResponseWriter, err error, status int) {
	var busy *busyError
	if errors.As(err, &busy) {
		w.Header().Set("Retry-After", strconv.Itoa(int(busy.wait.Seconds())+1))
		http.Error(w, busy.msg, busy.status)
		return
	}
	if errors.Is(err, errBusy) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, err.Error(), status)
}

// serveLogin guards one request to a login route. Every failure to check
// ends here with an error: the origin only sees attempts that carry a
// valid, unspent proof at the current level.
func (g *Gate) serveLogin(w http.ResponseWriter, r *http.Request, route *LoginRoute, passes map[string]string) {
	w = noStore(w)

	if r.ContentLength > loginMaxBody {
		http.Error(w, "login request too large", http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, loginMaxBody+1))
	if err != nil || len(body) > loginMaxBody {
		http.Error(w, "login request too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	username, err := loginUsername(r, body, route.UsernameField)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	acct, net, resHash := g.loginAccount(username), g.loginNetwork(r), loginResource(route)

	var c claims
	scheme, cred, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	switch {
	case strings.EqualFold(scheme, "PoW"):
		c, err = g.checkLoginWork(r, cred, resHash, acct, net)
		r.Header.Del("Authorization") // ours, not the origin's
	case passes[loginPassCookie] != "":
		c, err = g.checkLoginClaims(passes[loginPassCookie], "loginpass", resHash, acct, net)
		if err == nil {
			err = g.spent.use(loginSpentKey(c.ID), time.Unix(c.Expires, 0), g.now())
		}
	default:
		g.writeLoginChallenge(w, r, route, acct, http.StatusPaymentRequired, "")
		return
	}
	if err == nil {
		if _, ok := g.login.admit(acct, net, c.Level, g.now()); !ok {
			err = errLoginLevel
		}
	}
	if err != nil {
		var busy *busyError
		if errors.As(err, &busy) || errors.Is(err, errBusy) {
			writeLoginError(w, err, 0)
			return
		}
		g.writeLoginChallenge(w, r, route, acct, http.StatusPaymentRequired, err.Error())
		return
	}

	// The origin gets exactly the body that was checked.
	r.ContentLength, r.TransferEncoding, r.Trailer = int64(len(body)), nil, nil
	referer, steer := r.Header.Values("Referer"), loginSteerValues(r, body)
	ow := &outcomeWriter{ResponseWriter: w, hosts: g.loginHosts}
	g.opt.Next.ServeHTTP(ow, r)
	if route.succeeded(ow, referer, steer, g.now()) {
		g.login.succeeded(acct, net, g.now())
	} else {
		g.login.failed(g.now())
	}
}

// succeeded reports whether the origin's answer is a success (see
// LoginSuccess). An answer the origin never wrote is a failure. Unless
// set_cookie is set, a redirect to the Referer is a failure: origins send
// failed logins "back" there (Laravel's back()), and the client chooses
// it. With location_prefix, a redirect is a failure too unless the
// request carried an absolute Referer (loginHasReferer); query and body
// values are then skipped, so honest logins that follow "next" succeed.
// Without location_prefix (allowed only when no 3xx status counts, see
// CheckLoginRoutes), a redirect to any query or body value the request
// named (a "next" parameter) is a failure as well.
func (route *LoginRoute) succeeded(o *outcomeWriter, referer, steer []string, now time.Time) bool {
	s := route.Success
	if o.status == 0 || !slices.Contains(s.Status, o.status) {
		return false
	}
	loc, crossHost := "", false
	if o.location != "" {
		k, ok := route.redirectKey(o.location)
		u, err := url.Parse(strings.TrimSpace(o.location))
		if !ok || err != nil {
			return false
		}
		loc = k
		// A redirect to another host (or "//host/...") is never under
		// location_prefix, whatever its path. Only o.hosts (public_hosts
		// and the origin's own host) are this site: never the request's
		// Host or X-Forwarded-Host, which the client chooses and an
		// origin may echo into Location.
		crossHost = u.Host != "" && (u.Hostname() == "" ||
			!slices.ContainsFunc(o.hosts, func(h string) bool { return strings.EqualFold(u.Hostname(), hostOnly(h)) }))
	}
	if s.LocationPrefix != "" && crossHost {
		return false
	}
	if s.LocationNotPrefix != "" && loc != "" && strings.HasPrefix(loc, matchKey(s.LocationNotPrefix, false)) {
		return false
	}
	if s.LocationPrefix != "" && (loc == "" || !strings.HasPrefix(loc, matchKey(s.LocationPrefix, false))) {
		return false
	}
	if s.SetCookie != "" {
		for _, c := range (&http.Response{Header: http.Header{"Set-Cookie": o.cookies}}).Cookies() {
			if c.Name == s.SetCookie && c.Value != "" && c.MaxAge >= 0 && (c.Expires.IsZero() || c.Expires.After(now)) {
				return true
			}
		}
		return false
	}
	if loc == "" {
		return true
	}
	if crossHost {
		return false
	}
	if s.LocationPrefix != "" {
		// Without the Referer the origin may send a failure "back" to a
		// page it remembers (a session's previous URL), which the client
		// can put under the prefix: such a redirect proves nothing.
		if !loginHasReferer(referer) {
			return false
		}
		steer = nil
	}
	for _, v := range slices.Concat(referer, steer) {
		if k, ok := route.redirectKey(v); ok && k == loc {
			return false
		}
	}
	return true
}

// loginHasReferer reports whether the request carried exactly one
// Referer, an absolute http(s) URL with a host. An empty header counts as
// missing; several headers count as missing too, since the origin may
// read another one than the proxy.
func loginHasReferer(referer []string) bool {
	if len(referer) != 1 {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(referer[0]))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// hostOnly is a Host header without its port.
func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.Trim(h, "[]")
}

// redirectKey is the canonical path (matchKey, no case) a Location or a
// client value points at, resolved against the route.
func (route *LoginRoute) redirectKey(v string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil {
		return "", false
	}
	return matchKey((&url.URL{Path: route.Path}).ResolveReference(u).Path, false), true
}

// loginSteerValues are the values a client chose in a login request that
// an origin may redirect to besides the Referer: every query and body
// string value (nested JSON included).
func loginSteerValues(r *http.Request, body []byte) []string {
	var v []string
	q, _ := url.ParseQuery(r.URL.RawQuery)
	for _, vs := range q {
		v = append(v, vs...)
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch mt {
	case "application/x-www-form-urlencoded":
		f, _ := url.ParseQuery(string(body))
		for _, vs := range f {
			v = append(v, vs...)
		}
	case "application/json":
		var doc any
		if json.Unmarshal(body, &doc) == nil {
			var walk func(any)
			walk = func(x any) {
				switch x := x.(type) {
				case string:
					v = append(v, x)
				case []any:
					for _, e := range x {
						walk(e)
					}
				case map[string]any:
					for _, e := range x {
						walk(e)
					}
				}
			}
			walk(doc)
		}
	}
	return v
}

// loginUsername reads the username from an urlencoded or JSON body. It
// refuses anything the origin could read as another username than the
// one counted here: the field given twice (also under a spelling PHP or a
// case-insensitive JSON decoder would merge, like "User", "user.name" for
// "user_name" or "user[]"), the field in the query string, an encoded
// body, a charset other than UTF-8, or several Content-Type headers.
func loginUsername(r *http.Request, body []byte, field string) (string, error) {
	for _, ce := range r.Header.Values("Content-Encoding") {
		if !strings.EqualFold(strings.TrimSpace(ce), "identity") {
			return "", errors.New("login request with an encoded body")
		}
	}
	cts := r.Header.Values("Content-Type")
	if len(cts) != 1 {
		return "", errors.New("login content type must be application/x-www-form-urlencoded or application/json")
	}
	mt, params, err := mime.ParseMediaType(cts[0])
	if err != nil {
		mt = ""
	}
	if cs, ok := params["charset"]; ok && !strings.EqualFold(cs, "utf-8") {
		return "", errors.New("login request must be UTF-8")
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", errors.New("unreadable login query string")
	}
	// The origin must see the method that was checked: no override
	// header, and no "_method" field (Symfony and Laravel read it from the
	// body or the query).
	errOverride := errors.New("method override not allowed on a login route")
	for _, h := range methodOverrideHeaders {
		if len(r.Header.Values(h)) > 0 {
			return "", errOverride
		}
	}
	want, override := loginFieldKey(field), loginFieldKey("_method")
	for k := range q {
		switch loginFieldKey(k) {
		case want:
			return "", errors.New("login username in the query string")
		case override:
			return "", errOverride
		}
	}
	var u string
	n := 0
	switch mt {
	case "application/x-www-form-urlencoded":
		v, err := url.ParseQuery(string(body))
		if err != nil {
			return "", errors.New("unreadable login form")
		}
		for k, vs := range v {
			switch loginFieldKey(k) {
			case want:
				n += len(vs)
			case override:
				return "", errOverride
			}
		}
		u = v.Get(field)
	case "application/json":
		if u, n, err = loginJSONUsername(body, field); err != nil {
			return "", errors.New("unreadable login JSON")
		}
		if _, m, err := loginJSONUsername(body, "_method"); m > 0 || err != nil {
			return "", errOverride
		}
	default:
		return "", errors.New("login content type must be application/x-www-form-urlencoded or application/json")
	}
	if n != 1 {
		return "", errors.New("login request must name exactly one username")
	}
	if strings.TrimSpace(u) == "" || len(u) > loginMaxUsername {
		return "", errors.New("login request without a username")
	}
	return u, nil
}

// loginFieldKey is the form of a field name under which origins may merge
// two spellings: NFKC (full-width "ｕｓｅｒ"), invisible format characters
// dropped, Unicode case folding (JSON decoders matching struct fields fold
// "ſ" to "s" and the Kelvin sign to "k"), PHP's renaming of " ", "." and
// "[" to "_", and PHP arrays ("user[]"). Used for the username field and
// "_method" alike.
func loginFieldKey(k string) string {
	k = strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector) {
			return -1
		}
		return r
	}, norm.NFKC.String(k))
	k = strings.TrimLeft(k, " ")
	if i := strings.IndexByte(k, '['); i >= 0 && strings.IndexByte(k[i:], ']') > 0 {
		k = k[:i]
	}
	k = strings.NewReplacer(" ", "_", ".", "_", "[", "_").Replace(k)
	return strings.Map(foldRune, k)
}

// foldRune maps r to the smallest rune of the Unicode case-folding orbits
// of r, its lower case and its upper case, so every spelling a
// case-insensitive decoder may treat as one letter ("s", "S", "ſ"; "k",
// "K", the Kelvin sign; "i", "I", the dotless ı and the dotted İ) gives
// the same rune.
func foldRune(r rune) rune {
	m := r
	for _, s := range [...]rune{r, unicode.ToLower(r), unicode.ToUpper(r)} {
		m = min(m, s)
		for f := unicode.SimpleFold(s); f != s; f = unicode.SimpleFold(f) {
			m = min(m, f)
		}
	}
	return m
}

// loginJSONUsername reads the top-level object of body and returns the
// string under field and how many keys could mean field (see
// loginFieldKey; Go and other decoders take the last of duplicate keys).
func loginJSONUsername(body []byte, field string) (string, int, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return "", 0, errors.New("not a JSON object")
	}
	want := loginFieldKey(field)
	var u string
	n := 0
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return "", 0, err
		}
		k, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return "", 0, err
		}
		if loginFieldKey(k) != want {
			continue
		}
		n++
		if k == field && json.Unmarshal(raw, &u) != nil {
			return "", 0, errors.New("username is not a string")
		}
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return "", 0, errors.New("not a JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", 0, errors.New("data after the JSON object")
	}
	return u, n, nil
}

// outcomeWriter remembers the status, Location and Set-Cookie headers
// the origin answered (status 0: it wrote nothing).
type outcomeWriter struct {
	http.ResponseWriter
	hosts    []string // the names a Location may carry (see Gate.loginHosts)
	status   int
	location string
	cookies  []string
}

func (o *outcomeWriter) WriteHeader(code int) {
	if o.status == 0 && code >= 200 {
		o.status, o.location = code, o.Header().Get("Location")
		o.cookies = append([]string(nil), o.Header().Values("Set-Cookie")...)
	}
	o.ResponseWriter.WriteHeader(code)
}

func (o *outcomeWriter) Write(p []byte) (int, error) {
	if o.status == 0 {
		o.WriteHeader(http.StatusOK)
	}
	return o.ResponseWriter.Write(p)
}

func (o *outcomeWriter) Unwrap() http.ResponseWriter { return o.ResponseWriter }

var (
	//go:embed assets/login.js
	loginJSTemplate string
	//go:embed assets/argon2id-worker.js
	argon2idWorkerJS string

	// loginJS points at the worker by content hash, so a new release is
	// never served from an old cached copy.
	loginJS = strings.NewReplacer(
		"__WORKER__", Argon2idWorkerPath+"?v="+hex.EncodeToString(sha256Sum([]byte(argon2idWorkerJS))[:6]),
		"__POW_WORKER__", workerSrc,
	).Replace(loginJSTemplate)
)

func (g *Gate) serveLoginReserved(w http.ResponseWriter, r *http.Request) {
	if g.login == nil {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case LoginScriptPath:
		serveScript(w, r, loginJS)
	case Argon2idWorkerPath:
		serveScript(w, r, argon2idWorkerJS)
	case LoginChallengePath:
		g.serveLoginChallenge(w, r)
	case LoginRedeemPath:
		g.serveLoginRedeem(w, r)
	case LoginInvoicePath:
		g.serveLoginInvoice(w, r)
	}
}
