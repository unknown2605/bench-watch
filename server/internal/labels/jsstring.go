package labels

import "unicode/utf8"

// JSSpace is the class JavaScript's \s, trim() and Date.parse use: WhiteSpace plus LineTerminator.
// Go's unicode.IsSpace differs: it adds U+0085 and drops U+FEFF.
func JSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// JSSpaceClass is JSSpace as the body of a regexp character class.
const JSSpaceClass = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// LineTerminator is what JavaScript's regexp dot refuses to match.
func LineTerminator(r rune) bool { return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 }

func TrimStartJS(s string) string {
	for i, r := range s {
		if !JSSpace(r) {
			return s[i:]
		}
	}
	return ""
}

func TrimJS(s string) string {
	s = TrimStartJS(s)
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if !JSSpace(r) {
			break
		}
		s = s[:len(s)-size]
	}
	return s
}

func indexJSSpace(s string) int {
	for i, r := range s {
		if JSSpace(r) {
			return i
		}
	}
	return -1
}

// UTF16Len is JavaScript's String length. A lone surrogate reaches Go as U+FFFD, which also counts 1.
func UTF16Len(s string) int {
	n := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			n++
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
		i += size
	}
	return n
}

// UTF16Prefix is JavaScript's s.slice(0, n). A cut through a surrogate pair leaves a lone
// surrogate there, which stands here as U+FFFD: both are one non-space unit no gate accepts.
func UTF16Prefix(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			if units < n {
				return s[:i] + "\xef\xbf\xbd"
			}
			return s[:i]
		}
		units += w
	}
	return s
}
