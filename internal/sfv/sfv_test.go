package sfv

import (
	"bytes"
	"testing"
)

func TestDictionaryRoundTrip(t *testing.T) {
	in := `sig2=("@authority" "signature-agent";key="sig2");created=1735689600;keyid="poqk";alg="ed25519";expires=4889289600;tag="web-bot-auth", b=?0, c;x=1, d=:aGk=:, e=tok/en`
	d, err := ParseDictionary(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 5 {
		t.Fatalf("got %d members", len(d))
	}
	l := d.Get("sig2").(InnerList)
	s, err := SerializeInnerList(l)
	if err != nil || s != `("@authority" "signature-agent";key="sig2");created=1735689600;keyid="poqk";alg="ed25519";expires=4889289600;tag="web-bot-auth"` {
		t.Fatalf("round trip: %q %v", s, err)
	}
	if d.Get("b").(Item).Value != false || d.Get("c").(Item).Value != true || d.Get("c").(Item).Params.Get("x") != int64(1) {
		t.Fatal("booleans/params")
	}
	if !bytes.Equal(d.Get("d").(Item).Value.([]byte), []byte("hi")) || d.Get("e").(Item).Value != Token("tok/en") {
		t.Fatal("bytes/token")
	}
}

func TestStringEscapes(t *testing.T) {
	it, err := ParseItem(`"a\"b\\c"`)
	if err != nil || it.Value != `a"b\c` {
		t.Fatalf("%v %v", it, err)
	}
	if s, _ := SerializeItem(it); s != `"a\"b\\c"` {
		t.Fatalf("got %s", s)
	}
}

func TestInvalid(t *testing.T) {
	for _, s := range []string{
		`a=1,`, `A=1`, `a=1.5`, `a="unterminated`, `a=:bad base64:`, `a=(1 2`, `a="\x"`, `a=?2`,
		`a=1234567890123456`, `a=@1659578233`, `a="é"`, `a=1 b=2`,
	} {
		if _, err := ParseDictionary(s); err == nil {
			t.Errorf("%q: expected an error", s)
		}
	}
	long := make([]byte, MaxLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := ParseDictionary(string(long)); err == nil {
		t.Error("oversized input accepted")
	}
}

func TestDuplicateKeysOverwrite(t *testing.T) {
	d, err := ParseDictionary(`a=1, b=2, a=3`)
	if err != nil || len(d) != 2 || d.Get("a").(Item).Value != int64(3) {
		t.Fatalf("%v %v", d, err)
	}
}
