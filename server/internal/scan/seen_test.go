package scan

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/source"
)

// result is one tool result of a test transcript; content is the JSON of the block's content.
type result struct{ tool, content string }

// text is a result long enough to count as a repeat; short is one below extract.MinRepeatTokens.
func text(s string) string { return fmt.Sprintf("%q", s+strings.Repeat(" filler", 40)) }

func short(s string) string { return fmt.Sprintf("%q", s) }

func blocks(s string) string { return fmt.Sprintf(`[{"type":"text","text":%s}]`, text(s)) }

func resultLines(results []result, from int) []string {
	var out []string
	for i, r := range results {
		n := from + i
		out = append(out, fmt.Sprintf(`{"type":"assistant","timestamp":"2026-11-10T10:00:%02d.000Z",`+
			`"message":{"id":"m%d","model":"claude-opus-5-5",`+
			`"content":[{"type":"tool_use","id":"tu%d","name":%q,"input":{}}],`+
			`"usage":{"input_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":7,`+
			`"output_tokens":2}}}`, n%60, n, n, r.tool))
		out = append(out, fmt.Sprintf(`{"type":"user","timestamp":"2026-11-10T10:00:%02d.000Z","message":{"content":[`+
			`{"type":"tool_result","tool_use_id":"tu%d","content":%s}]}}`, n%60, n, r.content))
	}
	return out
}

func wantRepeats(results []result) []bool {
	seen := map[result]bool{}
	out := make([]bool, len(results))
	for i, r := range results {
		if r.content == `""` {
			continue
		}
		out[i] = seen[r]
		seen[r] = true
	}
	return out
}

func repeatsOf(shard *Shard) []bool {
	var out []bool
	for _, e := range shard.Events {
		if e.Kind == extract.Result {
			out = append(out, e.Repeat)
		}
	}
	return out
}

