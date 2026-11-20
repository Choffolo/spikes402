package webbotauth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
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
	"sync"
	"syscall"
	"time"
)

// Limits for key directory fetches (draft section 6.7 and appendix C).
const (
	maxDirectoryBytes   = 64 << 10                                      // response size
	maxDirectoryKeys    = 32                                            // keys per directory
	maxDirectories      = 1000                                          // cached directories
	maxConcurrentFetch  = 4                                             // directory fetches in flight
	fetchTimeout        = 5 * time.Second                               // wall clock per fetch
	defaultFreshness    = time.Hour                                     // when no max-age is given
	minFreshness        = 5 * time.Minute                               // clamp for max-age
	maxFreshness        = 24 * time.Hour                                // clamp for max-age
	negativeCacheTTL    = time.Minute                                   // after a failed fetch (draft: at most 5 min)
	maxStale            = 24 * time.Hour                                // keep using keys while refreshes fail
	fetchesPerMinute    = 30                                            // new fetches, all directories together
	fetchBurst          = 10                                            // ... with bursts of this many
	testKeyThumbprint   = "poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U" // RFC 9421 test key
	directoryUserAgent  = "Spikes402 (+https://github.com/Choffolo/spikes402)"
	directoryAcceptType = DirectoryMediaType + ", application/json;q=0.5"
)

var (
	// ErrUnavailable means the key directory could not be resolved (yet).
	ErrUnavailable = errors.New("key directory unavailable")
	// ErrNoKey means the resolved directory does not provide the key.
	ErrNoKey  = errors.New("key not in directory")
	errBadURL = errors.New("Signature-Agent is not an acceptable https URL")
)

type dirKey struct {
	pub      ed25519.PublicKey
	nbf, exp int64 // 0 = not set
}

type dirEntry struct {
	keys        map[string]dirKey // thumbprint -> key; nil if never resolved
	freshUntil  time.Time
	resolvedAt  time.Time
	failedUntil time.Time     // negative cache
	fetching    chan struct{} // closed when the current fetch ends
}

// Directories fetches and caches key directories. The zero value is not
// usable; call NewDirectories.
type Directories struct {
	client  *http.Client
	devMode bool
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]*dirEntry // by identifier
	tokens  float64
	last    time.Time
	sem     chan struct{}
}

// NewDirectories returns a directory cache. With devMode, plain http and
// private addresses (e.g. localhost) are allowed: for local demos only.
func NewDirectories(devMode bool) *Directories {
	d := &Directories{
		devMode: devMode,
		now:     time.Now,
		entries: make(map[string]*dirEntry),
		tokens:  fetchBurst,
		sem:     make(chan struct{}, maxConcurrentFetch),
	}
	dialer := &net.Dialer{Timeout: fetchTimeout, Control: d.checkAddress}
	d.client = &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			Proxy:                  nil, // never through an environment proxy
			DialContext:            dialer.DialContext,
			TLSHandshakeTimeout:    fetchTimeout,
			ResponseHeaderTimeout:  fetchTimeout,
			MaxResponseHeaderBytes: 16 << 10,
			DisableCompression:     true, // size limit applies to what we read
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // redirects MUST NOT be followed
		},
	}
	return d
}

// checkAddress refuses connections to non-public addresses (SSRF), at
// dial time so that DNS tricks cannot get around it.
func (d *Directories) checkAddress(_, address string, _ syscall.RawConn) error {
	if d.devMode {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !isPublic(ip) {
		return fmt.Errorf("refusing to fetch a key directory from %s", host)
	}
	return nil
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isPublic(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip))
}

