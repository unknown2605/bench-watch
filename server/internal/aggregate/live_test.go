package aggregate

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"benchwatch/server/internal/scan"
)

// growing is a transcript tree that a test grows, forks, rewrites, truncates and deletes at random.
type growing struct {
	t     *testing.T
	root  string
	rng   *rand.Rand
	files map[string][]string
	seq   int
}

func (g *growing) next() int { g.seq++; return g.seq }

var callID = regexp.MustCompile(`"message":\{"id":"(msg_[0-9]+)"`)

func (g *growing) uuid() string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", g.rng.Uint32(), g.next())
}

func stamp(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02T15:04:05.000Z") }

// moment is a time on one of the last four local days, often near midnight so days split inside a transcript.
func (g *growing) moment() int64 {
	y, m, d := time.Now().Date()
	day := time.Date(y, m, d-g.rng.IntN(4), 0, 0, 0, 0, time.Local)
	if g.rng.IntN(3) == 0 {
		return day.Add(-time.Duration(g.rng.IntN(120)) * time.Second).Unix()
	}
	return day.Add(time.Duration(8*3600 + g.rng.IntN(12*3600))).Unix()
}

var testModels = []string{"claude-opus-5-5", "claude-haiku-5-5", "claude-sonnet-5", "claude-mystery-9"}

func (g *growing) call(ts int64, id string, uses ...string) string {
	var blocks []string
	for _, u := range uses {
		blocks = append(blocks, `{"type":"tool_use","id":"`+u+`","name":"Grep","input":{}}`)
	}
	return fmt.Sprintf(`{"type":"assistant","timestamp":"%s","message":{"id":"%s","model":"%s","content":[%s],`+
		`"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,`+
		`"output_tokens":%d}}}`, stamp(ts), id, testModels[g.rng.IntN(len(testModels))], strings.Join(blocks, ","),
		g.rng.IntN(5000), g.rng.IntN(20000), g.rng.IntN(300000), g.rng.IntN(4000))
}

func (g *growing) result(ts int64, use string) string {
	failed := g.rng.IntN(5) == 0
	body := strings.Repeat("x", g.rng.IntN(40000))
	if failed {
		body = "Exit code 1"
	}
	return fmt.Sprintf(`{"type":"user","timestamp":"%s","message":{"content":[{"type":"tool_result",`+
		`"tool_use_id":"%s","content":"%s","is_error":%v}]}}`, stamp(ts), use, body, failed)
}

func (g *growing) boundary(ts int64) string {
	return fmt.Sprintf(`{"type":"system","subtype":"compact_boundary","timestamp":"%s","uuid":"%s",`+
		`"compactMetadata":{"trigger":"auto","preTokens":%d}}`, stamp(ts), g.uuid(), g.rng.IntN(900000))
}

// lines is a short burst of activity: calls with tool uses and their results, now and then a compaction.
func (g *growing) lines() []string {
	var out []string
	ts := g.moment()
	for range 1 + g.rng.IntN(4) {
		ts += int64(g.rng.IntN(90))
		use := fmt.Sprintf("toolu_%d", g.next())
		out = append(out, g.call(ts, fmt.Sprintf("msg_%d", g.next()), use), g.result(ts+1, use))
		if g.rng.IntN(8) == 0 {
			out = append(out, g.boundary(ts+2))
		}
	}
	return out
}

func (g *growing) write(rel string, lines []string) {
	full := filepath.Join(g.root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		g.t.Fatal(err)
	}
	g.files[rel] = lines
	if err := os.WriteFile(full, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		g.t.Fatal(err)
	}
}

func (g *growing) newPath() string {
	project := []string{"-Users-x-workspace-alpha", "-Users-x-workspace-beta"}[g.rng.IntN(2)]
	if len(g.files) > 0 && g.rng.IntN(3) == 0 {
		for _, rel := range g.sorted() {
			if parts := strings.Split(rel, string(filepath.Separator)); len(parts) == 2 && g.rng.IntN(2) == 0 {
				session := strings.TrimSuffix(parts[1], ".jsonl")
				return filepath.Join(parts[0], session, "subagents", fmt.Sprintf("agent-%d.jsonl", g.next()))
			}
		}
	}
	return filepath.Join(project, g.uuid()+".jsonl")
}

func (g *growing) sorted() []string {
	keys := make([]string, 0, len(g.files))
	for k := range g.files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (g *growing) pick() string {
	keys := g.sorted()
	return keys[g.rng.IntN(len(keys))]
}

// step changes the tree once and names what it did.
func (g *growing) step() string {
	if len(g.files) < 3 {
		g.write(g.newPath(), g.lines())
		return "new"
	}
	rel := g.pick()
	lines := g.files[rel]
	other := g.files[g.pick()]
	switch r := g.rng.IntN(100); {
	case r < 42 || len(lines) == 0 || len(other) == 0:
		g.write(rel, append(slices.Clone(lines), g.lines()...))
		return "append"
	case r < 50:
		// A streamed message repeats its ID on a later line; the earlier event's counts rise in place.
		ids := callID.FindAllStringSubmatch(strings.Join(lines, "\n"), -1)
		if len(ids) == 0 {
			g.write(rel, append(slices.Clone(lines), g.lines()...))
			return "append"
		}
		id := ids[g.rng.IntN(len(ids))][1]
		g.write(rel, append(slices.Clone(lines), g.call(g.moment(), id), g.call(g.moment(), fmt.Sprintf("msg_%d", g.next()))))
		return "stream"
	case r < 60:
		g.write(g.newPath(), g.lines())
		return "new"
	case r < 68:
		g.write(g.newPath(), append(slices.Clone(lines[:1+g.rng.IntN(len(lines))]), g.lines()...))
		return "fork"
	case r < 74:
		// An earlier first line makes the copy own the shared IDs.
		early := g.call(time.Now().AddDate(0, 0, -5).Unix(), fmt.Sprintf("msg_%d", g.next()))
		g.write(g.newPath(), append([]string{early}, lines...))
		return "older copy"
	case r < 80:
		g.write(rel, append(slices.Clone(lines), other[g.rng.IntN(len(other))]))
		return "copied line"
	case r < 86:
		g.write(rel, g.lines())
		return "rewrite"
	case r < 92:
		g.write(rel, slices.Clone(lines[:g.rng.IntN(len(lines))]))
		return "truncate"
	default:
		if err := os.Remove(filepath.Join(g.root, rel)); err != nil {
			g.t.Fatal(err)
		}
		delete(g.files, rel)
		return "delete"
	}
}

func sortedKeys(keys []string) []string {
	out := slices.Clone(keys)
	slices.Sort(out)
	return out
}

func equalRows(t *testing.T, step string, got, want *Rows) {
	t.Helper()
	tables := []struct {
		name      string
		got, want any
	}{
		{"usage", got.Usage, want.Usage}, {"bands", got.ContextBands, want.ContextBands},
		{"compactions", got.Compactions, want.Compactions}, {"boundaries", got.Boundaries, want.Boundaries},
		{"sessions", got.Sessions, want.Sessions}, {"subagents", got.Subagents, want.Subagents},
		{"runs", got.SubagentRuns, want.SubagentRuns}, {"results", got.Results, want.Results},
		{"sizes", got.ResultSizes, want.ResultSizes}, {"top", got.TopResults, want.TopResults},
		{"errors", got.Errors, want.Errors}, {"contributors", got.Contributors, want.Contributors},
		{"skipped by client", got.SkippedByClient, want.SkippedByClient},
		{"digests", got.Digests(), want.Digests()}, {"keys", sortedKeys(got.Keys), sortedKeys(want.Keys)},
		{"figures", []any{got.Transcripts, got.Lines, got.Skipped, got.DuplicateLines, got.UniqueCalls, got.Unpriced,
			got.ItemsDropped}, []any{want.Transcripts, want.Lines, want.Skipped, want.DuplicateLines, want.UniqueCalls,
			want.Unpriced, want.ItemsDropped}},
	}
	for _, x := range tables {
		if (reflect.ValueOf(x.got).Len() > 0 || reflect.ValueOf(x.want).Len() > 0) && !reflect.DeepEqual(x.got, x.want) {
			t.Fatalf("after %s: %s differ from a full rebuild:\n got %+v\nwant %+v", step, x.name, x.got, x.want)
		}
	}
}

// Every run of the live build must equal a full rebuild from an empty cache, whatever happened to the files.
func TestLiveBuildEqualsFullRebuild(t *testing.T) {
	for seed := range uint64(4) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			dir := t.TempDir()
			g := &growing{t: t, root: filepath.Join(dir, "projects"), rng: rand.New(rand.NewPCG(seed, 7)),
				files: map[string][]string{}}
			live := NewLive()
			incremental, actions := 0, map[string]int{}
			for i := range 120 {
				action := g.step()
				actions[action]++
				if i%9 == 4 {
					live.Rest()
				}
				st, err := scan.UpdateShards(g.root, filepath.Join(dir, "cache"), nil)
				if err != nil {
					t.Fatal(err)
				}
				got, kind, err := live.Build(st, nil, nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				if kind == Incremental {
					incremental++
				}
				fresh, err := scan.UpdateShards(g.root, filepath.Join(dir, fmt.Sprintf("full%d", i)), nil)
				if err != nil {
					t.Fatal(err)
				}
				want, err := BuildRows(fresh.Shards, MaxRunItems, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				equalRows(t, fmt.Sprintf("step %d (%s)", i, action), got, want)
				if err := os.RemoveAll(filepath.Join(dir, fmt.Sprintf("full%d", i))); err != nil {
					t.Fatal(err)
				}
			}
			if incremental < 100 || actions["rewrite"] == 0 || actions["truncate"] == 0 || actions["fork"] == 0 ||
				actions["older copy"] == 0 || actions["delete"] == 0 || actions["stream"] == 0 || live.grown < 20 {
				t.Fatalf("%d of 120 runs incremental, %d grown shards narrowed, actions %v", incremental, live.grown,
					actions)
			}
			st, err := scan.UpdateShards(g.root, filepath.Join(dir, "cache"), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, kind, err := live.Build(st, nil, nil, FullEvery); err != nil || kind != Scheduled || live.Drift != 0 {
				t.Fatalf("hourly full rebuild: kind %v drift %d err %v", kind, live.Drift, err)
			}
		})
	}
}

// A shard that cannot be loaded, or new token bands, make the next run replay everything.
func TestLiveBuildFallsBackToAFullReplay(t *testing.T) {
	dir := t.TempDir()
	g := &growing{t: t, root: filepath.Join(dir, "projects"), rng: rand.New(rand.NewPCG(9, 9)),
		files: map[string][]string{}}
	for range 4 {
		g.step()
	}
	live := NewLive()
	cache := filepath.Join(dir, "cache")
	build := func(bands []Band) BuildKind {
		t.Helper()
		st, err := scan.UpdateShards(g.root, cache, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, kind, err := live.Build(st, bands, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return kind
	}
	if build(nil) != Scheduled {
		t.Fatal("the first run must be a scheduled full replay")
	}
	g.step()
	if build(nil) != Incremental {
		t.Fatal("a second run with one change replayed everything")
	}
	if build(bandsOf(0, 50_000)) != Forced {
		t.Fatal("new token bands must force a full replay")
	}
}

func fullRows(t *testing.T, root, cache string) *Rows {
	t.Helper()
	defer os.RemoveAll(cache)
	fresh, err := scan.UpdateShards(root, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := BuildRows(fresh.Shards, MaxRunItems, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return want
}

// Another process sharing the cache (scan-dump) may absorb a change first; Live must find it by shard hash.
func TestLiveBuildReplaysChangesAbsorbedElsewhere(t *testing.T) {
	dir := t.TempDir()
	g := &growing{t: t, root: filepath.Join(dir, "projects"), rng: rand.New(rand.NewPCG(3, 11)),
		files: map[string][]string{}}
	cache := filepath.Join(dir, "cache")
	live := NewLive()
	for i := range 40 {
		action := g.step()
		if _, err := scan.UpdateShards(g.root, cache, nil); err != nil {
			t.Fatal(err)
		}
		st, err := scan.UpdateShards(g.root, cache, nil)
		if err != nil || st.Changed != 0 || st.Removed != 0 {
			t.Fatalf("step %d: the second update saw %d changes and %d removals, err %v", i, st.Changed, st.Removed,
				err)
		}
		if i > 0 && live.Current(st) {
			t.Fatalf("step %d (%s): the cache changed but Live calls itself current", i, action)
		}
		got, kind, err := live.Build(st, nil, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && kind != Incremental {
			t.Fatalf("step %d (%s): kind %v, want an incremental run", i, action, kind)
		}
		if !live.Current(st) {
			t.Fatalf("step %d: not current right after a build", i)
		}
		equalRows(t, fmt.Sprintf("step %d (%s)", i, action), got, fullRows(t, g.root, filepath.Join(dir, "full")))
	}
}

// A caller may change the rows it gets; the rows Live keeps for the next run must not change with them.
func TestLiveRowsAreTheCallersOwn(t *testing.T) {
	dir := t.TempDir()
	g := &growing{t: t, root: filepath.Join(dir, "projects"), rng: rand.New(rand.NewPCG(5, 5)),
		files: map[string][]string{}}
	cache := filepath.Join(dir, "cache")
	live := NewLive()
	spoil := func(r *Rows) {
		for i := range r.Usage {
			r.Usage[i].Calls += 1000
		}
		for i := range r.ContextBands {
			if p := r.ContextBands[i].MaxTokens; p != nil {
				*p += 7
			}
			r.ContextBands[i].Calls += 1000
		}
		for i := range r.Boundaries {
			if p := r.Boundaries[i].TokensAfter; p != nil {
				*p += 7
			}
		}
		for i := range r.ResultSizes {
			if p := r.ResultSizes[i].MaxTokens; p != nil {
				*p += 7
			}
		}
		for i := range r.Sessions {
			r.Sessions[i].Calls += 1000
		}
		for d, c := range r.Contributors {
			for i := range c {
				c[i] = "spoiled"
			}
			r.Contributors[d] = append(c, "extra")
		}
	}
	for i := range 30 {
		action := g.step()
		st, err := scan.UpdateShards(g.root, cache, nil)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Duration(0)
		if i == 20 {
			at = FullEvery
		}
		got, _, err := live.Build(st, nil, nil, at)
		if err != nil {
			t.Fatal(err)
		}
		equalRows(t, fmt.Sprintf("step %d (%s)", i, action), got, fullRows(t, g.root, filepath.Join(dir, "full")))
		spoil(got)
	}
	if DefaultContextBands[1].Max == nil || *DefaultContextBands[1].Max != 200_000 ||
		*DefaultResultBands[1].Max != 5000 {
		t.Fatal("a caller's change reached the default bands")
	}
}

// A boundary another transcript owns does not end the wait for the call after an older one, so a call appended past
// it still fills that older boundary's day.
func TestLiveAppendPastASharedBoundary(t *testing.T) {
	dir := t.TempDir()
	g := &growing{t: t, root: filepath.Join(dir, "projects"), rng: rand.New(rand.NewPCG(9, 9)),
		files: map[string][]string{}}
	y, m, d := time.Now().Date()
	noon := func(back int) int64 { return time.Date(y, m, d-back, 12, 0, 0, 0, time.Local).Unix() }
	shared := g.boundary(noon(1))
	// The other transcript starts first, so it owns the shared boundary.
	g.write("-p/bbbbbbbb-0000-4000-8000-000000000000.jsonl", []string{g.call(noon(3), "msg_0"), shared})
	main := "-p/aaaaaaaa-0000-4000-8000-000000000000.jsonl"
	g.write(main, []string{g.boundary(noon(2)), shared})
	cache := filepath.Join(dir, "cache")
	live := NewLive()
	for i, grow := range []bool{false, true} {
		if grow {
			g.write(main, append(slices.Clone(g.files[main]), g.call(time.Now().Unix()-60, "msg_1")))
		}
		st, err := scan.UpdateShards(g.root, cache, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, kind, err := live.Build(st, nil, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if grow && (kind != Incremental || live.grown != 1) {
			t.Fatalf("the append ran as %v with %d narrowed shards, want one narrowed incremental run", kind, live.grown)
		}
		equalRows(t, fmt.Sprintf("run %d", i), got, fullRows(t, g.root, filepath.Join(dir, "full")))
	}
}
