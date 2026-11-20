// Package webbotauth implements Web Bot Auth: bots sign their HTTP
// requests with HTTP Message Signatures (RFC 9421) using Ed25519 and say
// where their public keys live with the Signature-Agent header, as in
// draft-ietf-webbotauth-httpsig-protocol-01.
//
// The verifier is deliberately strict. It understands a fixed set of
// covered components and treats anything else as unverifiable rather
// than guessing.
package webbotauth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/Choffolo/spikes402/internal/sfv"
)

// Tag is the required value of the "tag" signature parameter.
const Tag = "web-bot-auth"

// DirectoryPath is the well-known location of a key directory.
const DirectoryPath = "/.well-known/http-message-signatures-directory"

// DirectoryMediaType is the media type a well-known directory is served with.
const DirectoryMediaType = "application/http-message-signatures-directory+json"

var errComponent = errors.New("unsupported or missing covered component")

// requestInfo is what the signature base is computed from.
type requestInfo struct {
	r      *http.Request
	scheme string // "https" or "http", as seen by the client
	agent  agentHeader
}

// agentHeader is the parsed Signature-Agent field: either a dictionary
// (current form) or a single string (legacy form, draft section 5.2.1).
type agentHeader struct {
	dict   sfv.Dictionary
	legacy bool
	raw    string
}

func parseAgentHeader(r *http.Request) (agentHeader, error) {
	raw := strings.TrimSpace(strings.Join(r.Header.Values("Signature-Agent"), ", "))
	if raw == "" {
		return agentHeader{}, nil
	}
	if strings.HasPrefix(raw, `"`) {
		if _, err := sfv.ParseItem(raw); err != nil {
			return agentHeader{}, err
		}
		return agentHeader{legacy: true, raw: raw}, nil
	}
	d, err := sfv.ParseDictionary(raw)
	return agentHeader{dict: d, raw: raw}, err
}

// agentURL returns the Signature-Agent URL for a signature label.
func (a agentHeader) agentURL(label string) (string, bool) {
	if a.legacy {
		it, err := sfv.ParseItem(a.raw)
		s, ok := it.Value.(string)
		return s, err == nil && ok && len(it.Params) == 0
	}
	it, ok := a.dict.Get(label).(sfv.Item)
	if !ok || len(it.Params) > 0 { // members with parameters MUST be ignored
		return "", false
	}
	s, ok := it.Value.(string)
	return s, ok
}

// authority returns the @authority value: lower-case host, default port removed.
func authority(host, scheme string) string {
	host = strings.ToLower(host)
	if h, port, err := net.SplitHostPort(host); err == nil {
		if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
			if strings.Contains(h, ":") {
				return "[" + h + "]"
			}
			return h
		}
	}
	return host
}

// componentValue returns the value of one covered component (RFC 9421
// section 2). Supported: @method, @authority, @scheme, @target-uri,
// @request-target, @path, @query, plain header fields, and
// "signature-agent";key=... . Anything else fails.
func (ri requestInfo) componentValue(c sfv.Item) (string, error) {
	name, ok := c.Value.(string)
	if !ok || name == "" {
		return "", errComponent
	}
	r := ri.r
	if strings.HasPrefix(name, "@") {
		if len(c.Params) > 0 {
			return "", errComponent
		}
		switch name {
		case "@method":
			return r.Method, nil
		case "@authority":
			return authority(r.Host, ri.scheme), nil
		case "@scheme":
			return ri.scheme, nil
		case "@target-uri":
			return ri.scheme + "://" + authority(r.Host, ri.scheme) + r.URL.RequestURI(), nil
		case "@request-target":
			return r.URL.RequestURI(), nil
		case "@path":
			if p := r.URL.EscapedPath(); p != "" {
				return p, nil
			}
			return "/", nil
		case "@query":
			return "?" + r.URL.RawQuery, nil
		}
		return "", errComponent
	}
	if name != strings.ToLower(name) {
		return "", errComponent
	}
	if name == "signature-agent" {
		key, hasKey := c.Params.Get("key").(string)
		if len(c.Params) > 1 || (len(c.Params) == 1 && !hasKey) {
			return "", errComponent
		}
		if !hasKey { // legacy form: the whole field
			if !ri.agent.legacy {
				return "", errComponent
			}
			return ri.agent.raw, nil
		}
		if ri.agent.legacy {
			return "", errComponent
		}
		v := ri.agent.dict.Get(key)
		it, ok := v.(sfv.Item)
		if !ok {
			return "", errComponent
		}
		return sfv.SerializeItem(it)
	}
	if len(c.Params) > 0 { // ;sf, ;bs, ;key on other fields, ;req, ;tr: not supported
		return "", errComponent
	}
	values := r.Header.Values(http.CanonicalHeaderKey(name))
	if len(values) == 0 {
		return "", errComponent
	}
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	return strings.Join(values, ", "), nil
}

// signatureBase builds the signature base (RFC 9421 section 2.5).
func (ri requestInfo) signatureBase(params sfv.InnerList) (string, error) {
	var b strings.Builder
	seen := map[string]bool{}
	for _, c := range params.Items {
		id, err := sfv.SerializeItem(c)
		if err != nil {
			return "", err
		}
		if seen[id] {
			return "", fmt.Errorf("component %s covered twice", id)
		}
		seen[id] = true
		v, err := ri.componentValue(c)
		if err != nil {
			return "", fmt.Errorf("%w: %s", err, id)
		}
		if strings.ContainsAny(v, "\r\n") {
			return "", errComponent
		}
		b.WriteString(id)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteByte('\n')
	}
	sp, err := sfv.SerializeInnerList(params)
	if err != nil {
		return "", err
	}
	b.WriteString(`"@signature-params": `)
	b.WriteString(sp)
	return b.String(), nil
}

// Thumbprint returns the JWK SHA-256 thumbprint (RFC 7638, RFC 8037
// appendix A.3) of an Ed25519 public key, base64url without padding.
// It is the keyid of signatures made with that key.
func Thumbprint(x []byte) string {
	canonical := `{"crv":"Ed25519","kty":"OKP","x":"` + base64.RawURLEncoding.EncodeToString(x) + `"}`
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
