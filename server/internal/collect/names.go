package collect

import (
	"regexp"
	"strings"
)

// Other is the one label every name refused by the gate becomes; such rows are merged per sample.
const Other = "other"

// OtherReal is how a real name spelled like Other is shown, so it never merges into the Other row.
// The default gate refuses this spelling, so no real name can take it.
const OtherReal = "other (real)"

// NameGate reports whether a name may leave the collector. CM-00005 wires the shared label gate in here.
type NameGate func(string) bool

// Name returns s when the gate passes it, and Other otherwise. A nil gate is the strict default.
func (g NameGate) Name(s string) string {
	if g == nil {
		g = DefaultNameGate
	}
	switch {
	case !g(s):
		return Other
	case s == Other:
		return OtherReal
	}
	return s
}

// SecretCheck is the gate's second stage: it sees only names that passed the charset, length, word and fixed
// shape checks, and true refuses the name.
type SecretCheck func(string) bool

// secondStage refuses what either check flags; LooksSecret sees only what the collector cannot read as words.
var secondStage SecretCheck = func(s string) bool { return looksSecret(s) || wordLooksSecret(s) }

// NewNameGate returns the shape checks followed by secret. A nil secret refuses every name.
func NewNameGate(secret SecretCheck) NameGate {
	if secret == nil {
		return func(string) bool { return false }
	}
	return func(s string) bool { return nameShape(s) && !secret(s) }
}

// DefaultNameGate passes short names of letters, digits and ._+()- (or up to four capitalised words)
// that neither stage takes for a key, UUID, hex string or random token.
func DefaultNameGate(s string) bool { return nameShape(s) && !secondStage(s) }

const (
	maxNameLen   = 64
	maxNameWords = 4
	longHex      = 12
	longOneCase  = 33
	longDigits   = 8
	maxDigitRun  = 4
	maxMixedLen  = 24
	maxMixed     = 1
	mixedTotal   = 16
	maxAcronym   = 7
	maxShort     = 3
	// Measured limits (CM-00009): dictionary passphrases (camel case, dashed) and a word plus digits pass, and random
	// one-case letters at up to 11% for 8 letters and 1% for 16 (gatefuzz_test.go, CM-00010).
	maxDigits        = 9
	maxVersionDigits = 11
)

