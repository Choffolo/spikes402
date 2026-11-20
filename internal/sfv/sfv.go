// Package sfv parses and serializes the subset of HTTP Structured Field
// Values (RFC 8941) that HTTP Message Signatures (RFC 9421) need:
// dictionaries, inner lists, and items that are integers, strings,
// tokens, byte sequences or booleans, with parameters. Decimals, dates
// and display strings are rejected.
package sfv

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// Token is a bare token such as "foo" or "*".
type Token string

// Param is one parameter; Value is int64, string, Token, []byte or bool.
type Param struct {
	Key   string
	Value any
}

// Params keep their order: it matters for signatures.
type Params []Param

// Get returns the value of key, or nil.
func (ps Params) Get(key string) any {
	for _, p := range ps {
		if p.Key == key {
			return p.Value
		}
	}
	return nil
}

func (ps *Params) set(key string, v any) {
	for i := range *ps {
		if (*ps)[i].Key == key {
			(*ps)[i].Value = v // RFC 8941: a repeated key overwrites
			return
		}
	}
	*ps = append(*ps, Param{key, v})
}

// Item is a bare item with parameters.
type Item struct {
	Value  any
	Params Params
}

// InnerList is a parenthesised list of items with parameters.
type InnerList struct {
	Items  []Item
	Params Params
}

// Member is one dictionary entry; Value is an Item or an InnerList.
type Member struct {
	Key   string
	Value any
}

// Dictionary keeps member order.
type Dictionary []Member

// Get returns the value of key, or nil.
func (d Dictionary) Get(key string) any {
	for _, m := range d {
		if m.Key == key {
			return m.Value
		}
	}
	return nil
}

// MaxLen bounds the input accepted by the parser.
const MaxLen = 8192

var errSyntax = errors.New("sfv: invalid structured field")

type parser struct {
	s string
	i int
}

// ParseDictionary parses a Dictionary (RFC 8941 section 4.2.2).
func ParseDictionary(s string) (Dictionary, error) {
	if len(s) > MaxLen {
		return nil, errSyntax
	}
	p := &parser{s: strings.Trim(s, " ")}
	var d Dictionary
	for p.i < len(p.s) {
		key, err := p.key()
		if err != nil {
			return nil, err
		}
		var v any
		if p.peek() == '=' {
			p.i++
			if v, err = p.itemOrInnerList(); err != nil {
				return nil, err
			}
		} else {
			ps, err := p.params()
			if err != nil {
				return nil, err
			}
			v = Item{Value: true, Params: ps}
		}
		replaced := false
		for j := range d {
			if d[j].Key == key {
				d[j].Value, replaced = v, true
			}
		}
		if !replaced {
			d = append(d, Member{key, v})
		}
		p.ows()
		if p.i == len(p.s) {
			return d, nil
		}
		if p.peek() != ',' {
			return nil, errSyntax
		}
		p.i++
		p.ows()
		if p.i == len(p.s) {
			return nil, errSyntax // trailing comma
		}
	}
	return d, nil
}

// ParseItem parses a single Item (RFC 8941 section 4.2.3).
func ParseItem(s string) (Item, error) {
	if len(s) > MaxLen {
		return Item{}, errSyntax
	}
	p := &parser{s: strings.Trim(s, " ")}
	it, err := p.item()
	if err != nil || p.i != len(p.s) {
		return Item{}, errSyntax
	}
	return it, nil
}

func (p *parser) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

