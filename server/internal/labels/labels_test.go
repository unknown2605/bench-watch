package labels

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestBashFamily(t *testing.T) {
	cases := [][2]string{
		{"git status", "git"}, {"cd /secret/path && git log", "git"}, {"SECRET_TOKEN=abc123 curl https://x", "curl"},
		{`API_KEY="a b c" node run.js`, "node"}, {"sudo -n docker ps", "docker"}, {"env FOO=bar python3 x.py", "python3"},
		{"timeout 30 npm test", "npm"}, {"/usr/bin/grep foo", "grep"}, {"# note\nls -la", "ls"}, {"cd /tmp", "cd"},
		{"for f in *; do echo; done", "shell loop"}, {"./my-private-script.sh --key=hunter2", "script"},
		{"supersecretbinary --flag", "other"}, {`"$(printf hello)" x`, "other"}, {"", "other"},
	}
	for _, c := range cases {
		if got := BashFamily(c[0]); got != "Bash: "+c[1] {
			t.Errorf("%q: %q", c[0], got)
		}
	}
	if BashFamily(nil) != "Bash: other" {
		t.Error("a missing command is other")
	}
}

func TestHugeCommandIsCapped(t *testing.T) {
	cases := [][2]string{
		{`git commit -m "` + strings.Repeat("x", 5_000_000) + `"`, "Bash: git"},
		{"sudo " + strings.Repeat("-a ", 3_000_000) + "git", "Bash: other"},
		{"timeout " + strings.Repeat("-a ", 3_000_000) + "5 git", "Bash: other"},
		{strings.Repeat(";", 5_000_000), "Bash: other"},
	}
	for i, c := range cases {
		if got := BashFamily(c[0]); got != c[1] {
			t.Errorf("case %d: %q", i, got)
		}
	}
}

func TestReadAndToolLabels(t *testing.T) {
	read := func(input map[string]any) string { return ResultLabel("Read", input) }
	if read(map[string]any{"file_path": "/x"}) != "Read: whole" ||
		read(map[string]any{"file_path": "/x", "offset": 5.0}) != "Read: ranged" ||
		read(map[string]any{"file_path": "/x", "limit": 5.0}) != "Read: ranged" ||
		ResultLabel("Grep", map[string]any{"pattern": "password=abc"}) != "Grep" {
		t.Fatal("Read must split whole and ranged; other tools pass by name")
	}
	if ToolLabel("mcp__9236dc32-1297-47d5-9400-7aba6c7fe479__get_metadata") != "mcp__connector__get_metadata" {
		t.Error("a connector UUID must become connector")
	}
	long := "mcp__plugin_playwright_playwright__browser_run_code_unsafe"
	if ToolLabel(long) != long {
		t.Error(long)
	}
	for _, name := range []string{"Edit", "Agent", "SubagentHandback", "bash", "TodoWrite"} {
		if ToolLabel(name) != name {
			t.Error(name)
		}
	}
	rejected := []any{"<img src=x onerror=alert(1)>", "NotABuiltInTool", "hunter2", "mcp__only_two", "mcp__a__b c",
		"mcp__a.b__c", "mcp__a__" + strings.Repeat("x", 300), "mcp__AbCdEf123456GhIjKl789012MnOp__tool",
		"mcp__glpat-abc__tool", "constructor",
		"__proto__", 42.0, nil}
	for _, name := range rejected {
		if ToolLabel(name) != "other" {
			t.Errorf("%v must be other", name)
		}
	}
}

