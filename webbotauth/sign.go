package webbotauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Choffolo/spikes402/internal/sfv"
)

// Signer signs outgoing requests (draft section 5.2).
type Signer struct {
	Key      ed25519.PrivateKey
	AgentURL string        // where the key directory is published (https)
	Label    string        // signature label, default "sig1"
	Lifetime time.Duration // default 1 minute
	now      func() time.Time
}

// Sign adds Signature-Agent, Signature-Input and Signature headers. It
// covers @method, @authority, @path and the Signature-Agent member, with
// a fresh random nonce. scheme is "https" for real use.
func (s *Signer) Sign(r *http.Request) error {
	label := s.Label
	if label == "" {
		label = "sig1"
	}
	lifetime := s.Lifetime
	if lifetime == 0 {
		lifetime = time.Minute
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	pub := s.Key.Public().(ed25519.PublicKey)
	params := sfv.InnerList{
		Items: []sfv.Item{
			{Value: "@method"},
			{Value: "@authority"},
			{Value: "@path"},
			{Value: "signature-agent", Params: sfv.Params{{Key: "key", Value: label}}},
		},
		Params: sfv.Params{
			{Key: "created", Value: now.Unix()},
			{Key: "expires", Value: now.Add(lifetime).Unix()},
			{Key: "keyid", Value: Thumbprint(pub)},
			{Key: "alg", Value: "ed25519"},
			{Key: "nonce", Value: base64.StdEncoding.EncodeToString(nonce)},
			{Key: "tag", Value: Tag},
		},
	}
	scheme := "https"
	if r.URL.Scheme == "http" {
		scheme = "http"
	}
	return sign(r, s.Key, label, s.AgentURL, scheme, params)
}

func sign(r *http.Request, key ed25519.PrivateKey, label, agentURL, scheme string, params sfv.InnerList) error {
	agent, err := sfv.SerializeItem(sfv.Item{Value: agentURL})
	if err != nil {
		return err
	}
	r.Header.Set("Signature-Agent", label+"="+agent)
	if r.Host == "" {
		r.Host = r.URL.Host
	}
	ah, err := parseAgentHeader(r)
	if err != nil {
		return err
	}
	base, err := requestInfo{r: r, scheme: scheme, agent: ah}.signatureBase(params)
	if err != nil {
		return err
	}
	input, err := sfv.SerializeInnerList(params)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(key, []byte(base))
	r.Header.Set("Signature-Input", label+"="+input)
	r.Header.Set("Signature", label+"=:"+base64.StdEncoding.EncodeToString(sig)+":")
	return nil
}

// DirectoryJSON returns the key directory (JWK Set) publishing pub.
func DirectoryJSON(pub ed25519.PublicKey) []byte {
	b, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "OKP",
		"crv": "Ed25519",
		"kid": Thumbprint(pub),
		"x":   base64.RawURLEncoding.EncodeToString(pub),
		"use": "sig",
	}}})
	return b
}

// DirectoryHandler serves the key directory of key, with the
// possession proof recommended by the draft (appendix B): a response
// signature covering the request's @authority and the Content-Digest.
func DirectoryHandler(key ed25519.PrivateKey) http.Handler {
	pub := key.Public().(ed25519.PublicKey)
	body := DirectoryJSON(pub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		h := w.Header()
		h.Set("Content-Type", DirectoryMediaType)
		h.Set("Cache-Control", "max-age=3600")
		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		cd, input, sig := signDirectory(key, authority(r.Host, scheme), body, now.Unix(), now.Add(24*time.Hour).Unix())
		h.Set("Content-Digest", cd)
		h.Set("Signature-Input", "binding="+input)
		h.Set("Signature", "binding=:"+base64.StdEncoding.EncodeToString(sig)+":")
		w.Write(body)
	})
}

// signDirectory computes the directory response signature (appendix B).
func signDirectory(key ed25519.PrivateKey, authority string, body []byte, created, expires int64) (digest, input string, sig []byte) {
	sum := sha256.Sum256(body)
	digest = "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	params := sfv.InnerList{
		Items: []sfv.Item{
			{Value: "@authority", Params: sfv.Params{{Key: "req", Value: true}}},
			{Value: "content-digest"},
		},
		Params: sfv.Params{
			{Key: "created", Value: created},
			{Key: "expires", Value: expires},
			{Key: "keyid", Value: Thumbprint(key.Public().(ed25519.PublicKey))},
			{Key: "tag", Value: "http-message-signatures-directory"},
		},
	}
	input, _ = sfv.SerializeInnerList(params)
	base := `"@authority";req: ` + authority + "\n" +
		`"content-digest": ` + digest + "\n" +
		`"@signature-params": ` + input
	return digest, input, ed25519.Sign(key, []byte(base))
}

// ParseKey reads a private key written by FormatKey.
func ParseKey(s string) (ed25519.PrivateKey, error) {
	seed, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, strconv.ErrSyntax
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// FormatKey encodes a private key (its seed) for storage.
func FormatKey(k ed25519.PrivateKey) string {
	return base64.RawURLEncoding.EncodeToString(k.Seed())
}
