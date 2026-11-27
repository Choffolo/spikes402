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
	"strconv"
	"strings"
	"time"

	"github.com/Choffolo/spikes402/pow"
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

	loginChallengeTTL = 120 * time.Second
	loginPassTTL      = 60 * time.Second
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
}

// LoginSuccess says how the origin answers a successful login. Any other
// answer counts as a failure.
type LoginSuccess struct {
	Status            []int  `json:"status"`
	LocationNotPrefix string `json:"location_not_prefix,omitempty"` // a redirect here is a failure, e.g. "/login"
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
		if !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, ReservedPrefix) ||
			strings.ContainsAny(r.Path, "*?[]\\") || checkPath(&url.URL{Path: r.Path}, nil) != "" {
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
	}
	return nil
}

// loginRoute returns the route for a method and path, or nil. A trailing
// slash and, unless case-sensitive, letter case do not matter: the origin
// may treat "/Login/" as "/login".
func (g *Gate) loginRoute(method, p string) *LoginRoute {
	p = strings.TrimSuffix(p, "/")
	for i := range g.opt.LoginRoutes {
		r := &g.opt.LoginRoutes[i]
		if r.Method != method {
			continue
		}
		if q := strings.TrimSuffix(r.Path, "/"); q == p || (!g.opt.CaseSensitive && strings.EqualFold(q, p)) {
			return r
		}
	}
	return nil
}

func (g *Gate) isLoginPath(p string) bool {
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
		if g.loginRoute(m, p) != nil {
			return true
		}
	}
	return false
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
	return g.loginID("account", strings.ToLower(strings.TrimSpace(username)))
}

// loginNetwork is the client's /24 (IPv4) or /64 (IPv6): wide enough to
// survive a phone changing address, narrow enough to stay one network.
func (g *Gate) loginNetwork(r *http.Request) string {
	ip := g.clientIP(r)
	switch {
	case ip == nil:
		return g.loginID("network", r.RemoteAddr)
	case ip.To4() != nil:
		return g.loginID("network", ip.To4().Mask(net.CIDRMask(24, 32)).String()+"/24")
	default:
		return g.loginID("network", ip.Mask(net.CIDRMask(64, 128)).String()+"/64")
	}
}

type loginChallenge struct {
	Error           string    `json:"error,omitempty"`
	Problem         string    `json:"problem,omitempty"`
	Challenge       string    `json:"challenge"`
	Algorithm       string    `json:"algorithm"`
	Difficulty      int       `json:"difficulty"`
	MemoryMiB       int       `json:"memory_mib"`
	Level           int       `json:"level"`
	ExpiresAt       time.Time `json:"expires_at"`
	ExpectedSeconds float64   `json:"expected_seconds_one_cpu_core"`
	Usage           string    `json:"usage"`
}

func loginPoW(level int) pow.Params {
	return pow.Params{Algorithm: pow.Argon2id, Difficulty: level - loginLevelToArgon, MemoryMiB: LoginArgonMemoryMiB}
}

// newLoginChallenge signs a token for one attempt by acct from net.
func (g *Gate) newLoginChallenge(route *LoginRoute, acct, net string) (loginChallenge, error) {
	now := g.now()
	level := g.login.Level(acct, net, now)
	p := g.loginParams(level)
	id := make([]byte, 16)
	rand.Read(id) // never fails (crypto/rand)
	exp := now.Add(loginChallengeTTL)
	token, err := g.sign.sign(claims{
		ID: hex.EncodeToString(id), Resource: loginResource(route), Expires: exp.Unix(),
		PowBits: p.Difficulty, PowAlg: p.Algorithm, PowMem: p.MemoryMiB,
		Kind: "login", Acct: acct, Net: net, Level: level,
	})
	if err != nil {
		return loginChallenge{}, err
	}
	return loginChallenge{
		Challenge: token, Algorithm: p.Algorithm, Difficulty: p.Difficulty, MemoryMiB: p.MemoryMiB,
		Level: level, ExpiresAt: exp.UTC(), ExpectedSeconds: float64(int(p.ExpectedSeconds()*10)) / 10,
		Usage: `find nonce so that Argon2id(password = challenge + ":" + nonce, salt = "spikes402-pow-v1", time = 1, memory = memory_mib MiB, threads = 1, 32 bytes) has "difficulty" leading zero bits, then repeat the login with "Authorization: PoW <challenge>:<nonce>"`,
	}, nil
}