func put(t *testing.T, file, content string, appendTo bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendTo {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(file, flags, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func update(t *testing.T, cache *Cache) *Store {
	t.Helper()
	st, err := cache.Update(nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func onlyShard(t *testing.T, st *Store) *Shard {
	t.Helper()
	if len(st.Entries) != 1 {
		t.Fatalf("%d entries, want 1", len(st.Entries))
	}
	for key := range st.Entries {
		shard, err := st.Load(key)
		if err != nil || shard == nil {
			t.Fatalf("shard of %s: %v", key, err)
		}
		return shard
	}
	return nil
}

var mixed = []result{
	{"Read", text("alpha")}, {"Grep", text("alpha")}, {"Read", text("alpha")}, {"Read", text("beta")},
	{"Read", blocks("alpha")}, {"Read", blocks("alpha")}, {"Bash", `""`},
	{"Bash", `""`}, {"Grep", text("alpha")}, {"Read", text("beta")}, {"Read", text("gamma")}, {"Read", text("alpha")},
}

const mainName = "aaaaaaaa-0000-0000-0000-000000000000.jsonl"

// Whatever the bytes a scan stops at, and whether or not the process restarted in between, the shard equals the one
// a single pass writes: same repeat marks, and no call, result or token counted twice.
func TestRepeatsDoNotDependOnScanTiming(t *testing.T) {
	whole := strings.Join(resultLines(mixed, 0), "\n") + "\n"
	want := wantRepeats(mixed)
	onePass := func() *Shard {
		root := t.TempDir()
		put(t, filepath.Join(root, "-p", mainName), whole, false)
		st, err := UpdateShards(root, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return onlyShard(t, st)
	}()
	if got := repeatsOf(onePass); !reflect.DeepEqual(got, want) {
		t.Fatalf("one pass marks %v, want %v", got, want)
	}
	if n := len(want); n < 10 || !want[2] || want[1] || want[7] {
		t.Fatalf("the fixture lost its cases: %v", want)
	}
	var cuts [][]int
	for at := 1; at < len(whole); at += 211 {
		cuts = append(cuts, []int{at})
	}
	for i, line := 0, 0; i < len(whole); i++ {
		if whole[i] == '\n' {
			if line++; line%3 == 0 {
				cuts = append(cuts, []int{i + 1}, []int{i})
			}
		}
	}
	cuts = append(cuts, []int{100, 900, 901, 2500}, []int{len(whole) / 3, 2 * len(whole) / 3})
	for _, restart := range []bool{false, true} {
		for _, cut := range cuts {
			root, cacheDir := t.TempDir(), t.TempDir()
			file := filepath.Join(root, "-p", mainName)
			cache := NewCache(root, cacheDir)
			rereads, last := 0, 0
			for _, at := range append(append([]int{}, cut...), len(whole)) {
				if at <= last || at > len(whole) {
					continue
				}
				put(t, file, whole[last:at], true)
				last = at
				if restart {
					cache = NewCache(root, cacheDir)
				}
				rereads += update(t, cache).Rereads
			}
			got := onlyShard(t, update(t, cache))
			same := reflect.DeepEqual(got.Events, onePass.Events) && got.Lines == onePass.Lines &&
				got.Skipped == onePass.Skipped && reflect.DeepEqual(got.Pending, onePass.Pending) &&
				got.Offset == onePass.Offset
			if !same {
				t.Fatalf("restart %v, cuts %v: the shard differs from one pass: %v, want %v", restart, cut,
					repeatsOf(got), want)
			}
			if !restart && rereads != 0 {
				t.Fatalf("cuts %v: %d rereads while the seen set was in memory", cut, rereads)
			}
		}
	}
}

// A restart loses the seen sets; only a transcript that grows afterwards is read again, once.
func TestRestartRereadsOnlyTranscriptsThatGrow(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	grows := filepath.Join(root, "-p", mainName)
	idle := filepath.Join(root, "-q", "bbbbbbbb-0000-0000-0000-000000000000.jsonl")
	first := strings.Join(resultLines(mixed[:6], 0), "\n") + "\n"
	put(t, grows, first, false)
	put(t, idle, first, false)
	update(t, NewCache(root, cacheDir))

	cache := NewCache(root, cacheDir)
	if st := update(t, cache); st.Rereads != 0 || st.BytesRead != 0 {
		t.Fatalf("a start with nothing new reread %d transcripts, %d bytes", st.Rereads, st.BytesRead)
	}
	rest := strings.Join(resultLines(mixed[6:], 6), "\n") + "\n"
	put(t, grows, rest, true)
	st := update(t, cache)
	if st.Rereads != 1 || st.RereadBytes != int64(len(first)) || st.BytesRead != int64(len(rest)) {
		t.Fatalf("%d rereads of %d bytes, %d bytes read on; want 1, %d, %d", st.Rereads, st.RereadBytes, st.BytesRead,
			len(first), len(rest))
	}
	put(t, grows, strings.Join(resultLines(mixed[:2], 20), "\n")+"\n", true)
	if st := update(t, cache); st.Rereads != 0 {
		t.Fatalf("a second growth reread the transcript %d times", st.Rereads)
	}
	shard, err := update(t, cache).Load(source.Key(clients.ClaudeCode, filepath.Join("-p", mainName)))
	if err != nil || shard == nil {
		t.Fatal("no shard for the grown transcript")
	}
	want := wantRepeats(append(append([]result{}, mixed...), mixed[:2]...))
	if got := repeatsOf(shard); !reflect.DeepEqual(got, want) {
		t.Fatalf("marks %v, want %v", got, want)
	}
}

// Idle, replaced and sub-agent transcripts: an idle one loses its set, a replaced file starts a new one, and every
// transcript has its own.
func TestSeenSetsFollowTheirTranscript(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "-p", mainName)
	sub := filepath.Join(root, "-p", strings.TrimSuffix(mainName, ".jsonl"), "subagents", "agent-a1.jsonl")
	cache := NewCache(root, t.TempDir())
	first := strings.Join(resultLines(mixed[:4], 0), "\n") + "\n"
	put(t, file, first, false)
	put(t, sub, first, false)
	st := update(t, cache)
	if len(cache.seen.sets) != 2 || cache.seen.entries != 6 {
		t.Fatalf("%d sets with %d entries, want 2 with 6", len(cache.seen.sets), cache.seen.entries)
	}
	for key := range st.Entries {
		shard, _ := st.Load(key)
		if got, want := repeatsOf(shard), wantRepeats(mixed[:4]); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s transcript marks %v, want %v", shard.Scope, got, want)
		}
	}

	cache.seen.now = func() time.Time { return time.Now().Add(SeenIdle) }
	update(t, cache)
	if len(cache.seen.sets) != 0 || cache.seen.entries != 0 {
		t.Fatalf("idle transcripts keep %d sets, %d entries", len(cache.seen.sets), cache.seen.entries)
	}
	cache.seen.now = time.Now
	put(t, file, strings.Join(resultLines(mixed[4:], 4), "\n")+"\n", true)
	if st := update(t, cache); st.Rereads != 1 || len(cache.seen.sets) != 1 {
		t.Fatalf("growth after idle: %d rereads, %d sets; want 1 and 1", st.Rereads, len(cache.seen.sets))
	}

	replaced := []result{{"Read", text("beta")}, {"Read", text("alpha")}, {"Read", text("beta")}}
	put(t, file, strings.Join(resultLines(replaced, 0), "\n")+"\n", false)
	st = update(t, cache)
	shard, _ := st.Load(source.Key(clients.ClaudeCode, filepath.Join("-p", mainName)))
	if got, want := repeatsOf(shard), wantRepeats(replaced); !reflect.DeepEqual(got, want) || st.Rereads != 0 {
		t.Fatalf("a replaced file marks %v after %d rereads, want %v after none", got, st.Rereads, want)
	}
}

// Over the bound the least recently read set goes and its transcript is reread when it grows; a transcript that
// cannot fit alone is reported unreadable instead of counted wrongly.
func TestSeenBoundEvictsAndNeverMiscounts(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "-p", mainName)
	b := filepath.Join(root, "-q", "bbbbbbbb-0000-0000-0000-000000000000.jsonl")
	distinct := func(prefix string, n int) []result {
		var out []result
		for i := range n {
			out = append(out, result{"Read", text(fmt.Sprintf("%s%d", prefix, i))})
		}
		return out
	}
	cache := NewCache(root, t.TempDir())
	cache.seen.maxEntries = 4
	put(t, a, strings.Join(resultLines(distinct("a", 3), 0), "\n")+"\n", false)
	update(t, cache)
	put(t, b, strings.Join(resultLines(distinct("b", 3), 0), "\n")+"\n", false)
	update(t, cache)
	if cache.seen.evictions != 1 || cache.seen.entries != 3 || len(cache.seen.sets) != 1 {
		t.Fatalf("%d evictions, %d entries, %d sets; want 1, 3, 1", cache.seen.evictions, cache.seen.entries,
			len(cache.seen.sets))
	}
	put(t, a, strings.Join(resultLines(distinct("a", 2), 3), "\n")+"\n", true)
	st := update(t, cache)
	shard, _ := st.Load(source.Key(clients.ClaudeCode, filepath.Join("-p", mainName)))
	want := []bool{false, false, false, true, true}
	if got := repeatsOf(shard); !reflect.DeepEqual(got, want) || st.Rereads != 1 {
		t.Fatalf("after eviction: marks %v, %d rereads; want %v, 1", got, st.Rereads, want)
	}
	if cache.seen.entries > cache.seen.maxEntries {
		t.Fatalf("%d entries over the bound of %d", cache.seen.entries, cache.seen.maxEntries)
	}

	put(t, b, strings.Join(resultLines(distinct("c", 5), 3), "\n")+"\n", true)
	before := st.Entries[source.Key(clients.ClaudeCode, filepath.Join("-q", "bbbbbbbb-0000-0000-0000-000000000000.jsonl"))]
	st = update(t, cache)
	after := st.Entries[source.Key(clients.ClaudeCode, filepath.Join("-q", "bbbbbbbb-0000-0000-0000-000000000000.jsonl"))]
	if st.Unreadable != 1 || after != before || !errors.Is(st.FileErr, ErrSeenFull) {
		t.Fatalf("a transcript over the bound: %d unreadable, entry changed %v, cause %v; want 1, false, %v",
			st.Unreadable, after != before, st.FileErr, ErrSeenFull)
	}
	if cache.seen.entries > cache.seen.maxEntries {
		t.Fatalf("%d entries over the bound of %d", cache.seen.entries, cache.seen.maxEntries)
	}

	// The failed read left nothing behind: read again with room, no result repeats one the failed read had seen.
	cache.seen.maxEntries = 100
	st = update(t, cache)
	shard, _ = st.Load(source.Key(clients.ClaudeCode, filepath.Join("-q", "bbbbbbbb-0000-0000-0000-000000000000.jsonl")))
	if got := repeatsOf(shard); !reflect.DeepEqual(got, make([]bool, 8)) || st.Unreadable != 0 || st.FileErr != nil {
		t.Fatalf("after the failed read: marks %v, %d unreadable; want 8 unmarked, 0", got, st.Unreadable)
	}
}

func TestSeenMemoryStaysUnderItsBound(t *testing.T) {
	if SeenMaxBytes != 24_000_000 {
		t.Fatalf("the bound is %d bytes; the docs and the ticket say 24 MB", SeenMaxBytes)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	mem := newSeen()
	const perSet = 50_000
	for i := 0; i < mem.maxEntries+3*perSet; i += perSet {
		set := mem.fresh()
		for j := range perSet {
			if repeat, err := mem.repeat(set, "Read", float64(i+j)); repeat || err != nil {
				t.Fatalf("entry %d: repeat %v, error %v", i+j, repeat, err)
			}
		}
		mem.keep(fmt.Sprint(i), set, float64(time.Now().UnixMilli()))
		if mem.entries > mem.maxEntries {
			t.Fatalf("%d entries over the bound of %d", mem.entries, mem.maxEntries)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	held := 0
	for _, set := range mem.sets {
		held += len(set.hashes)
	}
	if held != mem.entries || mem.entries != mem.maxEntries || mem.evictions != 3 {
		t.Fatalf("%d held, %d counted, %d evictions; want %d, %d, 3", held, mem.entries, mem.evictions, mem.maxEntries,
			mem.maxEntries)
	}
	if used := int64(after.HeapAlloc) - int64(before.HeapAlloc); used > SeenMaxBytes {
		t.Fatalf("a full registry holds %d bytes of heap, over the bound of %d", used, SeenMaxBytes)
	}
	runtime.KeepAlive(mem)
}

// Two transcripts that differ only in what their results say, with the same sizes and the same repeats, must give
// the same shard bytes: a shard holds counts and marks, nothing made from the content.
func TestShardHoldsNothingMadeFromResultContent(t *testing.T) {
	shardOf := func(words [3]string) []byte {
		// The last bytes differ too: a shard holds no hash of where its transcript was read to.
		tail := resultLines([]result{{"Read", fmt.Sprintf("%q", strings.Repeat("z", 300)+words[1])}}, 50)
		root, cacheDir := t.TempDir(), t.TempDir()
		results := []result{{"Read", text(words[0])}, {"Read", text(words[1])}, {"Read", text(words[0])},
			{"Grep", text(words[2])}, {"Grep", text(words[2])}}
		file := filepath.Join(root, "-p", mainName)
		put(t, file, strings.Join(append(resultLines(results, 0), tail...), "\n")+"\n", false)
		stamp := time.Now().Add(-time.Second).Truncate(time.Second)
		if err := os.Chtimes(file, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		st, err := UpdateShards(root, cacheDir, nil)
		if err != nil {
			t.Fatal(err)
		}
		shard := onlyShard(t, st)
		want := []bool{false, false, true, false, true, false}
		if got := repeatsOf(shard); !reflect.DeepEqual(got, want) {
			t.Fatalf("marks %v, want %v", got, want)
		}
		data, err := os.ReadFile(st.shardPath(shard.Key))
		if err != nil {
			t.Fatal(err)
		}
		for _, word := range words {
			if bytes.Contains(data, []byte(word)) {
				t.Fatalf("the shard holds result text %q", word)
			}
		}
		index, err := os.ReadFile(st.indexFile)
		if err != nil {
			t.Fatal(err)
		}
		return append(bytes.ReplaceAll(data, fmt.Appendf(nil, `"mtimeMs":%s`, appendFloat(nil, shard.MtimeMs)), nil),
			bytes.ReplaceAll(index, appendFloat(nil, st.Entries[shard.Key].BirthMs), nil)...)
	}
	one := shardOf([3]string{"secret-one", "secret-two", "secret-333"})
	other := shardOf([3]string{"hidden-aaa", "hidden-bbb", "hidden-ccc"})
	if !bytes.Equal(one, other) {
		t.Fatalf("shard or index bytes follow the result content:\n%s\n%s", one, other)
	}
}

func nested(depth int, leaf any) any {
	v := leaf
	for range depth {
		v = []any{v}
	}
	return v
}

// The parser has no depth limit, so content nested a million deep must neither be followed to the bottom nor change
// what counts: past the cap every value is the same.
func TestSeenFollowsContentToAFixedDepthOnly(t *testing.T) {
	mem := newSeen()
	set := mem.fresh()
	for _, c := range []struct {
		depth      int
		leaf       any
		wantRepeat bool
	}{
		{seenMaxDepth - 1, "one", false}, {seenMaxDepth - 1, "two", false}, {seenMaxDepth - 1, "one", true},
		{1_000_000, "one", false}, {1_000_000, "two", true}, {seenMaxDepth + 1, map[string]any{"k": 1.0}, true},
	} {
		repeat, err := mem.repeat(set, "Read", nested(c.depth, c.leaf))
		if err != nil || repeat != c.wantRepeat {
			t.Fatalf("depth %d, leaf %v: repeat %v, error %v; want %v", c.depth, c.leaf, repeat, err, c.wantRepeat)
		}
	}
	deepKey := func(leaf string) any {
		var v any = leaf
		for range seenMaxDepth + 5 {
			v = map[string]any{"k": v}
		}
		return v
	}
	if repeat, _ := mem.repeat(set, "Read", deepKey("one")); repeat {
		t.Fatal("deep objects repeat deep lists")
	}
	if repeat, _ := mem.repeat(set, "Read", deepKey("two")); !repeat {
		t.Fatal("objects are followed past the depth cap")
	}

	root := t.TempDir()
	deep := fmt.Sprintf(`[{"type":"text","text":%s},%s1%s]`, text("deep"), strings.Repeat("[", 100_000),
		strings.Repeat("]", 100_000))
	lines := resultLines([]result{{"Read", deep}, {"Read", deep}, {"Read", text("deep")}}, 0)
	put(t, filepath.Join(root, "-p", mainName), strings.Join(lines, "\n")+"\n", false)
	st, err := UpdateShards(root, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	shard := onlyShard(t, st)
	if got, want := repeatsOf(shard), []bool{false, true, false}; !reflect.DeepEqual(got, want) || shard.Skipped != 0 {
		t.Fatalf("deep results mark %v with %d lines skipped, want %v with none", got, shard.Skipped, want)
	}
}

func manyResults(n int) string {
	var parts []string
	for i := range n {
		// The first six say three things twice; every later one is new, so remembering them all needs n entries.
		say := i
		if i < 6 {
			say = i % 3
		}
		parts = append(parts, fmt.Sprintf(`{"type":"tool_result","tool_use_id":"big%d","content":%s}`, i,
			text(fmt.Sprintf("big%d", say))))
	}
	return `{"type":"user","timestamp":"2026-11-10T10:00:30.000Z","message":{"content":[` + strings.Join(parts, ",") +
		`]}}`
}

// One line can hold more results than a shard takes. The shard stops at its item cap inside the line, so the seen
// set of one transcript never needs more than the bound and the transcript is read once, not on every scan.
func TestOneLineCannotOutgrowTheShardOrTheSeenBound(t *testing.T) {
	const limit = 12
	newCache := func(root, cacheDir string) *Cache {
		cache := NewCache(root, cacheDir)
		cache.maxItems, cache.seen.maxEntries = limit, limit
		return cache
	}
	whole := strings.Join(resultLines(mixed[:3], 0), "\n") + "\n" + manyResults(40) + "\n" +
		strings.Join(resultLines(mixed[3:6], 3), "\n") + "\n"
	root := t.TempDir()
	put(t, filepath.Join(root, "-p", mainName), whole, false)
	cache := newCache(root, t.TempDir())
	st := update(t, cache)
	onePass := onlyShard(t, st)
	// Three calls and three results come first; the long line fills the six items left and the rest is skipped.
	want := []bool{false, false, true, false, false, false, true, true, true}
	if got := repeatsOf(onePass); !reflect.DeepEqual(got, want) || st.Unreadable != 0 || st.FileErr != nil {
		t.Fatalf("marks %v, %d unreadable, cause %v; want %v, 0, none", got, st.Unreadable, st.FileErr, want)
	}
	if n := len(onePass.Events) + len(onePass.Pending); n != limit || onePass.Lines != 13 || onePass.Skipped != 7 {
		t.Fatalf("%d items, %d lines, %d skipped; want %d, 13, 7", n, onePass.Lines, onePass.Skipped, limit)
	}
	if cache.seen.entries > limit {
		t.Fatalf("%d seen entries over the bound of %d", cache.seen.entries, limit)
	}
	if st := update(t, cache); st.BytesRead != 0 || st.Rereads != 0 || st.Unreadable != 0 {
		t.Fatalf("the next scan read %d bytes, reread %d, %d unreadable", st.BytesRead, st.Rereads, st.Unreadable)
	}

	for _, restart := range []bool{false, true} {
		for at := 1; at < len(whole); at += 97 {
			root, cacheDir := t.TempDir(), t.TempDir()
			file := filepath.Join(root, "-p", mainName)
			cache := newCache(root, cacheDir)
			put(t, file, whole[:at], false)
			first := update(t, cache)
			if restart {
				cache = newCache(root, cacheDir)
			}
			put(t, file, whole[at:], true)
			second := update(t, cache)
			got := onlyShard(t, second)
			if !reflect.DeepEqual(got.Events, onePass.Events) || got.Lines != onePass.Lines ||
				got.Skipped != onePass.Skipped || !reflect.DeepEqual(got.Pending, onePass.Pending) {
				t.Fatalf("restart %v, cut %d: the shard differs from one pass: %v", restart, at, repeatsOf(got))
			}
			if first.Unreadable+second.Unreadable != 0 || cache.seen.entries > limit {
				t.Fatalf("restart %v, cut %d: unreadable, or %d seen entries", restart, at, cache.seen.entries)
			}
		}
	}
}

// Short results and errors are never marked and never remembered: a fixed acknowledgement says the same thing
// after different work, and an error repeated is not content read twice.
func TestShortResultsAndErrorsNeverRepeat(t *testing.T) {
	under := fmt.Sprintf("%q", strings.Repeat("u", 4*extract.MinRepeatTokens-4))
	at := fmt.Sprintf("%q", strings.Repeat("a", 4*extract.MinRepeatTokens-3))
	results := []result{
		{"Bash", short("ok")}, {"Bash", short("ok")}, {"Write", short("File created successfully")},
		{"Write", short("File created successfully")}, {"Read", under}, {"Read", under}, {"Read", at}, {"Read", at},
	}
	lines := resultLines(results, 0)
	failed := func(n int, isError bool) string {
		return fmt.Sprintf(`{"type":"user","timestamp":"2026-11-10T10:00:40.000Z","message":{"content":[`+
			`{"type":"tool_result","tool_use_id":"err%d","is_error":%v,"content":%s}]}}`, n, isError, text("denied"))
	}
	lines = append(lines, failed(0, true), failed(1, true), failed(2, false), failed(3, false), failed(4, true))
	root := t.TempDir()
	put(t, filepath.Join(root, "-p", mainName), strings.Join(lines, "\n")+"\n", false)
	cache := NewCache(root, t.TempDir())
	shard := onlyShard(t, update(t, cache))
	want := []bool{false, false, false, false, false, false, false, true, false, false, false, true, false}
	if got := repeatsOf(shard); !reflect.DeepEqual(got, want) {
		t.Fatalf("marks %v, want %v", got, want)
	}
	if cache.seen.entries != 2 {
		t.Fatalf("%d results remembered, want 2: short results and errors are not kept", cache.seen.entries)
	}
}

// A shard that cannot be written keeps nothing in memory, so the next scan that can write counts every repeat once,
// whether or not the process restarted in between.
func TestFailedShardWriteLeavesTheMarksOfOnePass(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only folder")
	}
	first := strings.Join(resultLines(mixed[:5], 0), "\n") + "\n"
	rest := strings.Join(resultLines(mixed[5:], 5), "\n") + "\n"
	want := wantRepeats(mixed)
	for _, restart := range []bool{false, true} {
		root, cacheDir := t.TempDir(), t.TempDir()
		file := filepath.Join(root, "-p", mainName)
		cache := NewCache(root, cacheDir)
		put(t, file, first, false)
		st := update(t, cache)
		before := onlyShard(t, st)
		put(t, file, rest, true)
		if err := os.Chmod(st.shardDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(st.shardDir, 0o700) })
		for range 2 {
			st = update(t, cache)
			if st.Unreadable != 1 || st.FileErr == nil || cache.seen.entries != 0 || len(cache.seen.sets) != 0 {
				t.Fatalf("restart %v: a failed write gave %d unreadable, cause %v, %d seen entries in %d sets", restart,
					st.Unreadable, st.FileErr, cache.seen.entries, len(cache.seen.sets))
			}
			if got := onlyShard(t, st); !reflect.DeepEqual(got.Events, before.Events) || got.Offset != before.Offset {
				t.Fatalf("restart %v: the stored shard changed while its folder was read-only", restart)
			}
		}
		if err := os.Chmod(st.shardDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if restart {
			cache = NewCache(root, cacheDir)
		}
		st = update(t, cache)
		shard := onlyShard(t, st)
		if got := repeatsOf(shard); !reflect.DeepEqual(got, want) || st.Unreadable != 0 || st.Rereads != 1 {
			t.Fatalf("restart %v: marks %v, %d unreadable, %d rereads; want %v, 0, 1", restart, got, st.Unreadable,
				st.Rereads, want)
		}
		if n := len(shard.Events); n != 2*len(mixed) || shard.Lines != int64(2*len(mixed)) {
			t.Fatalf("restart %v: %d events from %d lines, want %d from %d", restart, n, shard.Lines, 2*len(mixed),
				2*len(mixed))
		}
	}
}

func setAge(t *testing.T, file string, ago time.Duration) {
	t.Helper()
	at := time.Now().Add(-ago).Truncate(time.Millisecond)
	if err := os.Chtimes(file, at, at); err != nil {
		t.Fatal(err)
	}
}

// A transcript rewritten in place, with the same size or a larger one, or cut short, is read from the start, whether
// the seen set is in memory or the process restarted. Nothing in the shard says where the transcript was read to.
func TestARewrittenOrCutTranscriptIsReadFromTheStart(t *testing.T) {
	lines := func(words ...string) string {
		var results []result
		for _, w := range words {
			// The word comes last, so the bytes before the place a scan stopped at differ between the contents.
			results = append(results, result{"Read", fmt.Sprintf("%q", strings.Repeat("filler ", 40)+w)})
		}
		return strings.Join(resultLines(results, 0), "\n") + "\n"
	}
	first := lines("alpha", "alpha", "gamma", "delta")
	cases := []struct {
		name, content string
		want          []bool
	}{
		{"same size", lines("omega", "gamma", "delta", "omega"), []bool{false, false, false, true}},
		{"larger", lines("omega", "gamma", "delta", "omega", "gamma"), []bool{false, false, false, true, true}},
		{"cut short", lines("omega", "omega"), []bool{false, true}},
	}
	if len(cases[0].content) != len(first) {
		t.Fatal("the same-size case lost its size")
	}
	for _, restart := range []bool{false, true} {
		for _, c := range cases {
			root, cacheDir := t.TempDir(), t.TempDir()
			file := filepath.Join(root, "-p", mainName)
			cache := NewCache(root, cacheDir)
			put(t, file, first, false)
			setAge(t, file, 2*time.Second)
			before := onlyShard(t, update(t, cache))
			if got, want := repeatsOf(before), []bool{false, true, false, false}; !reflect.DeepEqual(got, want) {
				t.Fatalf("first marks %v, want %v", got, want)
			}
			held := append([]extract.Event{}, before.Events...)
			data, err := os.ReadFile(filepath.Join(cacheDir, "shards", before.Key+".json"))
			if err != nil || bytes.Contains(data, []byte(`"print"`)) {
				t.Fatalf("the shard holds a print of its transcript: %v", err)
			}
			if restart {
				cache = NewCache(root, cacheDir)
			}
			put(t, file, c.content, false)
			setAge(t, file, time.Second)
			st := update(t, cache)
			got := onlyShard(t, st)
			if marks := repeatsOf(got); !reflect.DeepEqual(marks, c.want) || got.Offset != int64(len(c.content)) ||
				got.Lines != int64(2*len(c.want)) || len(got.Events) != 2*len(c.want) {
				t.Fatalf("restart %v, %s: marks %v from %d lines to offset %d, want %v from %d to %d", restart, c.name,
					marks, got.Lines, got.Offset, c.want, 2*len(c.want), len(c.content))
			}
			// What a scan reports as kept from the previous shard must really be in both.
			if a, ok := st.AppendedTo(got.Key); ok && (a.From > len(held) || a.From > len(got.Events) ||
				!reflect.DeepEqual(held[:a.From], got.Events[:a.From]) || a.From >= 4) {
				t.Fatalf("restart %v, %s: %d events reported as unchanged", restart, c.name, a.From)
			}
		}
	}
}

// After a restart or an idle spell a grown transcript is read whole, and the scan still reports which events are
// new, so only their days are replayed.
func TestAGrownTranscriptReadFromTheStartReportsItsNewEvents(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	file := filepath.Join(root, "-p", mainName)
	put(t, file, strings.Join(resultLines(mixed[:6], 0), "\n")+"\n", false)
	before := onlyShard(t, update(t, NewCache(root, cacheDir)))
	put(t, file, strings.Join(resultLines(mixed[6:], 6), "\n")+"\n", true)
	st := update(t, NewCache(root, cacheDir))
	a, ok := st.AppendedTo(before.Key)
	if !ok || a.From != len(before.Events) || st.Rereads != 1 {
		t.Fatalf("grown after a restart: appended %v from %d after %d rereads, want true from %d after 1", ok, a.From,
			st.Rereads, len(before.Events))
	}
	if got, want := repeatsOf(onlyShard(t, st)), wantRepeats(mixed); !reflect.DeepEqual(got, want) {
		t.Fatalf("marks %v, want %v", got, want)
	}
}

// A seen set rebuilt after a restart gives the marks of the incremental path when the shard is at, just under or
// over its item cap at the point the set is rebuilt.
func TestSeenRebuiltAtTheItemCapMarksAsTheIncrementalPath(t *testing.T) {
	const limit = 12
	newCache := func(root, cacheDir string) *Cache {
		cache := NewCache(root, cacheDir)
		cache.maxItems, cache.seen.maxEntries = limit, limit
		return cache
	}
	// Nine results, more than a shard of twelve items takes: it stops at six calls, five results and one pending
	// call. Every third result says the first again.
	var results []result
	for i := range 9 {
		say := fmt.Sprintf("cap%d", i)
		if i%3 == 2 {
			say = "cap0"
		}
		results = append(results, result{"Read", text(say)})
	}
	all := resultLines(results, 0)
	run := func(cut int, restart bool) (*Shard, *Shard, *Store) {
		root, cacheDir := t.TempDir(), t.TempDir()
		file := filepath.Join(root, "-p", mainName)
		cache := newCache(root, cacheDir)
		put(t, file, strings.Join(all[:cut], "\n")+"\n", false)
		stored := onlyShard(t, update(t, cache))
		if restart {
			cache = newCache(root, cacheDir)
		}
		put(t, file, strings.Join(all[cut:], "\n")+"\n", true)
		st := update(t, cache)
		if st.Unreadable != 0 || st.FileErr != nil || cache.seen.entries > limit {
			t.Fatalf("cut %d, restart %v: %d unreadable, cause %v, %d seen entries", cut, restart, st.Unreadable,
				st.FileErr, cache.seen.entries)
		}
		return stored, onlyShard(t, st), st
	}
	atCap := false
	for cut := 2; cut < len(all); cut += 2 {
		stored, incremental, _ := run(cut, false)
		_, rebuilt, st := run(cut, true)
		atCap = atCap || len(stored.Events)+len(stored.Pending) == limit
		if st.Rereads != 1 || st.RereadBytes != stored.Offset {
			t.Fatalf("cut %d: %d rereads of %d bytes, want 1 of %d", cut, st.Rereads, st.RereadBytes, stored.Offset)
		}
		if !reflect.DeepEqual(rebuilt.Events, incremental.Events) || rebuilt.Lines != incremental.Lines ||
			rebuilt.Skipped != incremental.Skipped || !reflect.DeepEqual(rebuilt.Pending, incremental.Pending) {
			t.Fatalf("cut %d: rebuilt marks %v over %d lines, %d skipped; incremental %v over %d, %d", cut,
				repeatsOf(rebuilt), rebuilt.Lines, rebuilt.Skipped, repeatsOf(incremental), incremental.Lines,
				incremental.Skipped)
		}
		if got, want := repeatsOf(rebuilt), []bool{false, false, true, false, false}; !reflect.DeepEqual(got, want) {
			t.Fatalf("cut %d: marks %v, want %v", cut, got, want)
		}
	}
	if !atCap {
		t.Fatal("no cut left the stored shard exactly at its item cap")
	}
}

// A transcript replaced by a new file (new birth time) that starts with the old content is not the old one grown, so
// no event is reported as kept, whether the seen set is in memory or not.
func TestAReplacedTranscriptIsNotReportedAsGrown(t *testing.T) {
	for _, restart := range []bool{false, true} {
		root, cacheDir := t.TempDir(), t.TempDir()
		file := filepath.Join(root, "-p", mainName)
		cache := NewCache(root, cacheDir)
		put(t, file, strings.Join(resultLines(mixed[:6], 0), "\n")+"\n", false)
		before := onlyShard(t, update(t, cache))
		if restart {
			cache = NewCache(root, cacheDir)
		}
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		put(t, file, strings.Join(resultLines(mixed, 0), "\n")+"\n", false)
		st := update(t, cache)
		if a, ok := st.AppendedTo(before.Key); ok {
			t.Fatalf("restart %v: a replaced transcript reported as grown from event %d", restart, a.From)
		}
		if got, want := repeatsOf(onlyShard(t, st)), wantRepeats(mixed); !reflect.DeepEqual(got, want) {
			t.Fatalf("restart %v: marks %v, want %v", restart, got, want)
		}
	}
}

// When the tail of the transcript cannot be read the set is dropped and the error is returned, so the caller keeps
// the previous shard.
func TestSeenTakeDropsTheSetWhenTheTailCannotBeRead(t *testing.T) {
	file := filepath.Join(t.TempDir(), "t.jsonl")
	put(t, file, strings.Repeat("x", 100), false)
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	mem := newSeen()
	set := mem.fresh()
	if _, err := mem.repeat(set, "Read", "a"); err != nil {
		t.Fatal(err)
	}
	set.offset = 100
	mem.sets["k"] = set
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := mem.take(f, "k", 100)
	if got != nil || err == nil {
		t.Fatalf("take on an unreadable file gave set %v, error %v; want nil and an error", got, err)
	}
	if mem.sets["k"] != nil || mem.entries != 0 {
		t.Fatalf("%d sets and %d entries left after the failed take, want 0 and 0", len(mem.sets), mem.entries)
	}
}

// A transcript too big for the bound, reread whole because the set is gone, keeps its stored shard and offset; the
// next scan with room rereads it once.
func TestSeenFullOnAWholeRereadKeepsTheStoredShard(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	file := filepath.Join(root, "-p", mainName)
	var results []result
	for i := range 6 {
		results = append(results, result{"Read", text(fmt.Sprintf("full%d", i))})
	}
	newCache := func(limit int) *Cache {
		cache := NewCache(root, cacheDir)
		cache.seen.maxEntries = limit
		return cache
	}
	put(t, file, strings.Join(resultLines(results[:3], 0), "\n")+"\n", false)
	stored := onlyShard(t, update(t, newCache(100)))
	key := source.Key(clients.ClaudeCode, filepath.Join("-p", mainName))
	entry := update(t, newCache(100)).Entries[key]
	put(t, file, strings.Join(resultLines(results[3:], 3), "\n")+"\n", true)

	for range 2 {
		cache := newCache(4)
		st := update(t, cache)
		if st.Unreadable != 1 || !errors.Is(st.FileErr, ErrSeenFull) || st.Entries[key] != entry || st.Changed != 0 {
			t.Fatalf("whole reread over the bound: %d unreadable, cause %v, entry kept %v, %d changed",
				st.Unreadable, st.FileErr, st.Entries[key] == entry, st.Changed)
		}
		if _, ok := st.AppendedTo(key); ok || cache.seen.entries != 0 || len(cache.seen.sets) != 0 {
			t.Fatalf("the failed reread left %d entries, %d sets or an appended report", cache.seen.entries,
				len(cache.seen.sets))
		}
		if kept, err := st.Load(key); err != nil || !reflect.DeepEqual(kept, stored) {
			t.Fatalf("stored shard changed after the failed reread: %v", err)
		}
	}

	st := update(t, newCache(100))
	if st.Unreadable != 0 || st.FileErr != nil || st.Rereads != 1 {
		t.Fatalf("next scan: %d unreadable, cause %v, %d rereads; want 0, nil, 1", st.Unreadable, st.FileErr, st.Rereads)
	}
	if got := repeatsOf(onlyShard(t, st)); !reflect.DeepEqual(got, make([]bool, 6)) {
		t.Fatalf("marks %v, want 6 unmarked", got)
	}
}
