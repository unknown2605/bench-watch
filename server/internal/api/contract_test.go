package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const specPath = "../../../api/openapi.yaml"

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("load %s: %v", specPath, err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate %s: %v", specPath, err)
	}
	return doc
}

type schemaWalker struct {
	seen     map[*openapi3.Schema]bool
	problems []string
}

func (w *schemaWalker) walk(where string, ref *openapi3.SchemaRef) {
	if ref == nil || ref.Value == nil {
		w.problems = append(w.problems, where+": missing schema")
		return
	}
	if ref.Ref != "" {
		where = ref.Ref
	}
	s := ref.Value
	if w.seen[s] {
		return
	}
	w.seen[s] = true

	for i, sub := range s.OneOf {
		w.walk(fmt.Sprintf("%s/oneOf/%d", where, i), sub)
	}
	for i, sub := range s.AllOf {
		w.walk(fmt.Sprintf("%s/allOf/%d", where, i), sub)
	}
	for i, sub := range s.AnyOf {
		w.walk(fmt.Sprintf("%s/anyOf/%d", where, i), sub)
	}
	if s.Not != nil {
		w.problems = append(w.problems, where+": `not` is not allowed")
	}

	switch {
	case s.Type.Is(openapi3.TypeString):
		w.checkString(where, s)
	case s.Type.Is(openapi3.TypeObject):
		if s.AdditionalProperties.Has == nil || *s.AdditionalProperties.Has || s.AdditionalProperties.Schema != nil {
			w.problems = append(w.problems, where+": object must set additionalProperties: false")
		}
		names := make([]string, 0, len(s.Properties))
		for name := range s.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			w.walk(where+"/properties/"+name, s.Properties[name])
		}
		for _, name := range s.Required {
			if _, ok := s.Properties[name]; !ok {
				w.problems = append(w.problems, where+": required property "+name+" is not defined")
			}
		}
	case s.Type.Is(openapi3.TypeArray):
		w.walk(where+"/items", s.Items)
	case s.Type.Is(openapi3.TypeInteger), s.Type.Is(openapi3.TypeNumber), s.Type.Is(openapi3.TypeBoolean):
	case s.Type.IsEmpty() && len(s.OneOf)+len(s.AllOf)+len(s.AnyOf) > 0:
	default:
		w.problems = append(w.problems, fmt.Sprintf("%s: unsupported type %v", where, s.Type))
	}
}

func (w *schemaWalker) param(where string, p *openapi3.ParameterRef) {
	if p == nil || p.Value == nil {
		w.problems = append(w.problems, where+": missing parameter")
		return
	}
	if len(p.Value.Content) > 0 {
		w.problems = append(w.problems, where+" "+p.Value.Name+": content parameters are not allowed")
	}
	w.walk(where+" "+p.Value.Name, p.Value.Schema)
}

func (w *schemaWalker) header(where string, h *openapi3.HeaderRef) {
	if h == nil || h.Value == nil {
		w.problems = append(w.problems, where+": missing header")
		return
	}
	if len(h.Value.Content) > 0 {
		w.problems = append(w.problems, where+": content headers are not allowed")
	}
	w.walk(where, h.Value.Schema)
}

func (w *schemaWalker) response(where string, r *openapi3.ResponseRef) {
	if r == nil || r.Value == nil {
		w.problems = append(w.problems, where+": missing response")
		return
	}
	for name, h := range r.Value.Headers {
		w.header(where+" header "+name, h)
	}
	for mediaType, media := range r.Value.Content {
		if media == nil {
			w.problems = append(w.problems, where+" "+mediaType+": missing media type")
			continue
		}
		w.walk(where+" "+mediaType, media.Schema)
	}
}

func (w *schemaWalker) checkString(where string, s *openapi3.Schema) {
	switch {
	case len(s.Enum) > 0:
		for _, v := range s.Enum {
			if _, ok := v.(string); !ok {
				w.problems = append(w.problems, fmt.Sprintf("%s: enum value %v is not a string", where, v))
			}
		}
	case s.Format == "date" || s.Format == "date-time":
	case s.Extensions["x-cm-label"] != nil:
		if s.Pattern == "" || s.MaxLength == nil {
			w.problems = append(w.problems, where+": a label needs pattern and maxLength")
			return
		}
		if *s.MaxLength > maxLabelLength {
			w.problems = append(w.problems, fmt.Sprintf("%s: maxLength %d over %d", where, *s.MaxLength, maxLabelLength))
		}
		for _, p := range patternProblems(s.Pattern) {
			w.problems = append(w.problems, where+": "+p)
		}
	default:
		w.problems = append(w.problems, where+": free string (needs enum, date or date-time format, or x-cm-label)")
	}
}

