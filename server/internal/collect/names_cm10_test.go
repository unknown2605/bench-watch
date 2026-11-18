package collect

import (
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

const (
	lowerLetters = "abcdefghijklmnopqrstuvwxyz"
	upperLetters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digitChars   = "0123456789"
	hexChars     = "0123456789abcdef"
)

func TestTheGatePassesNamesGluedToAbbreviations(t *testing.T) {
	for _, s := range []string{"mysqldump", "mysqlcheck", "vacuumdb", "pg_amcheck", "xpcproxy", "rpcsvchost", "audiomxd",
		"cfprefsd", "newfs_msdos", "com.apple.fskit.msdos", "snmptrapd", "pdftotext"} {
		if !DefaultNameGate(s) {
			t.Errorf("refused real name %q", s)
		}
	}
}

// An abbreviation or a daemon's d used to lift its pairs beside any number of letters (CM-00010 QA): sql and 12
// random letters passed at 4%, against 0.5% before the list existed.
func TestNameGateRandomLettersBesideAbbreviations(t *testing.T) {
	if raceEnabled {
		t.Skip("160 thousand gate calls of pure logic are slow under the race detector")
	}
	chunks := func(r *rand.Rand, n int) string {
		parts := make([]string, n/4)
		for i := range parts {
			parts[i] = randomFrom(r, lowerLetters, 3) + "d"
		}
		return strings.Join(parts, "-")
	}
	shapes := []struct {
		name  string
		limit float64
		make  func(r *rand.Rand) string
	}{
		{"sql+X", 1, func(r *rand.Rand) string { return "sql" + randomFrom(r, lowerLetters, 12) }},
		{"mysql+X", 1, func(r *rand.Rand) string { return "mysql" + randomFrom(r, lowerLetters, 12) }},
		{"X+db", 2, func(r *rand.Rand) string { return randomFrom(r, lowerLetters, 12) + "db" }},
		{"pg_X", 2, func(r *rand.Rand) string { return "pg_" + randomFrom(r, lowerLetters, 12) }},
		{"X+d", 5.5, func(r *rand.Rand) string { return randomFrom(r, lowerLetters, 11) + "d" }},
		{"sql+X+db", 0.5, func(r *rand.Rand) string { return "sql" + randomFrom(r, lowerLetters, 12) + "db" }},
		{"3 chunks ending in d", 6.5, func(r *rand.Rand) string { return chunks(r, 12) }},
		{"4 chunks ending in d", 2, func(r *rand.Rand) string { return chunks(r, 16) }},
	}
	for i, shape := range shapes {
		if rate := shapeRate(uint64(100+i), shape.make); rate > shape.limit {
			t.Errorf("%s passes %.2f%%, over %.2f%%", shape.name, rate, shape.limit)
		}
	}
	for _, s := range []string{"sqlxkqzvwbtfs", "qzxvkjwpfhtmdb", "zxqd-vkjd-wpqd", "syncqzxvkjwpfhtm"} {
		if DefaultNameGate(s) {
			t.Errorf("passed random letters beside an abbreviation: %q", s)
		}
	}
}

// A part of up to 4 letters in mixed case used to pass unread (CM-00010 QA): 3 dashed chunks passed at 3.7%.
func TestNameGateMixedCaseChunks(t *testing.T) {
	if raceEnabled {
		t.Skip("80 thousand gate calls of pure logic are slow under the race detector")
	}
	letters := lowerLetters + upperLetters
	for i, shape := range []struct{ size, groups int }{{4, 3}, {4, 4}, {3, 4}, {3, 5}} {
		rate := shapeRate(uint64(200+i), func(r *rand.Rand) string {
			parts := make([]string, shape.groups)
			for j := range parts {
				parts[j] = randomFrom(r, letters, shape.size)
			}
			return strings.Join(parts, "-")
		})
		if rate > 1 {
			t.Errorf("%d mixed-case chunks of %d pass %.2f%%, over 1%%", shape.groups, shape.size, rate)
		}
	}
	for _, s := range []string{"xKQL-pMWZ-tNRB-vHJD-qXZF-wBKM", "xlHA-kzHB-Mxou", "aKGC-ulZY-jiFM", "kQZW-aXBN-mTRL"} {
		if DefaultNameGate(s) {
			t.Errorf("passed mixed-case chunks %q", s)
		}
	}
	for _, s := range []string{"iOS", "tvOS", "WiFi", "iPad", "iPod", "macOS", "iOS-WiFi-Xcode", "WiFiAgent"} {
		if !DefaultNameGate(s) {
			t.Errorf("refused real name %q", s)
		}
	}
}

func TestTheGatePassesCompoundWordsWithFourConsonants(t *testing.T) {
	for _, s := range []string{"TelephonySubscriber", "CloudKitSubscriber", "Postgres", "Workflow", "WorkflowKit",
		"Lightweight", "Instrumentation", "Brightness", "Lightning", "Insights", "Bootstrap", "WiFi", "iOS",
		"UARPKit", "IOUSBHost"} {
		if !DefaultNameGate(s) {
			t.Errorf("refused real name %q", s)
		}
	}
	for _, s := range []string{"Xkrtplmbqa", "Abcdfghjkl", "Qwrtpsdfgh"} {
		if DefaultNameGate(s) {
			t.Errorf("passed unsayable letters %q", s)
		}
	}
}

// shapeRate is the share in percent of seeded draws of one shape that the gate passes.
func shapeRate(seed uint64, shape func(r *rand.Rand) string) float64 {
	r := rand.New(rand.NewPCG(seed, 10))
	passed := 0
	for range fuzzDraws {
		if DefaultNameGate(shape(r)) {
			passed++
		}
	}
	return 100 * float64(passed) / fuzzDraws
}

// Words beside random letters must not offset them (CM-00010): before, session-<12 letters> passed at 24%.
func TestNameGateRandomLettersBesideWords(t *testing.T) {
	if raceEnabled {
		t.Skip("400 thousand gate calls of pure logic are slow under the race detector")
	}
	shapes := []struct {
		name string
		wrap func(random string) string
	}{
		{"session-X", func(x string) string { return "session-" + x }},
		{"sessionX", func(x string) string { return "session" + x }},
		{"Xhelper", func(x string) string { return x + "helper" }},
		{"X-service", func(x string) string { return x + "-service" }},
		{"auth-token-X", func(x string) string { return "auth-token-" + x }},
		{"com.apple.X", func(x string) string { return "com.apple." + x }},
		{"com.docker.X.helper", func(x string) string { return "com.docker." + x + ".helper" }},
		{"web-X_service_1", func(x string) string { return "web-" + x + "_service_1" }},
		{"worker-X-manager-node", func(x string) string { return "worker-" + x + "-manager-node" }},
	}
	limits := map[int]float64{12: 7, 16: 2}
	for _, shape := range shapes {
		for n, limit := range limits {
			rate := shapeRate(uint64(n), func(r *rand.Rand) string { return shape.wrap(randomFrom(r, lowerLetters, n)) })
			if rate > limit {
				t.Errorf("%s with %d random letters passes %.2f%%, over %.2f%%", shape.name, n, rate, limit)
			}
		}
	}
}

// One capital or one digit among random letters used to leave the part unscored (CM-00010).
func TestNameGateRandomLettersWithOneCapitalOrDigit(t *testing.T) {
	if raceEnabled {
		t.Skip("300 thousand gate calls of pure logic are slow under the race detector")
	}
	shapes := []struct {
		name string
		make func(r *rand.Rand, n int) string
	}{
		{"capital first", func(r *rand.Rand, n int) string {
			return randomFrom(r, upperLetters, 1) + randomFrom(r, lowerLetters, n-1)
		}},
		{"capital in the middle", func(r *rand.Rand, n int) string {
			return randomFrom(r, lowerLetters, n/2) + randomFrom(r, upperLetters, 1) + randomFrom(r, lowerLetters, n-n/2-1)
		}},
		{"digit in the middle", func(r *rand.Rand, n int) string {
			return randomFrom(r, lowerLetters, n/2) + randomFrom(r, digitChars, 1) + randomFrom(r, lowerLetters, n-n/2-1)
		}},
		{"capital anywhere", func(r *rand.Rand, n int) string {
			b := []byte(randomFrom(r, lowerLetters, n))
			b[r.IntN(n)] = upperLetters[r.IntN(26)]
			return string(b)
		}},
		{"Session and a capitalised word", func(r *rand.Rand, n int) string {
			return "Session" + randomFrom(r, upperLetters, 1) + randomFrom(r, lowerLetters, n-1)
		}},
	}
	limits := map[int]float64{12: 5.5, 16: 1.5}
	for _, shape := range shapes {
		for n, limit := range limits {
			rate := shapeRate(uint64(n), func(r *rand.Rand) string { return shape.make(r, n) })
			if rate > limit {
				t.Errorf("%d letters with a %s pass %.2f%%, over %.2f%%", n, shape.name, rate, limit)
			}
		}
	}
}

func TestNameGateRefusesDashedAndUnderscoredAddresses(t *testing.T) {
	for _, s := range []string{"ip-10-0-0-1", "ip-172-31-5-9.local", "10-0-0-1", "host_192_168_1_20", "ec2-54-12-9-201",
		"node-10-1-2-3-web", "192_168_0_1", "10-0_0-1", "10_0-0_1", "ip_10_0_0_1", "ip-3-12-1-0", "host-1-2-3-4",
		"x192-168-1-1"} {
		if DefaultNameGate(s) {
			t.Errorf("passed address %q", s)
		}
	}
	for _, s := range []string{"2024-01-15", "python3-12-1", "node-v20-11-0", "1-2-3-4-5", "app_1_2_3", "x86_64-linux-gnu",
		"web-services_php84_1_fpm", "python-3-12-1-0", "v1-2-3-4", "a-1-2-3-4", "1-2-3-4"} {
		if !DefaultNameGate(s) {
			t.Errorf("refused %q, which is no address", s)
		}
	}
}

func TestNameGatePassesFourPartVersions(t *testing.T) {
	for _, s := range []string{"142.0.7444.59", "msedge-142.0.7444.176", "Chromium 142.0.7444.59",
		"chrome_131.0.6778.205", "Google Chrome 142.0.7444.175", "131.0.2903.112"} {
		if !DefaultNameGate(s) {
			t.Errorf("refused version %q", s)
		}
	}
	for _, s := range []string{"555.123.4567", "1.555.123.4567", "12.34.5678.9012", "1234.5678.9012.3456",
		"123-45-6789", "555-123-4567", "142.0.7444.59.123456", "4111-1111-1111-1111_1.1.1000.1",
		"868-54-3360_1.1.1000.1", "555.0123.4567.89", "app-1.2345.3.4-555-123-4567", "095.46814.18650.709",
		"4111111111_1.1.1000.1", "1.2000.3.4-123-45-6789", "pw-12345678-12345678_1.1.1000.1"} {
		if DefaultNameGate(s) {
			t.Errorf("passed digit groups %q", s)
		}
	}
}

// The floors and limits in this package were measured against this table; a changed table needs them measured again.
func TestLetterPairTableIsPinned(t *testing.T) {
	const want = "cd60e809a87bee9c870889136b2a122439767a1ed7a45ca5de9749c3eb7b5faf"
	for i, row := range letterPairs {
		if len(row) != 26 || strings.Trim(row, "0123456789") != "" {
			t.Fatalf("row %d is not 26 digits", i)
		}
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(letterPairs[:], "\n")))); got != want {
		t.Errorf("letterPairs hash is %s, want %s", got, want)
	}
}

