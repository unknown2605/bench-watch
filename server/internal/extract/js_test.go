package extract

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

const backslash = "\x5c"

var replacement = string(rune(0xfffd))

// The corpora are Node 24's own answers, recorded by a seeded generator with TZ pinned.
type dateCorpus struct {
	TZ      string               `json:"tz"`
	Strings [][2]any             `json:"strings"`
	Values  [][2]json.RawMessage `json:"values"`
	Numbers [][2]any             `json:"numbers"`
}

func loadJSON(t *testing.T, file string, into any) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatal(err)
	}
}

func withZone(t *testing.T, name string) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s missing", name)
	}
	saved := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = saved })
}

func TestDateParseMatchesNode(t *testing.T) {
	for _, file := range []string{"testdata/dates-new-york.json", "testdata/dates-ho-chi-minh.json"} {
		var corpus dateCorpus
		loadJSON(t, file, &corpus)
		t.Run(corpus.TZ, func(t *testing.T) {
			withZone(t, corpus.TZ)
			bad := 0
			for i, row := range corpus.Strings {
				text := row[0].(string)
				got, ok, err := DateParse(text)
				if err != nil {
					t.Fatalf("string %d threw", i)
				}
				want, valid := row[1].(float64)
				if ok != valid || (ok && got != want) {
					bad++
					if bad <= 20 {
						t.Errorf("row %d %q: go %v %v, node %v", i, text, got, ok, row[1])
					}
				}
			}
			for i, row := range corpus.Values {
				value, err := Parse(row[0])
				if err != nil {
					t.Fatal(err)
				}
				got, ok, err := DateParse(value)
				var want any
				_ = json.Unmarshal(row[1], &want)
				switch want := want.(type) {
				case string:
					if err == nil {
						t.Errorf("value %d: Node throws, Go gave %v", i, got)
					}
				case float64:
					if err != nil || !ok || got != want {
						t.Errorf("value %d: go %v %v %v, node %v", i, got, ok, err, want)
					}
				default:
					if err != nil || ok {
						t.Errorf("value %d: go %v %v %v, node NaN", i, got, ok, err)
					}
				}
			}
			for _, row := range corpus.Numbers {
				if got := NumberString(row[0].(float64)); got != row[1].(string) {
					t.Errorf("NumberString(%v) = %q, node %q", row[0], got, row[1])
				}
			}
		})
	}
}

func canon(v any) any {
	switch v := v.(type) {
	case string:
		return []any{"s", v}
	case float64:
		return []any{"n", NumberString(v)}
	case bool:
		return []any{"l", map[bool]string{true: "true", false: "false"}[v]}
	case nil:
		return []any{"l", "null"}
	case []any:
		items := make([]any, len(v))
		for i, item := range v {
			items[i] = canon(item)
		}
		return []any{"a", items}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		entries := make([]any, len(keys))
		for i, k := range keys {
			entries[i] = []any{k, canon(v[k])}
		}
		return []any{"o", entries}
	}
	return nil
}

func utf16Less(a, b string) bool {
	ua, ub := []rune(a), []rune(b)
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return unit(ua[i]) < unit(ub[i])
		}
	}
	return len(ua) < len(ub)
}

func unit(r rune) rune {
	if r >= 0x10000 {
		return 0xd800 + (r-0x10000)>>10
	}
	return r
}

func TestParseMatchesNode(t *testing.T) {
	var rows [][2]json.RawMessage
	loadJSON(t, "testdata/json.json", &rows)
	bad := 0
	for i, row := range rows {
		var encoded string
		_ = json.Unmarshal(row[0], &encoded)
		input, _ := base64.StdEncoding.DecodeString(encoded)
		var want any
		_ = json.Unmarshal(row[1], &want)
		value, err := Parse(input)
		var got any
		if err == nil {
			roundTrip, _ := json.Marshal(canon(value))
			_ = json.Unmarshal(roundTrip, &got)
		}
		wantText, _ := json.Marshal(want)
		gotText, _ := json.Marshal(got)
		if string(wantText) != string(gotText) {
			bad++
			if bad <= 20 {
				t.Errorf("row %d: go %s node %s", i, gotText, wantText)
			}
		}
	}
}