const maxLabelLength = 128

// A label pattern may pass only a short, plain token: anchored at both ends, no top-level alternation, bounded
// repeats, and no any-character, slash, backslash, control or non-ASCII character.
func patternProblems(pattern string) []string {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return []string{"pattern does not parse: " + err.Error()}
	}
	var problems []string
	subs := re.Sub
	if re.Op != syntax.OpConcat || len(subs) < 3 || subs[0].Op != syntax.OpBeginText ||
		subs[len(subs)-1].Op != syntax.OpEndText {
		problems = append(problems, "pattern must be one concatenation from ^ to $ (wrap alternatives in (?:...))")
	}
	var visit func(*syntax.Regexp)
	visit = func(r *syntax.Regexp) {
		switch r.Op {
		case syntax.OpStar, syntax.OpPlus:
			problems = append(problems, "unbounded repeat "+r.String())
		case syntax.OpRepeat:
			if r.Max < 0 {
				problems = append(problems, "unbounded repeat "+r.String())
			}
		case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
			problems = append(problems, "any-character match")
		case syntax.OpBeginLine, syntax.OpEndLine:
			problems = append(problems, "line anchor (multi-line mode)")
		case syntax.OpBeginText, syntax.OpEndText:
			if r != subs[0] && r != subs[len(subs)-1] {
				problems = append(problems, "anchor inside the pattern")
			}
		case syntax.OpLiteral:
			for _, c := range r.Rune {
				if !plainRune(c) {
					problems = append(problems, fmt.Sprintf("character %q", c))
				}
			}
		case syntax.OpCharClass:
			for i := 0; i+1 < len(r.Rune); i += 2 {
				lo, hi := r.Rune[i], r.Rune[i+1]
				if lo < 0x20 || hi >= 0x7f || (lo <= '/' && '/' <= hi) || (lo <= '\\' && '\\' <= hi) {
					problems = append(problems, fmt.Sprintf("class %s holds a slash, backslash, control or non-ASCII character", r.String()))
				}
			}
		}
		for _, sub := range r.Sub {
			visit(sub)
		}
	}
	visit(re)
	return problems
}

func plainRune(c rune) bool { return c >= 0x20 && c < 0x7f && c != '/' && c != '\\' }

// maxMatchLength is the longest text the pattern can match, or -1 when it is unbounded.
func maxMatchLength(r *syntax.Regexp) int {
	switch r.Op {
	case syntax.OpLiteral:
		return len(r.Rune)
	case syntax.OpCharClass, syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return 1
	case syntax.OpCapture, syntax.OpQuest:
		return maxMatchLength(r.Sub[0])
	case syntax.OpRepeat:
		n := maxMatchLength(r.Sub[0])
		if r.Max < 0 || n < 0 {
			return -1
		}
		return n * r.Max
	case syntax.OpStar, syntax.OpPlus:
		return -1
	case syntax.OpConcat, syntax.OpAlternate:
		total := 0
		for _, sub := range r.Sub {
			n := maxMatchLength(sub)
			if n < 0 {
				return -1
			}
			if r.Op == syntax.OpConcat {
				total += n
			} else {
				total = max(total, n)
			}
		}
		return total
	}
	return 0
}

func TestPatternRulesCatchWeakPatterns(t *testing.T) {
	for _, p := range []string{
		`^a|b$`, `^[a-z]+$`, `^[a-z]{1,}$`, `^.{1,8}$`, `^[ -~]{1,8}$`, `^[a-z/]{1,8}$`, `^a\nb$`, `[a-z]{1,8}`,
		`^[a-z]{1,8}`, `(?m)^[a-z]{1,8}$`, `^[a-z]{1,8}$|^x$`,
	} {
		if len(patternProblems(p)) == 0 {
			t.Errorf("%s: accepted", p)
		}
	}
	for _, p := range []string{`^(?:[a-z]{1,8}|unknown)$`, `^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$`} {
		if probs := patternProblems(p); len(probs) > 0 {
			t.Errorf("%s: refused: %v", p, probs)
		}
	}
}