func TestModelLabels(t *testing.T) {
	for _, m := range []string{"claude-opus-5-5", "claude-haiku-4-5-20251001", "claude-sonnet-5", "claude-opus-5-5[1m]",
		"claude-sonnet-4-6", "claude-opus-5", "claude-haiku-5-5", "claude-3-5-sonnet-20241022", "claude-3-opus-20240229",
		"claude-opus-4-1-20250805", "claude-sonnet-4-5[1m]", "claude-opus-4-5-20251101[1m]",
		"claude-3-5-sonnet-20241022[1m]"} {
		if ModelLabel(m) != m {
			t.Error(m)
		}
	}
	keyWord := strings.Join([]string{"gl", "pat"}, "")
	rejected := []any{"<script>", "gpt-5", "CLAUDE-OPUS", "claude-", "claude-a1b2c3d4e5f6g7h8i9j0k1l2m3",
		"claude-sk-ant-x", "claude-" + strings.Repeat("a", 60), 7.0, "claude-" + keyWord + "-5",
		"claude-3-" + keyWord + "-20241022",
		"claude-deadbeefdeadbeef-5", "claude-3-deadbeefdeadbeef", "claude-opus", "claude-abcdef0123456789abcd",
		"claude-word-word-word", "claude-opus-5-abc", "claude-opus-5[token]", "claude-opus.5"}
	for _, m := range rejected {
		if ModelLabel(m) != "unknown" {
			t.Errorf("%v must be unknown", m)
		}
	}
}

func TestAgentLabels(t *testing.T) {
	for _, a := range []string{"Explore", "general-purpose", "qa-tester", "plugin:code-reviewer", "claude-code-guide"} {
		if AgentLabel(a) != a {
			t.Error(a)
		}
	}
	for _, a := range []any{"Canary7Agent9Type4", "abc123def456", "sk-ant-abc", "9236dc32-1297-47d5-9400-7aba6c7fe479",
		"has space", "", 5.0} {
		if AgentLabel(a) != "unknown" {
			t.Errorf("%v must be unknown", a)
		}
	}
}

func TestLooksSecret(t *testing.T) {
	secrets := []string{"sk-ant-x", "sk_live_abc", "pk_test_abc", "rk_live_x", "github_pat_x", "glpat-x", "xoxb-1",
		"ya29.x", "shpat_x", "pre-a1b2c3d4e5f6a7b8c9d0e1f2-post", "AbCdEfGhIjKlMnOpQrStUvWxYz", "deadbeefdeadbeef",
		"123456789012", "a1b2c3-d4e5f6-a7b8c9",
		"aB3dE6:gH9jK2.mN5pQ8", strings.Repeat("q", 33), "v2api2024x", "project2025v2", "abcdef12", "decade2024", "xQ3vBn7k",
		"aB3dEfGh", "K8sAbCdE", "qwrtp3kjh", "abcde" + strings.Repeat("f", 30) + "1", strings.Repeat("a", 24) + "1",
		"textimagetext2imagetextim"}
	for _, s := range secrets {
		if !LooksSecret(s) {
			t.Errorf("%q must look secret", s)
		}
	}
	fine := []string{"www-ab-shop-alpha", "www-ab-kiwi7-zest", "www-biz-acmex-sg", "desk-app",
		"mcp__figma__get_design_context", "abcdefghijklmnopqrstuvwxyzabcdef", "claude-haiku-4-5-20251001",
		"www-apps-sample-foo1-ab-foo1-ab-sample-app",
		"mcp__Claude_Browser__tabs_context", "getDesignContext", "parseHTTPResponse", "shopify2024", "plugin:code-reviewer",
		strings.Repeat("q", 32), "textimagetext2imagetexti"}
	for _, s := range fine {
		if LooksSecret(s) {
			t.Errorf("%q must not look secret", s)
		}
	}
}

const home = "-Users-a-b"

