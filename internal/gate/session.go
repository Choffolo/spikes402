package gate

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

// SessionCookie is the optional cookie Spikes402 sets on 402 answers. It
// only says "this is the same client as before", so that a client that
// asks for the same page again gets its open offer back instead of a new
// invoice, while another client behind the same IP address never does.
// It holds a random value, nothing else, and is never sent to the origin.
const SessionCookie = "spikes402_session"

const sessionMaxAge = 24 * 60 * 60 // seconds

// session returns the client's session cookie if it is well formed, or
// "". It removes the cookie from the request, so the origin does not see it.
func (g *Gate) session(r *http.Request) string {
	var id string
	var kept []string
	found := false
	for _, c := range r.Cookies() {
		if c.Name == SessionCookie {
			found = true
			if validSession(c.Value) {
				id = c.Value
			}
			continue
		}
		if strings.HasPrefix(c.Name, passPrefix) {
			found = true // dropped here; the gate reads it with takePassCookies first
			continue
		}
		kept = append(kept, c.String())
	}
	if found {
		r.Header.Del("Cookie")
		if len(kept) > 0 {
			r.Header.Set("Cookie", strings.Join(kept, "; "))
		}
	}
	return id
}

func validSession(v string) bool {
	if len(v) != 32 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil && strings.ToLower(v) == v
}

// newSession creates a session ID and sets it as a cookie on the answer.
func newSession(w http.ResponseWriter, secure bool) string {
	b := make([]byte, 16)
	rand.Read(b) // never fails (crypto/rand)
	id := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: id, Path: "/", MaxAge: sessionMaxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	return id
}

// memo is the invoice description: phoenixd allows 128 bytes.
func memo(resource string) string {
	m := "Spikes402: " + resource
	if len(m) > 120 {
		m = m[:117] + "..."
	}
	return strings.ToValidUTF8(m, "")
}
