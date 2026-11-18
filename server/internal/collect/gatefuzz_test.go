package collect

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var fuzzSets = []struct{ name, chars string }{
	{"base62", "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"},
	{"base64url", "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"},
	{"hex", "0123456789abcdef"},
	{"base36", "abcdefghijklmnopqrstuvwxyz0123456789"},
	{"letters52", "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"},
	{"lower26", "abcdefghijklmnopqrstuvwxyz"},
	{"upper26", "ABCDEFGHIJKLMNOPQRSTUVWXYZ"},
}

const fuzzDraws = 20000

// fuzzRate is the share of seeded random tokens of one character set and length that the gate passes.
func fuzzRate(gate func(string) bool, chars string, n int) float64 {
	r := rand.New(rand.NewPCG(uint64(n), uint64(len(chars))))
	b := make([]byte, n)
	passed := 0
	for range fuzzDraws {
		for i := range b {
			b[i] = chars[r.IntN(len(chars))]
		}
		if gate(string(b)) {
			passed++
		}
	}
	return 100 * float64(passed) / fuzzDraws
}

// Measured pass rates in percent by length (CM-00010); the highest is base36 at 8 characters. One-case letters of
// 8 to 11 pass up to 0.18 more often, by chance abbreviations. A length takes the nearest listed one below.
var lookSecretRates = map[string]map[int]float64{
	"base62":    {8: 2.2, 10: 0.8, 12: 0.3, 15: 0.1},
	"base64url": {8: 4.1, 10: 1.6, 12: 0.6, 16: 0.1, 20: 0.05},
	"hex":       {8: 0.1, 12: 0},
	"base36":    {8: 11.6, 9: 7.5, 10: 2.1, 11: 0.9, 12: 0.6, 13: 0.3, 15: 0.1, 16: 0.05},
	"letters52": {8: 7.6, 10: 4.5, 12: 2.5, 14: 1.2, 16: 0.7, 20: 0.2},
	"lower26":   {8: 11, 9: 8.1, 10: 6.4, 11: 4.8, 12: 3.6, 14: 2.2, 16: 1, 20: 0.3, 24: 0.1},
	"upper26":   {8: 11, 9: 8.1, 10: 6.4, 11: 4.8, 12: 3.6, 14: 2.2, 16: 1, 20: 0.3, 24: 0.1},
}

func rateLimit(set string, n int) (float64, bool) {
	limit, found, at := 0.0, false, 0
	for k, v := range lookSecretRates[set] {
		if k <= n && k >= at {
			limit, found, at = v, true, k
		}
	}
	if n >= 20 && (!found || limit > 1) {
		return 1, true
	}
	return limit, found
}

func TestNameGateRandomTokens(t *testing.T) {
	if raceEnabled {
		t.Skip("2 million gate calls of pure logic take over a minute under the race detector")
	}
	var sb strings.Builder
	for _, set := range fuzzSets {
		fmt.Fprintf(&sb, "\n%-10s", set.name)
		for n := 8; n <= 32; n++ {
			rate := fuzzRate(DefaultNameGate, set.chars, n)
			fmt.Fprintf(&sb, " %d:%.2f", n, rate)
			if limit, ok := rateLimit(set.name, n); ok && rate > limit {
				t.Errorf("%s tokens of %d characters pass %.2f%%, over %.2f%%", set.name, n, rate, limit)
			}
		}
	}
	t.Log("GATE-FUZZ pass % by length, 20000 draws each:" + sb.String())
}

// Lowercase and digit groups of 3 to 6 joined by dashes, the shape of licence and order codes; total length 20 and
// up must pass at most 1%.
func TestNameGateDashedCodes(t *testing.T) {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	var sb strings.Builder
	for groups := 2; groups <= 7; groups++ {
		for size := 3; size <= 6; size++ {
			r := rand.New(rand.NewPCG(uint64(groups), uint64(size)))
			parts, passed := make([]string, groups), 0
			for range fuzzDraws {
				for i := range parts {
					parts[i] = randomFrom(r, chars, size)
				}
				if DefaultNameGate(strings.Join(parts, "-")) {
					passed++
				}
			}
			rate, length := 100*float64(passed)/fuzzDraws, groups*(size+1)-1
			fmt.Fprintf(&sb, " %dx%d:%.2f", groups, size, rate)
			if length >= 20 && rate > 1 {
				t.Errorf("%d groups of %d pass %.2f%%, over 1%%", groups, size, rate)
			}
		}
	}
	t.Log("GATE-FUZZ dashed pass %:" + sb.String())
}

// Lowercase letter groups joined by dashes; 12 letters and up must pass at most 7%, 16 and up at most 2.5%.
func TestNameGateDashedLetters(t *testing.T) {
	const chars = "abcdefghijklmnopqrstuvwxyz"
	var sb strings.Builder
	for groups := 2; groups <= 6; groups++ {
		for size := 3; size <= 6; size++ {
			r := rand.New(rand.NewPCG(uint64(groups), uint64(size)+100))
			parts, passed := make([]string, groups), 0
			for range fuzzDraws {
				for i := range parts {
					parts[i] = randomFrom(r, chars, size)
				}
				if DefaultNameGate(strings.Join(parts, "-")) {
					passed++
				}
			}
			rate, letters := 100*float64(passed)/fuzzDraws, groups*size
			fmt.Fprintf(&sb, " %dx%d:%.2f", groups, size, rate)
			if (letters >= 12 && rate > 7) || (letters >= 16 && rate > 2.5) {
				t.Errorf("%d groups of %d letters pass %.2f%%", groups, size, rate)
			}
		}
	}
	t.Log("GATE-FUZZ dashed letters pass %:" + sb.String())
}
