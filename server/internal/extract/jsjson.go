package extract

import (
	"errors"
	"strconv"
	"unicode/utf8"
)

type undefined struct{}

// Undefined is what a missing property reads as, which JSON itself cannot hold.
var Undefined any = undefined{}

var errSyntax = errors.New("json syntax")

// Get is JavaScript's value?.[key] on a JSON.parse result: a missing key, or a value that is not an object, reads
// as Undefined. Arrays never hold the named keys this scanner reads.
func Get(value any, key string) any {
	if object, ok := value.(map[string]any); ok {
		if v, found := object[key]; found {
			return v
		}
	}
	return Undefined
}

// Truthy is JavaScript's Boolean(value).
func Truthy(value any) bool {
	switch v := value.(type) {
	case nil, undefined:
		return false
	case bool:
		return v
	case float64:
		return v == v && v != 0
	case string:
		return v != ""
	}
	return true
}

// Parse is JSON.parse(buffer.toString('utf8')) with no depth limit, as in V8. encoding/json differs: it folds key
// case, keeps invalid UTF-8 its own way and stops at depth 10000.
func Parse(b []byte) (any, error) {
	p := parser{b: b, budget: 4 * len(b)}
	return p.document()
}

type frame struct {
	array  []any
	object map[string]any
	key    string
}

type parser struct {
	b      []byte
	i      int
	buf    []byte
	budget int
}

// A long list is sized once, when it reaches listSizeAt items in a document of docSizeFrom bytes or more: growing a
// slice by repeated reallocation leaves every smaller copy resident, about five times the final size.
const (
	listSizeAt  = 4096
	docSizeFrom = 1 << 20
)

// remaining counts the values left in the list p.i is inside, 0 when unsure. Only a list of listSizeAt items starts
// a scan, and the bytes scanned in a document are capped at four times its size.
func (p *parser) remaining() int {
	b, depth, entries, seen := p.b, 0, 0, false
	for i := p.i; i < len(b); i++ {
		if i-p.i > p.budget {
			break
		}
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
			for i++; i < len(b) && b[i] != '"'; i++ {
				if b[i] == '\\' {
					i++
				}
			}
		case '[', '{':
			depth++
		case ']', '}':
			if depth == 0 {
				p.budget -= i - p.i
				if seen {
					entries++
				}
				return entries
			}
			depth--
		case ',':
			if depth == 0 {
				if seen {
					entries++
				}
				seen = false
				continue
			}
		}
		seen = true
	}
	p.budget = 0
	return 0
}

// A shared box per small whole number: a boxed float64 is an 8-byte allocation, and tokens are 2 bytes in a long list.
var smallNumbers = func() (boxes [1000]any) {
	for n := range boxes {
		boxes[n] = float64(n)
	}
	return
}()

func (p *parser) peek() byte {
	for p.i < len(p.b) {
		switch c := p.b[p.i]; c {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return c
		}
	}
	return 0
}

func (p *parser) key() (string, error) {
	if p.peek() != '"' {
		return "", errSyntax
	}
	k, err := p.text()
	if err != nil {
		return "", err
	}
	if p.peek() != ':' {
		return "", errSyntax
	}
	p.i++
	return k, nil
}

func (p *parser) scalar(c byte) (any, error) {
	switch c {
	case '"':
		return p.text()
	case 't', 'f', 'n':
		return p.literal()
	}
	return p.number()
}

func (p *parser) document() (any, error) {
	var stack []*frame
	for {
		var value any
		switch c := p.peek(); c {
		case '{':
			p.i++
			if p.peek() == '}' {
				p.i++
				value = map[string]any{}
				break
			}
			k, err := p.key()
			if err != nil {
				return nil, err
			}
			stack = append(stack, &frame{object: map[string]any{}, key: k})
			continue
		case '[':
			p.i++
			if p.peek() == ']' {
				p.i++
				value = []any{}
				break
			}
			stack = append(stack, &frame{array: []any{}})
			continue
		default:
			v, err := p.scalar(c)
			if err != nil {
				return nil, err
			}
			value = v
		}
		for {
			if len(stack) == 0 {
				if p.peek(); p.i != len(p.b) {
					return nil, errSyntax
				}
				return value, nil
			}
			top := stack[len(stack)-1]
			if top.object != nil {
				top.object[top.key] = value
			} else {
				top.array = append(top.array, value)
			}
			c := p.peek()
			p.i++
			if c == ',' {
				if len(top.array) == listSizeAt && len(p.b) >= docSizeFrom {
					if more := p.remaining(); more > 0 {
						top.array = append(make([]any, 0, listSizeAt+more), top.array...)
					}
				}
				if top.object != nil {
					k, err := p.key()
					if err != nil {
						return nil, err
					}
					top.key = k
				}
				break
			}
			switch {
			case top.object != nil && c == '}':
				value = top.object
			case top.object == nil && c == ']':
				value = top.array
			default:
				return nil, errSyntax
			}
			stack = stack[:len(stack)-1]
		}
	}
}