// identifier validates a Signature-Agent URL and returns the identifier:
// the normalized URL without query or fragment (draft section 5.5).
func (d *Directories) identifier(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", errBadURL
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && !(d.devMode && scheme == "http") {
		return "", errBadURL
	}
	host := authority(u.Host, scheme)
	if !d.devMode && u.Port() != "" && u.Port() != "443" {
		return "", errBadURL // conservative: standard port only
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path, nil
}

// lookup returns the key with the given keyid published at rawURL, and
// the identifier. It waits at most wait for a fetch it had to start.
func (d *Directories) lookup(rawURL, keyid string, wait time.Duration) (ed25519.PublicKey, string, error) {
	id, err := d.identifier(rawURL)
	if err != nil {
		return nil, "", err
	}
	if keyid == testKeyThumbprint {
		return nil, id, errors.New("test keys are not accepted") // draft section 6.8
	}

	d.mu.Lock()
	now := d.now()
	e := d.entries[id]
	if e != nil && e.keys != nil && now.Before(e.freshUntil) {
		k, err := e.key(keyid, now)
		d.mu.Unlock()
		return k, id, err
	}
	done := d.startFetchLocked(id, rawURL, e, now)
	d.mu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-time.After(wait):
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	now = d.now()
	if e = d.entries[id]; e != nil && e.keys != nil && now.Before(e.freshUntil.Add(maxStale)) {
		k, err := e.key(keyid, now)
		return k, id, err
	}
	return nil, id, ErrUnavailable
}

func (e *dirEntry) key(keyid string, now time.Time) (ed25519.PublicKey, error) {
	k, ok := e.keys[keyid]
	if !ok || (k.nbf != 0 && now.Unix() < k.nbf) || (k.exp != 0 && now.Unix() >= k.exp) {
		return nil, ErrNoKey
	}
	return k.pub, nil
}

// startFetchLocked starts (or joins) a fetch of the directory, within the
// global limits, and returns a channel closed when it ends, or nil.
func (d *Directories) startFetchLocked(id, rawURL string, e *dirEntry, now time.Time) chan struct{} {
	if e != nil && e.fetching != nil {
		return e.fetching // coalesce concurrent fetches
	}
	if e != nil && now.Before(e.failedUntil) {
		return nil
	}
	d.tokens = min(fetchBurst, d.tokens+now.Sub(d.last).Minutes()*fetchesPerMinute)
	d.last = now
	if d.tokens < 1 {
		return nil
	}
	select {
	case d.sem <- struct{}{}:
	default:
		return nil
	}
	d.tokens--
	if e == nil {
		d.evictLocked(now)
		e = &dirEntry{}
		d.entries[id] = e
	}
	done := make(chan struct{})
	e.fetching = done
	go func() {
		defer func() { <-d.sem }()
		keys, freshness, err := d.fetch(rawURL, strings.HasSuffix(id, DirectoryPath))
		d.mu.Lock()
		defer d.mu.Unlock()
		t := d.now()
		if err != nil {
			e.failedUntil = t.Add(negativeCacheTTL) // keep any keys we had (draft 6.10)
		} else {
			e.keys, e.resolvedAt, e.freshUntil = keys, t, t.Add(freshness) // newer evidence wins
		}
		e.fetching = nil
		close(done)
	}()
	return done
}

// evictLocked makes room for one more entry.
func (d *Directories) evictLocked(now time.Time) {
	if len(d.entries) < maxDirectories {
		return
	}
	for id, e := range d.entries {
		if e.fetching == nil && (e.keys == nil || now.After(e.freshUntil.Add(maxStale))) {
			delete(d.entries, id)
		}
	}
	for id, e := range d.entries { // still full: drop any idle entry
		if len(d.entries) < maxDirectories {
			return
		}
		if e.fetching == nil {
			delete(d.entries, id)
		}
	}
}

// fetch downloads and parses one directory.
func (d *Directories) fetch(rawURL string, wellKnown bool) (map[string]dirKey, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", directoryAcceptType)
	req.Header.Set("User-Agent", directoryUserAgent)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("directory: HTTP %d", resp.StatusCode)
	}
	if wellKnown {
		mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil || mt != DirectoryMediaType {
			return nil, 0, errors.New("directory: wrong media type")
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDirectoryBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > maxDirectoryBytes {
		return nil, 0, errors.New("directory: too large")
	}
	keys, err := parseDirectory(body, wellKnown)
	if err != nil {
		return nil, 0, err
	}
	return keys, freshness(resp.Header.Get("Cache-Control")), nil
}

func freshness(cacheControl string) time.Duration {
	for _, part := range strings.Split(cacheControl, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(strings.ToLower(part)), "max-age="); ok {
			if s, err := strconv.Atoi(v); err == nil {
				return min(max(time.Duration(s)*time.Second, minFreshness), maxFreshness)
			}
		}
	}
	return defaultFreshness
}

type jwk struct {
	Kty    string   `json:"kty"`
	Crv    string   `json:"crv"`
	X      string   `json:"x"`
	Kid    string   `json:"kid"`
	Use    string   `json:"use"`
	KeyOps []string `json:"key_ops"`
	Nbf    int64    `json:"nbf"`
	Exp    int64    `json:"exp"`
}

// parseDirectory reads a JWK Set and keeps the usable Ed25519 keys,
// indexed by thumbprint. Malformed or unsupported entries are skipped.
func parseDirectory(body []byte, wellKnown bool) (map[string]dirKey, error) {
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil || set.Keys == nil {
		return nil, errors.New("directory: not a JWK Set")
	}
	keys := make(map[string]dirKey)
	for i, raw := range set.Keys {
		if i >= maxDirectoryKeys {
			break
		}
		var k jwk
		if json.Unmarshal(raw, &k) != nil || k.Kty != "OKP" || k.Crv != "Ed25519" {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			continue
		}
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if k.KeyOps != nil && !contains(k.KeyOps, "verify") {
			continue
		}
		tp := Thumbprint(x)
		if wellKnown && k.Kid != "" && k.Kid != tp { // kid MUST be the thumbprint there
			continue
		}
		if tp == testKeyThumbprint {
			continue
		}
		keys[tp] = dirKey{pub: ed25519.PublicKey(x), nbf: k.Nbf, exp: k.Exp}
	}
	return keys, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