func TestParseStrictRules(t *testing.T) {
	v, err := Parse([]byte(`{"Type":"assistant","type":"user","a":1,"a":2,"big":1e400,"tiny":1e-400}`))
	if err != nil {
		t.Fatal(err)
	}
	if Get(v, "type") != "user" || Get(v, "Type") != "assistant" || Get(v, "a") != 2.0 {
		t.Fatalf("keys must match exactly and the last duplicate wins")
	}
	if f := Get(v, "big").(float64); !math.IsInf(f, 1) || Get(v, "tiny") != 0.0 {
		t.Fatalf("out-of-range numbers must become Infinity and 0")
	}
	if Get(v, "missing") != Undefined || Get("text", "type") != Undefined || Get([]any{1.0}, "type") != Undefined {
		t.Fatalf("property reads off non-objects are undefined")
	}
	invalid := []byte{'"', 0xe2, 0x82, 'A', 0xed, 0xa0, 0x80, 0xf0, 0x9f, 0x98, '"'}
	want := replacement + "A" + strings.Repeat(replacement, 4)
	if s, err := Parse(invalid); err != nil || s != want {
		t.Fatalf("invalid UTF-8 must become U+FFFD per maximal subpart, got %q %v", s, err)
	}
	if lone, _ := Parse([]byte(`"` + backslash + `ud83d"`)); lone != replacement {
		t.Fatalf("a lone surrogate escape must become U+FFFD")
	}
	rejected := []string{"\xef\xbb\xbf{}", `{"a":1,}`, "[1,]", "\"\x1f\"", "01", "NaN", "{\"a\"\xc2\xa0:1}",
		`"` + backslash + `x"`}
	for _, text := range rejected {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%q must be rejected", text)
		}
	}
	deep := strings.Repeat("[", 200000) + strings.Repeat("]", 200000)
	if _, err := Parse([]byte(deep)); err != nil {
		t.Fatalf("V8 parses 200000-deep nesting, so the port must too")
	}
}

func TestRoundAndTruthiness(t *testing.T) {
	if RoundJS(0.49999999999999994) != 0 || RoundJS(2.5) != 3 || RoundJS(1e9) != 1e9 {
		t.Fatal("RoundJS must follow Math.round")
	}
	for _, v := range []any{nil, Undefined, false, 0.0, math.NaN(), ""} {
		if Truthy(v) {
			t.Errorf("%v must be falsy", v)
		}
	}
	for _, v := range []any{true, 1.0, "0", []any{}, map[string]any{}} {
		if !Truthy(v) {
			t.Errorf("%v must be truthy", v)
		}
	}
}

func nestedArrays(depth int) any {
	var v any = []any{}
	for i := 1; i < depth; i++ {
		v = []any{v}
	}
	return v
}

func TestToStringFailsPastTheDepthBoundLikeAStackOverflow(t *testing.T) {
	if _, err := ToString(nestedArrays(MaxToStringDepth)); err != nil {
		t.Fatalf("depth %d: %v", MaxToStringDepth, err)
	}
	if _, err := ToString(nestedArrays(MaxToStringDepth + 1)); err != ErrStackDepth {
		t.Fatalf("depth %d: got %v", MaxToStringDepth+1, err)
	}
}

func TestParseSizesALongListOnce(t *testing.T) {
	filler := strings.Repeat(`"x,]}\"[{",`, 120000)
	text := `{"list":[` + filler + `1,2,[3,{"k":"a]b"}],{}],"obj":{"a":[],"b":"}","c":{"d":[1,2]}},"n":-0.5e1}`
	if len(text) < docSizeFrom {
		t.Fatal("the document must be large enough to be sized first")
	}
	got, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal([]byte(text), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("a sized document must parse to the same values")
	}
	list := Get(got, "list").([]any)
	if cap(list) != len(list) {
		t.Fatalf("the long list was grown, cap %d for %d items", cap(list), len(list))
	}
}

