package gate

import (
	"net/http"
	"strings"
	"time"
)

// A browser cannot send an Authorization header when it navigates, so a
// page that paid with a WebLN wallet turns its proof into a pass: a cookie
// that unlocks exactly one resource for a short time. The page then
// navigates to the same URL for real, so the origin's HTML, CSS, scripts and
// files all load normally, and a reload within the lifetime does not pay again.
//
// How it works:
//   - The page sends the proof as usual, plus "Spikes402-Redeem: cookie".
//   - The gate checks and spends the token (once, like any redemption), does
//     not call the origin, and answers 204 with the pass cookie.
//   - A GET with a valid pass for that resource goes to the origin. The
//     cookie is removed from the request, like the session cookie.
//
// The pass is signed with the token key, marked Kind "pass" (a pass is never
// accepted as a token), bound to the method and URL like a token, HttpOnly,
// SameSite=Strict, and valid for PassTTL. Nothing is stored on the server.
const (
	// RedeemHeader asks for a pass instead of the page itself.
	RedeemHeader = "Spikes402-Redeem"
	redeemCookie = "cookie" // the value of RedeemHeader

	passPrefix = "spikes402_pass_"
	PassTTL    = 60 * time.Second
)

// passName is the cookie name for one resource (hash from hashResource), so
// that passes for different pages do not replace each other.
func passName(resHash string) string { return passPrefix + resHash[:8] }

// issuePass sets the pass cookie for the resource of r.
func (g *Gate) issuePass(w http.ResponseWriter, r *http.Request, id string) {
	res := hashResource(resource(r))
	pass, err := g.sign.sign(claims{ID: id, Resource: res, Expires: g.now().Add(PassTTL).Unix(), Kind: "pass"})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: passName(res), Value: pass, Path: "/", MaxAge: int(PassTTL / time.Second),
		HttpOnly: true, Secure: g.scheme(r) == "https", SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// validPass reports whether value is an unexpired pass for the resource.
func (g *Gate) validPass(value, resHash string) bool {
	if value == "" {
		return false
	}
	c, err := g.sign.verify(value)
	return err == nil && c.Kind == "pass" && c.Resource == resHash && g.now().Unix() < c.Expires
}

// takePassCookies removes our pass cookies from r (the origin never sees
// them) and returns their values by cookie name.
func takePassCookies(r *http.Request) map[string]string {
	var passes map[string]string
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, passPrefix) {
			if passes == nil {
				passes = map[string]string{}
			}
			passes[c.Name] = c.Value
		}
	}
	return passes
}