// The database CHECK functions use the spec's pattern literally; where the pattern can match more than
// maxLength characters, the function must also check the length.
var labelFunctions = map[string]string{
	"project": "is_project_label", "model": "is_model_label", "agentType": "is_agent_type_label",
	"tool": "is_tool_label", "session": "is_session_label", "hash": "is_hash16", "container": "is_container_label",
	"stack": "is_stack_label", "app": "is_display_label", "volume": "is_display_label",
	"interface": "is_interface_label", "filesystem": "is_filesystem_label", "guardRule": "is_guard_rule_label",
}

var labelFunctionSQL = regexp.MustCompile(`(?m)^CREATE (?:OR REPLACE )?FUNCTION (is_[a-z0-9_]+)\(v text\) RETURNS boolean LANGUAGE sql ` +
	`IMMUTABLE PARALLEL SAFE\n    RETURN (?:length\(v\) <= ([0-9]+) AND )?v ~ '([^']*)';$`)

func TestDatabaseChecksMatchTheSpec(t *testing.T) {
	doc := loadSpec(t)
	files, err := filepath.Glob("../../db/migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations not found: %v", err)
	}
	type fn struct {
		pattern string
		length  int
	}
	fns := map[string]fn{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(b), "-- +goose Down")
		for _, m := range labelFunctionSQL.FindAllStringSubmatch(up, -1) {
			n := -1
			if m[2] != "" {
				n, _ = strconv.Atoi(m[2])
			}
			fns[m[1]] = fn{pattern: m[3], length: n}
		}
	}
	for name, ref := range doc.Components.Schemas {
		label, _ := ref.Value.Extensions["x-cm-label"].(string)
		if label == "" || label == "errorCode" {
			continue
		}
		fname, ok := labelFunctions[label]
		if !ok {
			t.Errorf("%s: x-cm-label %q has no database function in this test", name, label)
			continue
		}
		f, ok := fns[fname]
		if !ok {
			t.Errorf("%s: %s not found in the migrations", name, fname)
			continue
		}
		if f.pattern != ref.Value.Pattern {
			t.Errorf("%s: %s pattern %q differs from the spec %q", name, fname, f.pattern, ref.Value.Pattern)
		}
		re, err := syntax.Parse(ref.Value.Pattern, syntax.Perl)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		maxLen := int(*ref.Value.MaxLength)
		if longest := maxMatchLength(re); longest < 0 || longest > maxLen {
			if f.length != maxLen {
				t.Errorf("%s: pattern matches up to %d characters, so %s must check length(v) <= %d", name, longest,
					fname, maxLen)
			}
		}
	}
}

// What the Go scanner emits when a value fails a gate (cmd/scan-dump/sentinels_test.go); each must pass both the
// spec and the database checks.
var scannerSentinels = map[string][]string{
	"ProjectLabel":   {"other", "home", "scratch (tmp)"},
	"ToolLabel":      {"other", "Read: whole", "Read: ranged", "Read: image", "Bash: other", "Bash: script", "Bash: cd", "Bash: shell loop", "Bash: git"},
	"ModelLabel":     {"unknown"},
	"AgentTypeLabel": {"unknown"},
	"SessionLabel":   {"unknown"},
}

func TestScannerSentinelsPass(t *testing.T) {
	doc := loadSpec(t)
	for name, values := range scannerSentinels {
		re := regexp.MustCompile(doc.Components.Schemas[name].Value.Pattern)
		for _, v := range values {
			if !re.MatchString(v) {
				t.Errorf("%s: sentinel %q is refused", name, v)
			}
		}
	}
	boundary := regexp.MustCompile(`^(?:[0-9a-f]{16}|[1-9][0-9]{8,12})$`)
	for _, v := range []string{"0123456789abcdef", "946684800", "1762560000", "4102444800"} {
		if !boundary.MatchString(v) {
			t.Errorf("boundary key %q is refused", v)
		}
	}
	b, err := os.ReadFile("../../db/migrations/00001_settings_and_partitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "RETURN v ~ '"+boundary.String()+"';") {
		t.Error("is_boundary_key no longer uses the pattern this test checks")
	}
}