func (p *parser) ows() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *parser) sp() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func isLCAlpha(c byte) bool { return c >= 'a' && c <= 'z' }
func isAlpha(c byte) bool   { return isLCAlpha(c) || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool   { return c >= '0' && c <= '9' }

func isTChar(c byte) bool {
	return isAlpha(c) || isDigit(c) || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

func (p *parser) key() (string, error) {
	start := p.i
	if c := p.peek(); !isLCAlpha(c) && c != '*' {
		return "", errSyntax
	}
	for p.i < len(p.s) {
		c := p.s[p.i]
		if !isLCAlpha(c) && !isDigit(c) && c != '_' && c != '-' && c != '.' && c != '*' {
			break
		}
		p.i++
	}
	return p.s[start:p.i], nil
}

func (p *parser) itemOrInnerList() (any, error) {
	if p.peek() == '(' {
		return p.innerList()
	}
	return p.item()
}

func (p *parser) innerList() (InnerList, error) {
	p.i++ // '('
	var l InnerList
	for p.i < len(p.s) {
		p.sp()
		if p.peek() == ')' {
			p.i++
			ps, err := p.params()
			if err != nil {
				return InnerList{}, err
			}
			l.Params = ps
			return l, nil
		}
		it, err := p.item()
		if err != nil {
			return InnerList{}, err
		}
		l.Items = append(l.Items, it)
		if c := p.peek(); c != ' ' && c != ')' {
			return InnerList{}, errSyntax
		}
	}
	return InnerList{}, errSyntax
}

func (p *parser) item() (Item, error) {
	v, err := p.bareItem()
	if err != nil {
		return Item{}, err
	}
	ps, err := p.params()
	if err != nil {
		return Item{}, err
	}
	return Item{Value: v, Params: ps}, nil
}

func (p *parser) params() (Params, error) {
	var ps Params
	for p.peek() == ';' {
		p.i++
		p.sp()
		key, err := p.key()
		if err != nil {
			return nil, err
		}
		var v any = true
		if p.peek() == '=' {
			p.i++
			if v, err = p.bareItem(); err != nil {
				return nil, err
			}
		}
		ps.set(key, v)
	}
	return ps, nil
}

func (p *parser) bareItem() (any, error) {
	c := p.peek()
	switch {
	case c == '-' || isDigit(c):
		return p.integer()
	case c == '"':
		return p.str()
	case c == '*' || isAlpha(c):
		return p.token(), nil
	case c == ':':
		return p.bytes()
	case c == '?':
		return p.boolean()
	}
	return nil, errSyntax
}

func (p *parser) integer() (int64, error) {
	start := p.i
	if p.peek() == '-' {
		p.i++
	}
	digits := p.i
	for p.i < len(p.s) && isDigit(p.s[p.i]) {
		p.i++
	}
	n := p.i - digits
	if n == 0 || n > 15 || p.peek() == '.' { // decimals are not supported
		return 0, errSyntax
	}
	return strconv.ParseInt(p.s[start:p.i], 10, 64)
}

func (p *parser) str() (string, error) {
	p.i++ // '"'
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		switch {
		case c == '\\':
			if p.i == len(p.s) || (p.s[p.i] != '"' && p.s[p.i] != '\\') {
				return "", errSyntax
			}
			b.WriteByte(p.s[p.i])
			p.i++
		case c == '"':
			return b.String(), nil
		case c < 0x20 || c > 0x7e:
			return "", errSyntax
		default:
			b.WriteByte(c)
		}
	}
	return "", errSyntax
}

func (p *parser) token() Token {
	start := p.i
	p.i++
	for p.i < len(p.s) && (isTChar(p.s[p.i]) || p.s[p.i] == ':' || p.s[p.i] == '/') {
		p.i++
	}
	return Token(p.s[start:p.i])
}

func (p *parser) bytes() ([]byte, error) {
	p.i++ // ':'
	end := strings.IndexByte(p.s[p.i:], ':')
	if end < 0 {
		return nil, errSyntax
	}
	b, err := base64.StdEncoding.DecodeString(p.s[p.i : p.i+end])
	if err != nil {
		return nil, errSyntax
	}
	p.i += end + 1
	return b, nil
}

func (p *parser) boolean() (bool, error) {
	p.i++ // '?'
	switch p.peek() {
	case '1':
		p.i++
		return true, nil
	case '0':
		p.i++
		return false, nil
	}
	return false, errSyntax
}

// SerializeItem returns the canonical serialization of an item.
func SerializeItem(it Item) (string, error) {
	var b strings.Builder
	if err := writeBare(&b, it.Value); err != nil {
		return "", err
	}
	if err := writeParams(&b, it.Params); err != nil {
		return "", err
	}
	return b.String(), nil
}

// SerializeInnerList returns the canonical serialization of an inner list.
func SerializeInnerList(l InnerList) (string, error) {
	var b strings.Builder
	b.WriteByte('(')
	for i, it := range l.Items {
		if i > 0 {
			b.WriteByte(' ')
		}
		s, err := SerializeItem(it)
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	b.WriteByte(')')
	if err := writeParams(&b, l.Params); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeParams(b *strings.Builder, ps Params) error {
	for _, p := range ps {
		b.WriteByte(';')
		b.WriteString(p.Key)
		if v, ok := p.Value.(bool); ok && v {
			continue
		}
		b.WriteByte('=')
		if err := writeBare(b, p.Value); err != nil {
			return err
		}
	}
	return nil
}

func writeBare(b *strings.Builder, v any) error {
	switch v := v.(type) {
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case string:
		b.WriteByte('"')
		for i := 0; i < len(v); i++ {
			c := v[i]
			if c < 0x20 || c > 0x7e {
				return errSyntax
			}
			if c == '"' || c == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
		}
		b.WriteByte('"')
	case Token:
		b.WriteString(string(v))
	case []byte:
		b.WriteByte(':')
		b.WriteString(base64.StdEncoding.EncodeToString(v))
		b.WriteByte(':')
	case bool:
		if v {
			b.WriteString("?1")
		} else {
			b.WriteString("?0")
		}
	default:
		return errSyntax
	}
	return nil
}