func (p *parser) literal() (any, error) {
	for _, l := range [...]struct {
		text  string
		value any
	}{{"true", true}, {"false", false}, {"null", nil}} {
		if len(p.b)-p.i >= len(l.text) && string(p.b[p.i:p.i+len(l.text)]) == l.text {
			p.i += len(l.text)
			return l.value, nil
		}
	}
	return nil, errSyntax
}

func digits(b []byte, i int) int {
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	return i
}

func (p *parser) number() (any, error) {
	b, start, i := p.b, p.i, p.i
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && b[i] >= '1' && b[i] <= '9':
		i = digits(b, i)
	default:
		return nil, errSyntax
	}
	if i < len(b) && b[i] == '.' {
		j := digits(b, i+1)
		if j == i+1 {
			return nil, errSyntax
		}
		i = j
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		j := digits(b, i)
		if j == i {
			return nil, errSyntax
		}
		i = j
	}
	p.i = i
	if i-start <= 3 && b[start] != '-' && digits(b, start) == i {
		n := 0
		for _, c := range b[start:i] {
			n = n*10 + int(c-'0')
		}
		return smallNumbers[n], nil
	}
	// Out of range gives ±Inf or 0 with ErrRange, the values V8 gives too.
	f, err := strconv.ParseFloat(string(b[start:i]), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, errSyntax
	}
	return f, nil
}

func hexValue(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[:4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}

// DecodeWHATWG reads one UTF-8 sequence as Node's Buffer.toString does: an invalid sequence costs one U+FFFD per
// maximal subpart and never swallows the byte that ended it.
func DecodeWHATWG(b []byte) (rune, int) {
	c := b[0]
	if c < 0x80 {
		return rune(c), 1
	}
	var need int
	lo, hi := byte(0x80), byte(0xbf)
	switch {
	case c >= 0xc2 && c <= 0xdf:
		need = 1
	case c >= 0xe0 && c <= 0xef:
		need = 2
		if c == 0xe0 {
			lo = 0xa0
		} else if c == 0xed {
			hi = 0x9f
		}
	case c >= 0xf0 && c <= 0xf4:
		need = 3
		if c == 0xf0 {
			lo = 0x90
		} else if c == 0xf4 {
			hi = 0x8f
		}
	default:
		return utf8.RuneError, 1
	}
	for k := 1; k <= need; k++ {
		if k >= len(b) || b[k] < lo || b[k] > hi {
			return utf8.RuneError, k
		}
		lo, hi = 0x80, 0xbf
	}
	r, _ := utf8.DecodeRune(b[:need+1])
	return r, need + 1
}

// A lone surrogate escape becomes U+FFFD: one UTF-16 unit like the surrogate, and the bytes Node hashes it as.
func (p *parser) text() (string, error) {
	b := p.b
	start := p.i + 1
	i := start
	for i < len(b) && b[i] != '"' && b[i] != '\\' && b[i] >= 0x20 && b[i] < 0x80 {
		i++
	}
	if i < len(b) && b[i] == '"' {
		p.i = i + 1
		return string(b[start:i]), nil
	}
	out := append(p.buf[:0], b[start:i]...)
	for {
		if i >= len(b) {
			return "", errSyntax
		}
		c := b[i]
		switch {
		case c == '"':
			p.i = i + 1
			p.buf = out
			return string(out), nil
		case c < 0x20:
			return "", errSyntax
		case c >= 0x80:
			r, size := DecodeWHATWG(b[i:])
			out = utf8.AppendRune(out, r)
			i += size
		case c != '\\':
			out = append(out, c)
			i++
		default:
			if i+1 >= len(b) {
				return "", errSyntax
			}
			esc := b[i+1]
			i += 2
			switch esc {
			case '"', '\\', '/':
				out = append(out, esc)
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				r, ok := hexValue(b[i:])
				if !ok {
					return "", errSyntax
				}
				i += 4
				if r >= 0xd800 && r <= 0xdbff && i+1 < len(b) && b[i] == '\\' && b[i+1] == 'u' {
					if low, ok := hexValue(b[i+2:]); ok && low >= 0xdc00 && low <= 0xdfff {
						r = 0x10000 + (r-0xd800)<<10 + (low - 0xdc00)
						i += 6
					}
				}
				if r >= 0xd800 && r <= 0xdfff {
					r = utf8.RuneError
				}
				out = utf8.AppendRune(out, r)
			default:
				return "", errSyntax
			}
		}
	}
}

// DecodeUTF8 is Buffer.toString('utf8') for bytes outside JSON strings, such as meta.json keys' surroundings.
func DecodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	out := make([]byte, 0, len(b)+8)
	for i := 0; i < len(b); {
		r, size := DecodeWHATWG(b[i:])
		out = utf8.AppendRune(out, r)
		i += size
	}
	return string(out)
}