// Synthetic values in the formats of real keys, assembled at run time; none may ever pass.
func TestNameGateRefusesEveryKeyFormat(t *testing.T) {
	if raceEnabled {
		t.Skip("180 thousand gate calls of pure logic are slow under the race detector")
	}
	url64 := alnum + "-_"
	formats := []struct {
		name  string
		start string
		chars string
		n     int
	}{
		{"anthropic", fake("sk", "-ant-", "api03-"), url64, 80}, {"openai project", fake("sk", "-proj-"), url64, 48},
		{"openai", fake("sk", "-"), alnum, 48}, {"stripe live", fake("sk", "_live_"), alnum, 24},
		{"stripe test", fake("sk", "_test_"), alnum, 24}, {"stripe publishable", fake("pk", "_live_"), alnum, 24},
		{"stripe restricted", fake("rk", "_live_"), alnum, 24}, {"github personal", fake("gh", "p_"), alnum, 36},
		{"github oauth", fake("gh", "o_"), alnum, 36}, {"github server", fake("gh", "s_"), alnum, 36},
		{"github fine-grained", fake("github", "_pat_"), alnum + "_", 60},
		{"slack bot", fake("xo", "xb-"), "0123456789-", 40}, {"slack user", fake("xo", "xp-"), alnum + "-", 40},
		{"aws access", fake("AK", "IA"), upperLetters + digitChars, 16},
		{"aws temporary", fake("AS", "IA"), upperLetters + digitChars, 16}, {"google", fake("AI", "za"), url64, 35},
		{"gitlab", fake("gl", "pat-"), url64, 20}, {"npm", fake("np", "m_"), alnum, 36},
		{"hugging face", fake("h", "f_"), alnum, 34}, {"sendgrid", fake("S", "G."), url64, 22},
		{"shopify", fake("shp", "at_"), hexChars, 32}, {"digitalocean", fake("do", "p_v1_"), hexChars, 64},
		{"linear", fake("lin", "_api_"), alnum, 40}, {"notion", fake("sec", "ret_"), alnum, 43},
		{"jwt", fake("ey", "J"), url64, 40}, {"pypi", fake("py", "pi-"), url64, 40},
		{"google oauth", fake("ya", "29."), url64, 40}, {"twilio", fake("S", "K"), hexChars, 32},
		{"hex 32", "", hexChars, 32}, {"hex 40", "", hexChars, 40}, {"hex 64", "", hexChars, 64},
		{"base64url 32", "", url64, 32}, {"base62 40", "", alnum, 40}, {"base32 32", "", upperLetters + "234567", 32},
	}
	r := rand.New(rand.NewPCG(20261113, 10))
	for _, f := range formats {
		passed, example := 0, ""
		for range 5000 {
			if s := f.start + randomFrom(r, f.chars, f.n); DefaultNameGate(s) {
				passed, example = passed+1, s
			}
		}
		if passed > 0 {
			t.Errorf("%s: %d of 5000 synthetic keys pass, such as %q", f.name, passed, example)
		}
	}
	uuid := func() string {
		return strings.Join([]string{randomFrom(r, hexChars, 8), randomFrom(r, hexChars, 4), randomFrom(r, hexChars, 4),
			randomFrom(r, hexChars, 4), randomFrom(r, hexChars, 12)}, "-")
	}
	for range 5000 {
		if s := uuid(); DefaultNameGate(s) || DefaultNameGate("session-"+s) {
			t.Fatalf("passed synthetic UUID %q, bare or after a word", s)
		}
	}
}

func TestLooksSecretTakesNamesPastTheLengthLimit(t *testing.T) {
	long := strings.Repeat("ab-", 60) + strings.Repeat("x", 200)
	if !looksSecret(long) {
		t.Errorf("a %d character run of letters was not taken for a secret", len(long))
	}
	// 21 sayable words hold 105 letter pairs, more than the 64 the scorer keeps; nothing else stops them first.
	words := strings.Repeat("banana-", 20) + "banana"
	if looksSecret(words) {
		t.Errorf("looksSecret stopped %d characters of sayable words; only the length limit should", len(words))
	}
	if DefaultNameGate(words) {
		t.Errorf("passed a name of %d characters", len(words))
	}
}

func BenchmarkDefaultNameGate(b *testing.B) {
	r := rand.New(rand.NewPCG(7, 7))
	names := []string{"Google Chrome Helper (Renderer)", "com.apple.WebKit.WebContent", "web-services_php84_1_fpm",
		"mysqldump", "syncdefaultsd", "WiFiMacSettingsIntents", "database_service", "node", "142.0.7444.59"}
	for range 9 {
		names = append(names, "session-"+randomFrom(r, lowerLetters, 16), randomFrom(r, alnum, 24))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DefaultNameGate(names[i%len(names)])
	}
}