func gatesPassing(s string) []string {
	var passing []string
	if strings.Contains(ToolLabel("mcp__"+s+"__get"), s) {
		passing = append(passing, "mcp server")
	}
	if strings.Contains(ToolLabel("mcp__srv__"+s), s) {
		passing = append(passing, "mcp tool")
	}
	if ModelLabel("claude-"+s) != "unknown" || ModelLabel("claude-"+strings.ToLower(s)) != "unknown" {
		passing = append(passing, "model")
	}
	if AgentLabel(s) == s {
		passing = append(passing, "agent")
	}
	if strings.Contains(ProjectLabel(home+"-workspace-"+s, home), s) {
		passing = append(passing, "project")
	}
	return passing
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomUUID() string {
	h := randomHex(16)
	return h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

func TestKeyPrefixesFailEveryGate(t *testing.T) {
	join := func(parts ...string) string { return strings.Join(parts, "") }
	tail := "abcdefghijkl"
	values := map[string]string{
		"github": join("gh", "p_") + tail, "google": join("AI", "za") + tail, "jwt": join("ey", "J") + tail,
		"npm": join("np", "m_") + tail, "huggingFace": join("h", "f_") + tail,
	}
	realistic := map[string]string{
		"github": join("gh", "p_") + randomHex(18), "google": join("AI", "za") + randomURLSafe(26),
		"jwt": join("ey", "J") + randomURLSafe(24),
		"npm": join("np", "m_") + randomHex(18), "huggingFace": join("h", "f_") + randomHex(17),
	}
	for name, v := range values {
		if !LooksSecret(v) || len(gatesPassing(v)) != 0 {
			t.Errorf("%s prefix must fail every gate", name)
		}
	}
	for name, v := range realistic {
		if len(gatesPassing(v)) != 0 {
			t.Errorf("%s in its real length must fail every gate", name)
		}
	}
}

func TestRandomTokensRarelyPass(t *testing.T) {
	const n = 5000
	generators := map[string]func() string{
		"urlsafe22": func() string { return randomURLSafe(17)[:22] }, "urlsafe43": func() string { return randomURLSafe(32) },
		"hex20": func() string { return randomHex(10) }, "hex40": func() string { return randomHex(20) },
		"upperUuid": func() string { return strings.ToUpper(randomUUID()) }, "lowerUuid": randomUUID,
	}
	for name, make := range generators {
		counts := map[string]int{}
		for range n {
			for _, gate := range gatesPassing(make()) {
				counts[gate]++
			}
		}
		for gate, count := range counts {
			if float64(count)/n >= 0.01 {
				t.Errorf("%s passes %s %.2f%%", name, gate, 100*float64(count)/n)
			}
		}
	}
}

func TestPartsAndUUIDs(t *testing.T) {
	upper := strings.ToUpper(randomUUID())
	if ToolLabel("mcp__"+upper+"__get_metadata") != "mcp__connector__get_metadata" || !LooksSecret(upper) {
		t.Fatal("an upper-case UUID must become connector and look secret")
	}
	lower := "qwxzkjhgfdsamnbvcxzlkjhgfdsapoiuytrewqaz"
	for _, v := range []string{lower, strings.ToUpper(lower)} {
		if !LooksSecret(v) || len(gatesPassing(v)) != 0 {
			t.Errorf("a 40-letter run must fail every gate")
		}
	}
}

func TestOrdinaryNamesWithNumbers(t *testing.T) {
	for _, name := range []string{"s3bucket", "text2image", "getS3Object", "oauth2client", "web3auth", "base64url",
		"sha256sum", "magento2store", "gpt4turbo", "mp4convert", "utf8decode", "i18nextconfig", "MainProject2024",
		"react18app", "shopify2024",
		"www-ab-kiwi7-zest"} {
		tool := "mcp__srv__" + strings.ReplaceAll(name, "-", "_")
		if LooksSecret(name) || AgentLabel(name) != name || ProjectLabel(home+"-workspace-"+name, home) != name ||
			ToolLabel(tool) != tool {
			t.Errorf("%s must pass", name)
		}
	}
	for _, name := range []string{"s3bucket", "text2image", "getS3Object", "oauth2client"} {
		if got := fmt.Sprint(gatesPassing(name)); got != "[mcp server mcp tool agent project]" {
			t.Errorf("%s passes %s", name, got)
		}
	}
}

// seededPick is the Node suite's generator, so both sides draw the same tokens.
func seededPick(seed int32) func(int) int {
	state := seed
	return func(count int) int {
		state += 0x6d2b79f5
		s := uint32(state)
		mixed := int32(uint32(int32(s^(s>>15))) * uint32(1|state))
		m := uint32(mixed)
		mixed = int32(uint32(mixed+int32(uint32(int32(m^(m>>7)))*uint32(61|mixed)))) ^ mixed
		m = uint32(mixed)
		return int(math.Floor(float64(m^(m>>14)) / 4294967296 * float64(count)))
	}
}

const fuzzDraws = 20000

type shape struct {
	name string
	make func(func(int) int) string
}

func fuzzCounts(shapes []shape, hit func(string) bool) map[string]int {
	out := map[string]int{}
	for i, s := range shapes {
		pick := seededPick(int32(i + 1))
		for range fuzzDraws {
			if hit(s.make(pick)) {
				out[s.name]++
			}
		}
	}
	return out
}

const (
	lowerSet  = "abcdefghijklmnopqrstuvwxyz"
	upperSet  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digitSet  = "0123456789"
	urlSafe   = lowerSet + upperSet + digitSet + "-_"
	hexSet    = digitSet + "abcdef"
	base62Set = lowerSet + upperSet + digitSet
	base36Set = lowerSet + digitSet
)

func drawn(alphabet string, length int) func(func(int) int) string {
	return func(pick func(int) int) string {
		b := make([]byte, length)
		for i := range b {
			b[i] = alphabet[pick(len(alphabet))]
		}
		return string(b)
	}
}

func sized(name, alphabet string, lengths ...int) []shape {
	var out []shape
	for _, l := range lengths {
		out = append(out, shape{fmt.Sprintf("%s%d", name, l), drawn(alphabet, l)})
	}
	return out
}

func admitted(v string) bool { return len(gatesPassing(v)) > 0 }

func checkCounts(t *testing.T, got map[string]int, node map[string]int, limit func(name string, pct float64) bool) {
	t.Helper()
	for name, want := range node {
		pct := 100 * float64(got[name]) / fuzzDraws
		t.Logf("%s admitted %.2f%% (Node %d of %d, Go %d)", name, pct, want, fuzzDraws, got[name])
		if got[name] != want {
			t.Errorf("%s: Go admits %d, Node %d", name, got[name], want)
		}
		if !limit(name, pct) {
			t.Errorf("%s admitted %.2f%%, past its recorded limit", name, pct)
		}
	}
}

func TestFuzzRealLengths(t *testing.T) {
	uuid := func(pick func(int) int) string {
		parts := make([]string, 0, 5)
		for _, l := range []int{8, 4, 4, 4, 12} {
			parts = append(parts, drawn(hexSet, l)(pick))
		}
		return strings.Join(parts, "-")
	}
	var shapes []shape
	shapes = append(shapes, sized("urlsafe", urlSafe, 20, 22, 32, 43)...)
	shapes = append(shapes, sized("base62_", base62Set, 16, 20, 24, 32, 43)...)
	shapes = append(shapes, sized("base36_", base36Set, 20, 24, 32)...)
	shapes = append(shapes, sized("hex", hexSet, 16, 20, 32, 40)...)
	shapes = append(shapes, sized("lower", lowerSet, 33, 40)...)
	shapes = append(shapes, sized("upper", upperSet, 33, 40)...)
	shapes = append(shapes, shape{"uuid", uuid}, shape{"upperUuid",
		func(p func(int) int) string { return strings.ToUpper(uuid(p)) }})
	node := map[string]int{"urlsafe20": 122, "urlsafe22": 57, "urlsafe32": 1, "urlsafe43": 0, "base62_16": 64,
		"base62_20": 9, "base62_24": 0, "base62_32": 0, "base62_43": 0, "base36_20": 65, "base36_24": 9, "base36_32": 0,
		"hex16": 0, "hex20": 0,
		"hex32": 0, "hex40": 0, "lower33": 0, "lower40": 0, "upper33": 0, "upper40": 0, "uuid": 0, "upperUuid": 0}
	checkCounts(t, fuzzCounts(shapes, admitted), node, func(_ string, pct float64) bool { return pct <= 1 })
}

func TestFuzzShortTokens(t *testing.T) {
	limits := map[string]float64{"urlsafe12": 8.5, "urlsafe16": 2.8, "base36_12": 7, "base36_16": 1.8, "base62_12": 2}
	var shapes []shape
	shapes = append(shapes, sized("urlsafe", urlSafe, 12, 16)...)
	shapes = append(shapes, sized("base36_", base36Set, 12, 16)...)
	shapes = append(shapes, sized("base62_", base62Set, 12)...)
	node := map[string]int{"urlsafe12": 1488, "urlsafe16": 392, "base36_12": 1183, "base36_16": 248, "base62_12": 299}
	checkCounts(t, fuzzCounts(shapes, admitted), node, func(n string, pct float64) bool { return pct <= limits[n] })
}

func TestFuzzModelShapedTokens(t *testing.T) {
	limits := map[string]float64{"base36_12": 2.5, "base36_16": 0.8}
	isModel := func(token string) bool { return ModelLabel("claude-"+token+"-5") != "unknown" }
	node := map[string]int{"base36_12": 382, "base36_16": 122}
	checkCounts(t, fuzzCounts(sized("base36_", base36Set, 12, 16), isModel), node,
		func(n string, pct float64) bool { return pct <= limits[n] })
}

var words = strings.Fields(`api app auth base bucket build cache cart check client cloud code config context core
data debug deploy design dev docs editor email event file filter flow front gateway get guard hook image index job
key label list local log mail main map media merge model monitor node object order page parse plugin post postgres
price proxy queue react read redis report review route schema script search send server service shop shopify site
sync table task test text theme tool user view web worker write`)

func TestFuzzOrdinaryNames(t *testing.T) {
	word := func(p func(int) int) string { return words[p(len(words))] }
	capital := func(w string) string { return strings.ToUpper(w[:1]) + w[1:] }
	number := func(p func(int) int) string {
		return []string{"2", "3", "4", "5", "8", "10", "18", "64", "256", "2024"}[p(10)]
	}
	shapes := []shape{
		{"kebab", func(p func(int) int) string { return word(p) + "-" + word(p) + "-" + word(p) }},
		{"snake", func(p func(int) int) string { return word(p) + "_" + word(p) + "_" + word(p) }},
		{"plugin", func(p func(int) int) string { return word(p) + ":" + word(p) + "-" + word(p) }},
		{"camel", func(p func(int) int) string { return word(p) + capital(word(p)) + capital(word(p)) }},
		{"wordNumberWord", func(p func(int) int) string { return word(p) + number(p) + word(p) }},
		{"letterNumberWord", func(p func(int) int) string { return string(lowerSet[p(26)]) + number(p) + word(p) }},
		{"camelNumber",
			func(p func(int) int) string { return word(p) + string(upperSet[p(26)]) + number(p) + capital(word(p)) }},
		{"wordYear", func(p func(int) int) string { return word(p) + word(p) + number(p) }},
	}
	required := map[string]float64{"kebab": 100, "snake": 100, "plugin": 100, "camel": 100, "wordNumberWord": 85,
		"letterNumberWord": 90, "camelNumber": 85, "wordYear": 85}
	node := map[string]int{"kebab": 20000, "snake": 20000, "plugin": 20000, "camel": 20000, "wordNumberWord": 19529,
		"letterNumberWord": 19789, "camelNumber": 19352, "wordYear": 18288}
	got := fuzzCounts(shapes, func(v string) bool { return !LooksSecret(v) })
	checkCounts(t, got, node, func(n string, pct float64) bool { return pct >= required[n] })
}

func TestProjectLabels(t *testing.T) {
	cases := [][2]string{
		{"-Users-a-b-workspace-www-tag-foo", "www-tag-foo"}, {"-Users-a-b", "home"},
		{"-private-tmp-claude-501-x", "scratch (tmp)"},
		{"-Users-a-b-workspace-www-foo--claude-worktrees-bar", "www-foo"}, {"-Users-a-b-Documents-notes", "Documents-notes"},
		{"-Users-someone-else-private", "other"}, {"-opt-client-secret-project", "other"},
		{"-Users-a-bc-workspace-x", "other"},
		{"-Users-a-b-workspace-AbCdEf123456GhIjKl789012MnOp", "other"}, {"-Users-a-b-workspace-glpat-abc", "other"},
	}
	for _, c := range cases {
		if got := ProjectLabel(c[0], home); got != c[1] {
			t.Errorf("%s: %q", c[0], got)
		}
	}
}

func TestErrorClasses(t *testing.T) {
	cases := []struct {
		denied     bool
		text, want string
	}{
		{true, "x", "denied"},
		{false, "The user doesn't want to proceed with this tool use. The tool use was rejected", "denied"},
		{false, "Permission to use Bash has been denied.", "denied"},
		{false, "Permission for this action has been denied. Reason: x", "denied"},
		{true, "PreToolUse:Bash hook error: [command hook 1]: blocked", "blocked by hook"},
		{true, "<tool_use_error>PreToolUse:Edit hook error: blocked</tool_use_error>", "blocked by hook"},
		{false, "Exit code 2\nsecret output", "non-zero exit"},
		{false, "Exit code 1\nbash: /x: Permission denied", "non-zero exit"},
		{false, "Exit code 1\nthe pre-commit hook failed", "non-zero exit"},
		{false, "Exit code 7\ncurl: connection timeout", "non-zero exit"},
		{false, "Exit code 1\nThe user doesn't want this file", "non-zero exit"},
		{false, "Exit code 127\nsh: x: command not found", "non-zero exit"},
		{false, "Exit code 143\nCommand timed out after 2m", "timeout"}, {false, "bash: /x: Permission denied", "other"},
		{false, "a hook of no interest", "other"}, {false, "Request timed out", "timeout"},
		{false, "<tool_use_error>InputValidationError: timeout must be a number</tool_use_error>", "other"},
		{false, "<tool_use_error>String to replace not found in file.</tool_use_error>", "not found"},
		{false, "<tool_use_error>File does not exist.</tool_use_error>", "not found"},
		{false, "File does not exist.", "not found"},
		{false, "something secret", "other"}, {false, "Connection timeout while reading", "timeout"},
		{false, "Operation TIMED OUT", "timeout"}, {false, "TimeOut", "timeout"},
	}
	for _, c := range cases {
		if got := ErrorClass(c.denied, c.text); got != c.want {
			t.Errorf("%q: %q", c.text, got)
		}
	}
}

// The porting risks: JavaScript's \s set, UTF-16 lengths and slices, and the gate's 300-unit head.
func TestJavaScriptStringRules(t *testing.T) {
	for _, r := range []rune{0xa0, 0x2028, 0x2029, 0xfeff, 0x1680, 0x2000, 0x200a, 0x202f, 0x205f, 0x3000, '\v'} {
		if !JSSpace(r) {
			t.Errorf("U+%04X is JavaScript whitespace", r)
		}
	}
	for _, r := range []rune{0x85, 0x200b, 0x180e} {
		if JSSpace(r) {
			t.Errorf("U+%04X is not JavaScript whitespace", r)
		}
	}
	nbsp := string(rune(0xa0))
	if BashFamily("sudo"+nbsp+"git status") != "Bash: git" {
		t.Error("NBSP must split words as JavaScript's \\s does")
	}
	if BashFamily("sudo"+string(rune(0x85))+"git") == "Bash: git" {
		t.Error("U+0085 must not split words")
	}
	emoji := string(rune(0x1f600))
	if UTF16Len(emoji+"a") != 3 || UTF16Prefix(emoji+"a", 1) != string(rune(0xfffd)) ||
		UTF16Prefix(emoji+"a", 2) != emoji {
		t.Error("UTF-16 lengths and slices must count surrogate pairs as two units")
	}
	head := strings.Repeat(emoji, 150) + "Exit code 1"
	if ErrorClass(false, strings.Repeat("x", 299)+"timeout") == "timeout" || ErrorClass(false, head) != "other" {
		t.Error("the class must read only the first 300 UTF-16 units")
	}
	if ErrorClass(false, strings.Repeat(emoji, 140)+"timed out") != "timeout" {
		t.Error("280 units of emoji leave room for the phrase")
	}
	if ModelLabel("claude-"+strings.Repeat(emoji, 25)) != "unknown" {
		t.Error("the model gate counts UTF-16 units")
	}
	if BashFamily("sudo"+string(rune(0xfeff))+"git") != "Bash: git" {
		t.Error("BOM is JavaScript whitespace")
	}
	kelvin, longS := string(rune(0x212a)), string(rune(0x17f))
	node := map[string]bool{
		strings.Repeat("q", 32) + kelvin: true, strings.Repeat("q", 32) + longS: true, "qwrtp" + kelvin + "jh": false,
		"xyz" + kelvin + "abcdefgh": false, strings.Repeat("Q", 32) + kelvin: true, "aB3dEfG" + kelvin: true,
		"ab" + kelvin + "cd" + longS + "ef12": true,
	}
	for value, want := range node {
		if LooksSecret(value) != want {
			t.Errorf("%q: Node says %v", value, want)
		}
	}
}
