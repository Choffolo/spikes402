package webbotauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Choffolo/spikes402/internal/sfv"
)

// Test vectors from draft-ietf-webbotauth-httpsig-protocol-01, appendix
// E.2, using the Ed25519 test key of RFC 9421 appendix B.1.4.
const (
	testSeedHex = "9f8362f87a484a954e6e740c5b4c0e84229139a20aa8ab56ff66586f6a7d29c5"
	testX       = "JrQLj5P_89iXES9-vFgrIy29clF9CC_oPPsw3c5D0bs"
)

func testKey(t *testing.T) ed25519.PrivateKey {
	seed, _ := hex.DecodeString(testSeedHex)
	k := ed25519.NewKeyFromSeed(seed)
	if base64.RawURLEncoding.EncodeToString(k.Public().(ed25519.PublicKey)) != testX {
		t.Fatal("test key does not match RFC 9421 B.1.4")
	}
	return k
}

func TestThumbprintVector(t *testing.T) {
	x, _ := base64.RawURLEncoding.DecodeString(testX)
	if got := Thumbprint(x); got != testKeyThumbprint {
		t.Fatalf("got %s", got)
	}
}

type vector struct {
	name, agent, input, signature, base string
}

var vectors = []vector{
	{
		name:      "E.2.1 dictionary Signature-Agent",
		agent:     `sig2="https://signature-agent.test"`,
		input:     `sig2=("@authority" "signature-agent";key="sig2");created=1735689600;keyid="poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U";alg="ed25519";expires=4889289600;nonce="n9p433xm+NJ3ph3upfBIGmsuwHw387YV7Q/F+6BSpGCVjYCqQw6rznNA8PVVLySrAWsv0hQtFioQb6E1YsauiA==";tag="web-bot-auth"`,
		signature: `sig2=:CGokAoY/FrPpyuzTe1Fb2kLTAH9Idw6qSFd1+D77r1firsMRCxeuLu5e6O+ek3zQgDMAzjiXtLVfqE00xnABCQ==:`,
		base: `"@authority": example.com
"signature-agent";key="sig2": "https://signature-agent.test"
"@signature-params": ("@authority" "signature-agent";key="sig2");created=1735689600;keyid="poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U";alg="ed25519";expires=4889289600;nonce="n9p433xm+NJ3ph3upfBIGmsuwHw387YV7Q/F+6BSpGCVjYCqQw6rznNA8PVVLySrAWsv0hQtFioQb6E1YsauiA==";tag="web-bot-auth"`,
	},
	{
		name:      "E.2.2 legacy Signature-Agent",
		agent:     `"https://signature-agent.test"`,
		input:     `sig2=("@authority" "signature-agent");created=1735689600;keyid="poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U";alg="ed25519";expires=1735693200;nonce="e8N7S2MFd/qrd6T2R3tdfAuuANngKI7LFtKYI/vowzk4lAZYadIX6wW25MwG7DCT9RUKAJ0qVkU0mEeLElW1qg==";tag="web-bot-auth"`,
		signature: `sig2=:jdq0SqOwHdyHr9+r5jw3iYZH6aNGKijYp/EstF4RQTQdi5N5YYKrD+mCT1HA1nZDsi6nJKuHxUi/5Syp3rLWBA==:`,
		base: `"@authority": example.com
"signature-agent": "https://signature-agent.test"
"@signature-params": ("@authority" "signature-agent");created=1735689600;keyid="poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U";alg="ed25519";expires=1735693200;nonce="e8N7S2MFd/qrd6T2R3tdfAuuANngKI7LFtKYI/vowzk4lAZYadIX6wW25MwG7DCT9RUKAJ0qVkU0mEeLElW1qg==";tag="web-bot-auth"`,
	},
}

func vectorRequest(v vector) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	r.Header.Set("Signature-Agent", v.agent)
	r.Header.Set("Signature-Input", v.input)
	r.Header.Set("Signature", v.signature)
	return r
}

func TestVectorsVerify(t *testing.T) {
	pub := testKey(t).Public().(ed25519.PublicKey)
	for _, v := range vectors {
		r := vectorRequest(v)
		agent, err := parseAgentHeader(r)
		if err != nil {
			t.Fatal(err)
		}
		inputs, _ := sfv.ParseDictionary(v.input)
		sigs, _ := sfv.ParseDictionary(v.signature)
		base, err := requestInfo{r: r, scheme: "https", agent: agent}.signatureBase(inputs.Get("sig2").(sfv.InnerList))
		if err != nil {
			t.Fatalf("%s: %v", v.name, err)
		}
		if base != v.base {
			t.Fatalf("%s: signature base differs:\n%s\nwant:\n%s", v.name, base, v.base)
		}
		if !ed25519.Verify(pub, []byte(base), sigs.Get("sig2").(sfv.Item).Value.([]byte)) {
			t.Fatalf("%s: signature does not verify", v.name)
		}
	}
}

func TestVectorSignReproduces(t *testing.T) {
	v := vectors[0]
	inputs, _ := sfv.ParseDictionary(v.input)
	r := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err := sign(r, testKey(t), "sig2", "https://signature-agent.test", "https", inputs.Get("sig2").(sfv.InnerList)); err != nil {
		t.Fatal(err)
	}
	if r.Header.Get("Signature") != v.signature || r.Header.Get("Signature-Input") != v.input || r.Header.Get("Signature-Agent") != v.agent {
		t.Fatalf("signing does not reproduce the vector:\n%s\n%s", r.Header.Get("Signature-Input"), r.Header.Get("Signature"))
	}
}

// E.2.3: signed directory response.
func TestVectorDirectorySignature(t *testing.T) {
	body := `{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U","x":"JrQLj5P_89iXES9-vFgrIy29clF9CC_oPPsw3c5D0bs","use":"sig"}]}`
	digest, input, sig := signDirectory(testKey(t), "signature-agent.test", []byte(body), 1735689600, 4889289600)
	if digest != "sha-256=:CADMT2aBdV/rqQr/NIru64ERQkCobVvllA4V0fLFDu0=:" {
		t.Fatalf("digest %s", digest)
	}
	if input != `("@authority";req "content-digest");created=1735689600;expires=4889289600;keyid="poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U";tag="http-message-signatures-directory"` {
		t.Fatalf("input %s", input)
	}
	if got := base64.StdEncoding.EncodeToString(sig); got != "l6P8R67tm3kujAxbHWio7ll01qrEZ0dKD/WWlGhNYEmTnFZM8Wt0VQ9zqGfvo7T/UMkBxsigzChM1Gpz7gOVBg==" {
		t.Fatalf("signature %s", got)
	}
	// Our directory JSON for the same key has the same content.
	if string(DirectoryJSON(testKey(t).Public().(ed25519.PublicKey))) != `{"keys":[{"crv":"Ed25519","kid":"poqkLGiymh_W0uP6PZFw-dvez3QJT5SolqXBCW38r0U","kty":"OKP","use":"sig","x":"JrQLj5P_89iXES9-vFgrIy29clF9CC_oPPsw3c5D0bs"}]}` {
		t.Fatal("unexpected directory JSON")
	}
}