func TestRemainingCountsValuesNotCommas(t *testing.T) {
	for text, want := range map[string]int{
		"1,,,,,]":                       1,
		"1, 2 ,\n3]":                    3,
		`"a,b",[1,2],{"k":","}]`:        3,
		" ]":                            0,
		`"x\"],",{"a":[]}}`:             2,
		strings.Repeat(",", 1000) + "]": 0,
	} {
		p := parser{b: []byte(text), budget: 4 * len(text)}
		if got := p.remaining(); got != want {
			t.Errorf("remaining(%.40q) = %d, want %d", text, got, want)
		}
	}
}

func TestABrokenLongListIsNotSizedFromItsCommas(t *testing.T) {
	text := []byte("[" + strings.Repeat("1,", listSizeAt) + strings.Repeat(",", 4<<20) + "]")
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := Parse(text)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("a list of empty entries must be rejected")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20 {
		t.Fatalf("parsing a %d-byte broken list allocated %d bytes", len(text), grew)
	}
}

// Each level holds a long list and then the next level, so every scan covers all deeper levels.
func nestedLongLists(levels int) string {
	item := `"` + strings.Repeat("x", 28) + `",`
	var b strings.Builder
	for range levels {
		b.WriteString("[" + strings.Repeat(item, listSizeAt+1))
	}
	b.WriteString("0")
	b.WriteString(strings.Repeat("]", levels))
	return b.String()
}

func TestTheScanBudgetRunsOutWithoutChangingTheValues(t *testing.T) {
	text := nestedLongLists(10)
	if len(text) < docSizeFrom {
		t.Fatal("the document must be large enough to be sized")
	}
	p := parser{b: []byte(text), budget: 4 * len(text)}
	got, err := p.document()
	if err != nil {
		t.Fatal(err)
	}
	if p.budget != 0 {
		t.Fatalf("budget left %d, want it spent", p.budget)
	}
	var want any
	if err := json.Unmarshal([]byte(text), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("a document past the scan budget must parse to the same values")
	}
}

func TestMalformedLongListsAreRejected(t *testing.T) {
	long := strings.Repeat(`"`+strings.Repeat("x", 300)+`",`, listSizeAt+1)
	for name, tail := range map[string]string{
		"trailing comma":       `1,]`,
		"empty entry":          `1,,2]`,
		"closed by a brace":    `1}`,
		"never closed":         `1`,
		"unterminated string":  `"abc]`,
		"missing comma":        `1 2]`,
		"bare word":            `x]`,
		"inner list unclosed":  `[1,2]`,
		"key in a list":        `"a":1]`,
		"close before a value": `]`,
	} {
		text := "[" + long + tail
		if len(text) < docSizeFrom {
			t.Fatal("the document must be large enough to be sized")
		}
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestNumberBoundaries(t *testing.T) {
	for text, want := range map[string]float64{
		"999": 999, "1000": 1000, "0": 0, "-1": -1, "1e3": 1000, "1E3": 1000, "1.5": 1.5, "-0.5e1": -5,
		"5e-324": math.SmallestNonzeroFloat64, "1e-400": 0, "1E400": math.Inf(1), "-1e400": math.Inf(-1),
		"9007199254740993": 9007199254740992,
	} {
		v, err := Parse([]byte(text))
		if err != nil || v != want {
			t.Errorf("%s = %v %v, want %v", text, v, err, want)
		}
	}
	if v, err := Parse([]byte("-0")); err != nil || !math.Signbit(v.(float64)) {
		t.Errorf("-0 must keep its sign, got %v %v", v, err)
	}
	for _, text := range []string{"-", "1.", "1e", "1e+", "-a", ".5", "+1", "0x1", "1.e3", "--1", "Infinity"} {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%q must be rejected", text)
		}
	}
}