// The Bash family allowlist (the retired Node scanner's FAMILIES, carried over unchanged); the ToolLabel pattern
// must list exactly these plus four sentinels.
const bashFamilies = `git gh docker docker-compose kubectl helm npm npx pnpm yarn bun node deno tsc tsx vitest jest
eslint biome prettier playwright python python3 pip pip3 uv uvx pytest ruff mypy ruby gem bundle php composer go gofmt
cargo rustc make cmake curl wget http ssh scp rsync sftp aws gcloud az sls serverless shopify terraform wrangler
cloudflared jq yq sed awk grep egrep rg find fd ls cat head tail less wc sort uniq cut tr tee xargs diff cmp comm stat
file du df ps top lsof netstat kill pkill pgrep nc dig ping mkdir rmdir rm cp mv touch chmod chown ln readlink realpath
basename dirname tar zip unzip gzip gunzip base64 shasum sha1sum sha256sum md5 md5sum openssl date sleep echo printf pwd
which whoami id uname hostname env test true false bash sh zsh open osascript launchctl sysctl vm_stat sw_vers defaults
plutil pbcopy pbpaste brew nvm pm2 nginx mysql mysqldump mariadb psql pg_dump redis-cli sqlite3 dphp dnode dsql dredis
stack-doctor magento-cloud artisan ffmpeg ffprobe convert magick sips identify exiftool strings xxd od hexdump nl seq
paste column bc expr yes wait type command perl gofumpt golangci-lint staticcheck govulncheck semgrep gitleaks trivy`

func TestBashFamiliesAreExactlyTheAllowlist(t *testing.T) {
	want := append(strings.Fields(bashFamilies), "cd", "other", "script", "shell loop")
	sort.Strings(want)
	pattern := loadSpec(t).Components.Schemas["ToolLabel"].Value.Pattern
	alt := regexp.MustCompile(`Bash: \(\?:([^)]*)\)`).FindStringSubmatch(pattern)
	if alt == nil {
		t.Fatal("ToolLabel pattern has no Bash: (?:...) group")
	}
	got := strings.Split(alt[1], "|")
	sort.Strings(got)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("ToolLabel Bash families differ from bashFamilies plus the four sentinels")
	}
	re := regexp.MustCompile(pattern)
	for _, f := range want {
		if !re.MatchString("Bash: " + f) {
			t.Errorf("Bash: %s refused", f)
		}
	}
	for _, v := range []string{"Bash: hunter2", "Bash: gitx", "Bash: xgit", "Bash: ", "Bash: git status", "Bash: Git"} {
		if re.MatchString(v) {
			t.Errorf("%q accepted: a Bash label must name an allowlisted family", v)
		}
	}
}

func TestEveryOperationIsGet(t *testing.T) {
	doc := loadSpec(t)
	for path, item := range doc.Paths.Map() {
		if !strings.HasPrefix(path, "/api/") {
			t.Errorf("%s: path outside /api/", path)
		}
		for method := range item.Operations() {
			if method != http.MethodGet {
				t.Errorf("%s %s: only GET is allowed", method, path)
			}
		}
		if item.Get == nil {
			t.Errorf("%s: no GET operation", path)
		}
	}
}

func TestEverySchemaFollowsThePrivacyRule(t *testing.T) {
	doc := loadSpec(t)
	w := &schemaWalker{seen: map[*openapi3.Schema]bool{}}

	names := make([]string, 0, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w.walk("#/components/schemas/"+name, doc.Components.Schemas[name])
	}
	for name, p := range doc.Components.Parameters {
		w.param("#/components/parameters/"+name, p)
	}
	for name, h := range doc.Components.Headers {
		w.header("#/components/headers/"+name, h)
	}
	for name, r := range doc.Components.Responses {
		w.response("#/components/responses/"+name, r)
	}
	if len(doc.Components.RequestBodies) > 0 {
		w.problems = append(w.problems, "components.requestBodies must be empty")
	}
	for path, item := range doc.Paths.Map() {
		for _, p := range item.Parameters {
			w.param(path+" path-item parameter", p)
		}
		for method, op := range item.Operations() {
			where := method + " " + path
			if op.RequestBody != nil {
				w.problems = append(w.problems, where+": no operation may take a request body")
			}
			for _, p := range op.Parameters {
				w.param(where+" parameter", p)
			}
			if op.Responses == nil {
				w.problems = append(w.problems, where+": no responses")
				continue
			}
			for code, resp := range op.Responses.Map() {
				w.response(where+" "+code, resp)
			}
		}
	}
	for _, p := range w.problems {
		t.Error(p)
	}
}