var (
	nameChars = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+()-]*$`)
	// A dotted quad, also after or before a word and a dot (host.10.0.0.1), or a bare number of 5 or more digits;
	// versions such as 2.1.34 or 1.2.3.4.5 pass.
	ipShape = regexp.MustCompile(`(?:^|[^0-9.]|[^0-9]\.)[0-9]{1,3}(?:\.[0-9]{1,3}){3}(?:$|[^0-9.]|\.(?:$|[^0-9]))`)
	// A four-part version (142.0.7444.59): no leading zeros, a minor of at most 2 digits and a last part of at most 3,
	// so a phone or card number in four dotted groups does not fit (CM-00010).
	versionShape = regexp.MustCompile(`(?:^|[^0-9.])([1-9][0-9]{0,2}\.(?:0|[1-9][0-9]?)\.(?:0|[1-9][0-9]{0,4})\.` +
		`(?:0|[1-9][0-9]{0,2}))(?:$|[^0-9.])`)
	bareNumber = regexp.MustCompile(`^[0-9]{5,}$`)
	uuidShape  = regexp.MustCompile(`(?i)[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}`)
	keyShape   = regexp.MustCompile(`(?:^|[^A-Za-z0-9])(?:sk-[A-Za-z0-9_-]{16,}|` +
		`sk-(?:ant|proj|live|test|or)(?:$|[^A-Za-z0-9])|(?:sk|pk|rk)_(?:live|test)_|` +
		`(?:hf|npm)_[A-Za-z0-9]{16,}|gh[pousr]_|github_pat_|glpat-|xox[a-z]-|shp[a-z]{2}_|dop_v1_)|` +
		`AKIA[0-9A-Z]{12,}|ASIA[0-9A-Z]{12,}|AIza[0-9A-Za-z_-]{16,}|eyJ[A-Za-z0-9_-]{8,}`)
)

func nameShape(s string) bool {
	if len(s) == 0 || len(s) > maxNameLen || !nameChars.MatchString(s) || strings.HasSuffix(s, " ") {
		return false
	}
	if uuidShape.MatchString(s) || keyShape.MatchString(s) || bareNumber.MatchString(s) {
		return false
	}
	if ipShape.MatchString(s) || dashedAddress(s) {
		return false
	}
	if strings.Contains(s, " ") {
		words := strings.Split(s, " ")
		if len(words) > maxNameWords {
			return false
		}
		for _, w := range words {
			if w == "" || !capitalised(w) {
				return false
			}
		}
	}
	return true
}

// dashedAddress: exactly four octets joined by - or _ in any mix (ip-10-0-0-1, 10-0_0-1), the first 10 or more or
// after an address word. Dashed versions (python-3-12-1-0), zero-padded dates and longer chains pass (CM-00010).
func dashedAddress(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			continue
		}
		start, groups, octets := i, 0, true
		for {
			from := i
			for i < len(s) && isDigit(s[i]) {
				i++
			}
			groups++
			octets = octets && octet(s[from:i])
			if i+1 >= len(s) || (s[i] != '-' && s[i] != '_') || !isDigit(s[i+1]) {
				break
			}
			i++
		}
		if groups == 4 && octets && (isDigit(s[start+1]) || addressWord(s[:start])) {
			return true
		}
	}
	return false
}

func octet(g string) bool {
	return len(g) == 1 || (len(g) == 2 && g[0] != '0') || (len(g) == 3 && g[0] != '0' && g <= "255")
}

// addressWord: the text before a quad, less its closing separators, ends in the word ip, host or addr.
func addressWord(before string) bool {
	before = strings.ToLower(strings.TrimRight(before, "-_."))
	for _, w := range [...]string{"ip", "host", "addr"} {
		if rest, ok := strings.CutSuffix(before, w); ok && (rest == "" || !isLower(rest[len(rest)-1])) {
			return true
		}
	}
	return false
}

// looksSecret: parts read as words, numbers or names (php84); unsayable or, past 3, short lowercase parts are mixed.
// Letters must score as English letter pairs (names_pairs.go); 9 digits in all read as a card, SSN or phone number.
func looksSecret(s string) bool {
	mixed, mixedLen, short, prev := 0, 0, 0, ""
	var runs pairRuns
	for _, p := range strings.FieldsFunc(s, isNameSeparator) {
		if tokenPart(p) || (count(p, isDigit) == len(p) && commonPassword(prev+p)) {
			return true
		}
		prev = p
		runs.add(p)
		switch {
		case strings.IndexFunc(p, isDigitRune) >= 0 && strings.IndexFunc(p, isLetterRune) >= 0:
			mixed++
			mixedLen += len(p)
		case len(p) <= 3 && count(p, isLower) == len(p):
			short++
		case unsayable(p):
			mixed++
		}
	}
	return mixed+max(short-maxShort, 0) > maxMixed || mixedLen >= mixedTotal || manyDigits(s) || runs.random()
}

// manyDigits: 9 digits in all, not counting one four-part version of at most 11 digits.
func manyDigits(s string) bool {
	digits := count(s, isDigit)
	if digits < maxDigits {
		return false
	}
	if m := versionShape.FindStringSubmatchIndex(s); m != nil && m[3]-m[2]-3 <= maxVersionDigits {
		digits -= m[3] - m[2] - 3
	}
	return digits >= maxDigits
}

func unsayable(p string) bool {
	return len(p) >= 4 && count(p, isLower) == len(p) && !sayable(p)
}

func isNameSeparator(r rune) bool {
	return r == ' ' || r == '.' || r == '_' || r == '-' || r == '+' || r == '(' || r == ')'
}

func capitalised(w string) bool {
	w = strings.TrimPrefix(w, "(")
	return w != "" && (isUpper(w[0]) || isDigit(w[0]))
}

func isLower(c byte) bool      { return c >= 'a' && c <= 'z' }
func isUpper(c byte) bool      { return c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isHex(c byte) bool        { return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }
func isDigitRune(r rune) bool  { return r >= '0' && r <= '9' }
func isLetterRune(r rune) bool { return r < 128 && (isLower(byte(r)) || isUpper(byte(r))) }

func isVowel(c byte) bool {
	switch c | 0x20 {
	case 'a', 'e', 'i', 'o', 'u', 'y':
		return true
	}
	return false
}

func count(s string, test func(byte) bool) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if test(s[i]) {
			n++
		}
	}
	return n
}

func tokenPart(p string) bool {
	n, digits, upper, lower := len(p), count(p, isDigit), count(p, isUpper), count(p, isLower)
	switch {
	case n >= longHex && count(p, isHex) == n, commonPassword(p):
		return true
	case digits == n:
		return n >= longDigits
	case n < 3:
		return false
	case digits == 0 && (upper == 0 || lower == 0):
		return n >= longOneCase
	case digits == 0:
		return !readsAsWords(p)
	}
	return !ordinaryMixed(p)
}

// camelWords: mixed-case letters split into words with a vowel and acronyms of up to 7 capitals, no more
// acronyms than words; a lone capital may open (XProtect), a lowercase word may lead (iTerm, macOS).
func camelWords(p string) bool {
	start, acronyms, words := 0, 0, 0
	for i := 1; i <= len(p); i++ {
		if i < len(p) && !wordBreak(p, i) {
			continue
		}
		w := p[start:i]
		if !camelWord(w, start == 0) {
			return false
		}
		if count(w, isUpper) == len(w) {
			acronyms++
		} else {
			words++
		}
		start = i
	}
	return acronyms <= words
}

func wordBreak(p string, i int) bool {
	if !isUpper(p[i]) {
		return false
	}
	return isLower(p[i-1]) || (i+1 < len(p) && isLower(p[i+1]))
}

func camelWord(w string, first bool) bool {
	upper := count(w, isUpper)
	switch {
	case upper == len(w):
		return (len(w) >= 2 || first) && len(w) <= maxAcronym
	case upper == 0 && first:
		return len(w) < 3 || count(w, isVowel) > 0
	case len(w) == 2:
		return twoLetterWords[w]
	}
	return count(w, isVowel) > 0
}

// twoLetterWords are the capitalised two-letter words real names use (ShipIt, WiFiAgent, TGOnDevice).
var twoLetterWords = map[string]bool{"An": true, "As": true, "At": true, "By": true, "Do": true, "Fi": true, "Go": true,
	"If": true, "In": true, "Is": true, "It": true, "Me": true, "My": true, "No": true, "Of": true, "On": true, "Or": true,
	"So": true, "To": true, "Up": true, "We": true, "Wi": true}

// ordinaryMixed: one run of up to 4 digits at the end (php84, iTerm2), first (1Password, 3d), after 1 to 3
// letters (s3bucket, k8s), or 1 or 2 digits between lowercase words (appleh13camerad, wifip2pd).
func ordinaryMixed(p string) bool {
	i := strings.IndexFunc(p, isDigitRune)
	j := i
	for j < len(p) && isDigit(p[j]) {
		j++
	}
	before, after := p[:i], p[j:]
	if len(p) > maxMixedLen || j-i > maxDigitRun || strings.IndexFunc(after, isDigitRune) >= 0 {
		return false
	}
	switch {
	case after == "":
	case before == "":
		if len(after) > 3 && !(isUpper(after[0]) && count(after, isLower) > 0) {
			return false
		}
	case j-i > 2 || count(before, isUpper)+count(after, isUpper) > 0:
		return false
	case len(before) > 3 || j-i > 1:
		return len(before) >= 4 && len(after) >= 2 && count(before, isVowel) > 0 &&
			!consonantRun(before, 4) && !consonantRun(after, 3)
	}
	if len(p) >= 8 && (twoCapitals(before) || twoCapitals(after)) {
		return false
	}
	return lettersRead(before, after == "") && lettersRead(after, false)
}

// twoCapitals finds a two-capital acronym in camel case (ABCdef is AB and Cdef); real ones are rare beside digits.
func twoCapitals(w string) bool {
	run := 0
	for i := 0; i <= len(w); i++ {
		if i < len(w) && isUpper(w[i]) {
			run++
			continue
		}
		if (run == 3 && i < len(w)) || (run == 2 && i == len(w) && len(w) > 2) {
			return true
		}
		run = 0
	}
	return false
}

func lettersRead(w string, acronym bool) bool {
	upper, lower := count(w, isUpper), count(w, isLower)
	switch {
	case w == "":
		return true
	case upper > 0 && lower > 0:
		return camelWords(w)
	case upper > 0:
		return len(w) <= 1 || (acronym && len(w) <= 4)
	}
	return !consonantRun(w, 4)
}

// consonantRun counts "sql" as a syllable, for mysql, sqlite and postgresql.
func consonantRun(w string, n int) bool {
	w = strings.ReplaceAll(strings.ToLower(w), "sql", "a")
	run := 0
	for i := 0; i < len(w); i++ {
		if isVowel(w[i]) {
			run = 0
		} else if run++; run >= n {
			return true
		}
	}
	return false
}