var (
	errLoginBinding = errors.New("proof is for another account, network or login route")
	errLoginLevel   = errors.New("proof is below the current level; get a new challenge")
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

// checkLoginWork verifies "<token>:<nonce>" for a login challenge and
// spends the token, right or wrong: one check per token, ever. The
// Argon2id check is rate limited per client and bounded by the memory
// budget; when the budget is full the answer is 503, never a pass.
func (g *Gate) checkLoginWork(r *http.Request, cred, resHash, acct, net string) (claims, error) {
	token, nonceStr, ok := strings.Cut(strings.TrimSpace(cred), ":")
	if !ok {
		return claims{}, errBadToken
	}
	c, err := g.checkLoginClaims(token, "login", resHash, acct, net)
	if err != nil {
		return claims{}, err
	}
	nonce, err := strconv.ParseUint(nonceStr, 10, 64)
	p := c.powParams()
	if err != nil || p != g.loginParams(c.Level) || p.Validate() != nil {
		return claims{}, errBadProof
	}
	now := g.now()
	if g.spent.has(spentKey("PoW", c.ID)) {
		return claims{}, errReplay
	}
	if ok, wait := g.loginLim.allow(g.client(r), now); !ok {
		return claims{}, &busyError{http.StatusTooManyRequests, wait, "too many requests"}
	}
	if !g.argon.acquire(p.MemoryMiB, argonAcquireWait) {
		return claims{}, &busyError{http.StatusServiceUnavailable, time.Second, "busy checking proofs of work, retry shortly"}
	}
	defer g.argon.release(p.MemoryMiB)
	if err := g.spent.use(spentKey("PoW", c.ID), time.Unix(c.Expires, 0), now); err != nil {
		return claims{}, err
	}
	if !p.Check(token, nonce) {
		return claims{}, errBadProof
	}
	return c, nil
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
	if ok, wait := g.loginLim.allow(g.client(r), g.now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	ch, err := g.newLoginChallenge(route, acct, g.loginNetwork(r))
	if err != nil {
		http.Error(w, "internal error", http.StatusServiceUnavailable)
		return
	}
	if status == http.StatusPaymentRequired {
		ch.Error, ch.Problem = "login proof-of-work required", problem
		w.Header().Add("WWW-Authenticate", fmt.Sprintf(`PoW challenge=%q, difficulty="%d", algorithm="argon2id", memory-mib="%d"`, ch.Challenge, ch.Difficulty, ch.MemoryMiB))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", protectedCacheControl)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ch)
}

// serveLoginRedeem answers POST /.spikes402/login-redeem with
// "Authorization: PoW <token>:<nonce>": a valid proof becomes a one-use
// login pass, a cookie the browser sends with the login form.
func (g *Gate) serveLoginRedeem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", protectedCacheControl)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scheme, cred, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "PoW") {
		http.Error(w, errScheme.Error(), http.StatusBadRequest)
		return
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
	c, err := g.checkLoginWork(r, cred, c0.Resource, "", net)
	if err == nil && c.Level < g.login.Level(c.Acct, net, g.now()) {
		err = errLoginLevel
	}
	if err != nil {
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
	username, err := loginUsername(r.Header.Get("Content-Type"), body, route.UsernameField)
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
			err = g.spent.use(spentKey("PoW", c.ID), time.Unix(c.Expires, 0), g.now())
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

	ow := &outcomeWriter{ResponseWriter: w}
	g.opt.Next.ServeHTTP(ow, r)
	if route.succeeded(ow.code(), ow.location) {
		g.login.succeeded(acct, net, g.now())
	} else {
		g.login.failed(g.now())
	}
}

func (route *LoginRoute) succeeded(status int, location string) bool {
	ok := false
	for _, s := range route.Success.Status {
		ok = ok || s == status
	}
	if !ok || route.Success.LocationNotPrefix == "" || location == "" {
		return ok
	}
	u, err := url.Parse(location)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(strings.ToLower(u.Path), strings.ToLower(route.Success.LocationNotPrefix))
}

// loginUsername reads the username from an urlencoded or JSON body.
func loginUsername(contentType string, body []byte, field string) (string, error) {
	mt, _, _ := mime.ParseMediaType(contentType)
	var u string
	switch mt {
	case "application/x-www-form-urlencoded":
		v, err := url.ParseQuery(string(body))
		if err != nil {
			return "", errors.New("unreadable login form")
		}
		u = v.Get(field)
	case "application/json":
		var m map[string]json.RawMessage
		if json.Unmarshal(body, &m) != nil || json.Unmarshal(m[field], &u) != nil {
			return "", errors.New("unreadable login JSON")
		}
	default:
		return "", errors.New("login content type must be application/x-www-form-urlencoded or application/json")
	}
	if strings.TrimSpace(u) == "" || len(u) > loginMaxUsername {
		return "", errors.New("login request without a username")
	}
	return u, nil
}

// outcomeWriter remembers the status and Location the origin answered.
type outcomeWriter struct {
	http.ResponseWriter
	status   int
	location string
}

func (o *outcomeWriter) WriteHeader(code int) {
	if o.status == 0 && code >= 200 {
		o.status, o.location = code, o.Header().Get("Location")
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

func (o *outcomeWriter) code() int {
	if o.status == 0 {
		return http.StatusOK
	}
	return o.status
}

var (
	//go:embed assets/login.js
	loginJSTemplate string
	//go:embed assets/argon2id-worker.js
	argon2idWorkerJS string

	// loginJS points at the worker by content hash, so a new release is
	// never served from an old cached copy.
	loginJS = strings.Replace(loginJSTemplate, "__WORKER__",
		Argon2idWorkerPath+"?v="+hex.EncodeToString(sha256Sum([]byte(argon2idWorkerJS))[:6]), 1)
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
	}
}