func TestResponsesAreJSONExceptLive(t *testing.T) {
	doc := loadSpec(t)
	for path, item := range doc.Paths.Map() {
		want := "application/json"
		if path == "/api/live" {
			want = "text/event-stream"
		}
		if item.Get == nil || item.Get.Responses == nil {
			t.Errorf("GET %s: no responses", path)
			continue
		}
		if item.Get.Responses.Default() == nil {
			t.Errorf("GET %s: no default ErrorResponse", path)
		}
		for code, resp := range item.Get.Responses.Map() {
			if resp == nil || resp.Value == nil {
				t.Errorf("GET %s %s: missing response", path, code)
				continue
			}
			if code != "200" {
				media := resp.Value.Content.Get("application/json")
				if len(resp.Value.Content) != 1 || media == nil || media.Schema == nil ||
					media.Schema.Ref != "#/components/schemas/ErrorResponse" {
					t.Errorf("GET %s %s: must be exactly an application/json ErrorResponse", path, code)
				}
			}
			got := make([]string, 0, len(resp.Value.Content))
			for mediaType := range resp.Value.Content {
				got = append(got, mediaType)
			}
			if code == "200" && (len(got) != 1 || got[0] != want) {
				t.Errorf("GET %s %s: content types %v, want [%s]", path, code, got, want)
			}
		}
		if item.Get.Responses.Status(http.StatusOK) == nil {
			t.Errorf("GET %s: no 200 response", path)
		}
	}
}

func TestLiveEventCoversEveryType(t *testing.T) {
	doc := loadSpec(t)
	live := doc.Components.Schemas["LiveEvent"]
	if live == nil || live.Value.Discriminator == nil || live.Value.Discriminator.PropertyName != "type" {
		t.Fatal("LiveEvent must be a oneOf with a discriminator on type")
	}
	if len(live.Value.Discriminator.Mapping) != len(live.Value.OneOf) {
		t.Errorf("discriminator maps %d types for %d variants",
			len(live.Value.Discriminator.Mapping), len(live.Value.OneOf))
	}
	for _, v := range live.Value.OneOf {
		if v == nil || v.Value == nil {
			t.Error("LiveEvent: missing variant")
			continue
		}
		typ := v.Value.Properties["type"]
		if typ == nil || typ.Value == nil || len(typ.Value.Enum) != 1 {
			t.Errorf("%s: type must be a single-value enum", v.Ref)
		}
	}
}

func TestLabelPatterns(t *testing.T) {
	doc := loadSpec(t)
	cases := map[string]struct{ ok, bad []string }{
		"ProjectLabel": {[]string{"benchwatch", "scratch (tmp)", "a.b_c-1"}, []string{"/Users/x", "a b", "", "~", ".env", "-Users-x-workspace", "..", "."}},
		"ToolLabel":    {[]string{"Read", "Read: ranged", "Read: image", "Bash: git", "Bash: shell loop", "mcp__figma__use", "other"}, []string{"Bash: rm -rf /", "Read: /etc/passwd", "a\nb", "Read: other"}},
		"SessionLabel": {[]string{"0a1b2c3d", "unknown"}, []string{"0A1B2C3D", "0a1b2c3d4", "0a1b2c3", "unknow", "Unknown"}},
		"Hash16":       {[]string{"0123456789abcdef"}, []string{"0123456789abcde", "0123456789abcdeg"}},
		"ModelLabel": {[]string{"claude-opus-5-5", "claude-sonnet-4-6[1m]", "unknown", "gpt-5.1-codex-max", "o4-mini",
			"gemini-2.5-pro", "qwen3-coder-plus"}, []string{"claude-", "claude-opus 5", "/Users/x", "a/b", "../x", ".env",
			"a..b", "GPT-4", "gpt says hi", "a\nb", "c:\\x", "~"}},
		"AgentTypeLabel": {[]string{"worker", "plugin:agent", "Explore"}, []string{"1worker", "a b", ""}},
	}
	for name, c := range cases {
		s := doc.Components.Schemas[name]
		if s == nil {
			t.Errorf("%s: schema missing", name)
			continue
		}
		re := regexp.MustCompile(s.Value.Pattern)
		for _, v := range c.ok {
			if !re.MatchString(v) {
				t.Errorf("%s: %q should match", name, v)
			}
		}
		for _, v := range c.bad {
			if re.MatchString(v) {
				t.Errorf("%s: %q should not match", name, v)
			}
		}
	}
}
