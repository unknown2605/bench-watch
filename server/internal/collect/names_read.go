package collect

import (
	"regexp"
	"strings"

	"benchwatch/server/internal/labels"
)

// wordLooksSecret gives LooksSecret each word minus the parts readsAsWords accepts, whose letter rules misread
// WiFiAgent, mDNSResponder and mysqldb8 (CM-00009 QA-M1); its key prefixes and UUIDs still see the whole name.
func wordLooksSecret(s string) bool {
	if labels.KeyOrUUID(s) {
		return true
	}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == '(' || r == ')' }) {
		var rest []string
		for _, p := range strings.FieldsFunc(w, isNameSeparator) {
			if !readsAsWords(p) {
				rest = append(rest, p)
			}
		}
		if len(rest) > 0 && labels.LooksSecret(strings.Join(rest, "-")) {
			return true
		}
	}
	return false
}

var lowerWithNumber = regexp.MustCompile(`^([a-z]{3,})[0-9]{1,4}$`)

// shortNames are the mixed-case names with no word of 3 letters; a rule by shape passed random chunks (CM-00010 QA).
var shortNames = wordSet("iOS tvOS xrOS WiFi")

// readsAsWords: case changes split letters into sayable words, acronyms of up to 7 capitals and known two-letter
// words, and words of 3 or more letters hold half of the part; or a sayable word and up to 4 digits (mysqldb8).
func readsAsWords(p string) bool {
	if m := lowerWithNumber.FindStringSubmatch(p); m != nil {
		return count(m[1], isHex) < len(m[1]) && sayable(m[1])
	}
	if count(p, isLower)+count(p, isUpper) != len(p) {
		return false
	}
	words, acronyms, long, afterWord, start := 0, 0, 0, false, 0
	for i := 1; i <= len(p); i++ {
		if i < len(p) && !wordBreak(p, i) {
			continue
		}
		w := p[start:i]
		upper := count(w, isUpper)
		switch {
		case upper == len(w) && (len(w) >= 2 || start == 0) && len(w) <= maxAcronym:
			acronyms++
		case upper == 0 && start == 0 && len(w) <= 3:
			words++
		case len(w) == 2 && twoLetterWords[w]:
			words++
		case len(w) >= 3 && upper == 1 && isUpper(w[0]) && sayable(strings.ToLower(w)):
			words++
		case len(w) >= 3 && upper == 0 && start == 0 && sayable(w):
			words++
		default:
			return false
		}
		if len(w) >= 3 && (upper < len(w) || afterWord) {
			long += len(w)
		}
		afterWord = len(w) >= 3 && upper < len(w)
		start = i
	}
	return acronyms <= words && (2*long+1 >= len(p) || shortNames[p])
}

var digraphs = strings.NewReplacer("ght", "t", "ck", "k", "ch", "c", "sh", "s", "th", "t", "ph", "f")

const (
	minCompound   = 5
	compoundFloor = -2
)

// sayable reads a digraph as one consonant (Background, Strength); 4 consonants in a row also pass in a word of 5
// or more letters whose letter pairs score as English, such as a compound (Subscriber, Postgres, Workflow; CM-00010).
func sayable(w string) bool {
	if count(w, isVowel) == 0 {
		return false
	}
	// Replace allocates, so only a word that has 4 consonants in a row is rewritten.
	if !consonantRun(w, 4) || !consonantRun(digraphs.Replace(w), 4) {
		return true
	}
	return len(w) >= minCompound && pairScore(w) >= compoundFloor
}

// passwordWords are bases of the most common passwords; real names do not end one in digits (Summer2024, hunter2).
var passwordWords = wordSet(`password passwd welcome summer winter spring autumn hunter letmein qwerty qwertyuiop asdf
	asdfgh asdfghjkl zxcvbn iloveyou sunshine princess football baseball soccer hockey superman batman dragon monkey
	shadow michael jordan trustno abc abcd admin login secret changeme freedom whatever charlie starwars pokemon flower
	purple jessica ashley january february march april june july august september october november december`)

var leet = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t")

// commonPassword: a password base with digits after it, or spelled with digits for letters (passw0rd).
func commonPassword(p string) bool {
	base := strings.TrimRight(p, "0123456789")
	if base == "" || !isLetterRune(rune(base[0])) {
		return false
	}
	word := strings.ToLower(leet.Replace(base))
	return passwordWords[word] && (base != p || word != strings.ToLower(base) || alwaysPassword[word])
}

var alwaysPassword = wordSet("password passwd letmein qwerty qwertyuiop asdfghjkl iloveyou changeme trustno")

func wordSet(text string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(text) {
		set[w] = true
	}
	return set
}
