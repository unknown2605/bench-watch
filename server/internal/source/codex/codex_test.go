package codex

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/codelog"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/source"
)

const (
	threadA = "019e3da9-0000-7000-8000-00000000000a"
	threadB = "019e3da9-0000-7000-8000-00000000000b"
	threadC = "019e3da9-0000-7000-8000-00000000000c"
	threadD = "019e3da9-0000-7000-8000-00000000000d"
	threadE = "019e3da9-0000-7000-8000-00000000000e"
)

func meta(id, extra string) string {
	return `{"timestamp":"2026-05-19T07:00:00.000Z","ordinal":0,"type":"session_meta","payload":{"id":"` + id +
		`","session_id":"` + id + `","cwd":"/nowhere/proj"` + extra + `}}`
}

func turn(ts, model string) string {
	return `{"timestamp":"` + ts + `","type":"turn_context","payload":{"model":"` + model + `","cwd":"/nowhere/proj"}}`
}

func usageJSON(u [5]int64) string {
	return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,`+
		`"cache_write_input_tokens":0,"total_tokens":%d}`, u[0], u[1], u[2], u[3], u[4])
}

func tokens(ts string, total, last [5]int64) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		usageJSON(total) + `,"last_token_usage":` + usageJSON(last) + `,"model_context_window":272000}}}`
}

const nullInfo = `{"timestamp":"2026-05-19T10:00:00Z","type":"event_msg","payload":{"type":"token_count","info":null}}`

// Thread A, plain and live: one null info, one repeated total, one malformed line and a cut last line.
var (
	a1 = [5]int64{100, 40, 10, 5, 110}
	a2 = [5]int64{250, 100, 30, 8, 280}
	a3 = [5]int64{400, 100, 35, 8, 435}
	a4 = [5]int64{500, 100, 40, 8, 540}
	b1 = [5]int64{1000, 800, 50, 20, 1050}
	b2 = [5]int64{2100, 1600, 120, 40, 2220}
)

func diff(total, prev [5]int64) [5]int64 {
	var d [5]int64
	for i := range d {
		d[i] = total[i] - prev[i]
	}
	return d
}

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

var (
	threadALines = lines(meta(threadA, ""), turn("2026-05-19T09:59:00Z", "gpt-5.1-codex"), nullInfo,
		tokens("2026-05-19T10:00:00Z", a1, a1), tokens("2026-05-19T10:00:01Z", a1, a1),
		tokens("2026-05-19T10:05:00Z", a2, diff(a2, a1)), `{"timestamp":"2026-05-19T10:06:00Z","type":`,
		turn("2026-05-19T10:07:00Z", "o4-mini"), tokens("2026-05-19T10:08:00Z", a3, diff(a3, a2)))
	threadALast  = tokens("2026-05-20T10:00:00Z", a4, diff(a4, a3))
	threadBLines = lines(meta(threadB, ""), turn("2026-05-18T09:00:00Z", "gpt-5"),
		tokens("2026-05-18T09:01:00Z", b1, b1), tokens("2026-05-18T09:02:00Z", b2, diff(b2, b1)))
	// C ends with an imported total that carries total_tokens alone.
	threadCLines = lines(meta(threadC, ""), tokens("2026-05-18T12:00:00Z", [5]int64{10, 0, 5, 0, 15},
		[5]int64{10, 0, 5, 0, 15}), tokens("2026-05-18T12:01:00Z", [5]int64{0, 0, 0, 0, 9941},
		[5]int64{0, 0, 0, 0, 9926}))
	// A fork of B replays B's history with new timestamps, then makes one request of its own.
	d1           = [5]int64{2300, 1600, 130, 40, 2430}
	threadDLines = lines(meta(threadD, `,"forked_from_id":"`+threadB+`"`), turn("2026-05-21T09:00:00Z", "gpt-5"),
		tokens("2026-05-21T09:00:01Z", b1, b1), tokens("2026-05-21T09:00:01Z", b2, diff(b2, b1)),
		tokens("2026-05-21T09:10:00Z", d1, diff(d1, b2)))
)

func compress(t *testing.T, plain string) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rollout(id string) string { return "rollout-2026-05-19T07-00-00-" + id + ".jsonl" }

type fixture struct {
	home, live, liveA string
	reader            *Reader
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	home := t.TempDir()
	f := fixture{home: home, live: filepath.Join(home, "sessions", "2026", "05", "19")}
	f.liveA = filepath.Join(f.live, rollout(threadA))
	write(t, f.liveA, []byte(threadALines+threadALast[:40]))
	archived := filepath.Join(home, "archived_sessions")
	write(t, filepath.Join(archived, rollout(threadB)+".zst"), compress(t, threadBLines))
	write(t, filepath.Join(archived, rollout(threadC)+".zst"), []byte(threadCLines))
	write(t, filepath.Join(archived, rollout(threadD)), []byte(threadDLines))
	f.reader = New(home)
	return f
}

func readAll(t *testing.T, r *Reader, unit source.Unit, from source.Cursor) source.Cursor {
	t.Helper()
	f, _, err := source.Open(r.Roots(), unit.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cur, err := r.Read(f, unit, from, source.Options{MaxItems: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func unitOf(t *testing.T, r *Reader, name string) source.Unit {
	t.Helper()
	d, err := r.Discover()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range d.Units {
		if filepath.Base(u.Path) == name {
			return u
		}
	}
	t.Fatalf("no unit %s", name)
	return source.Unit{}
}

func ns(events []extract.Event) [][5]int64 {
	out := make([][5]int64, len(events))
	for i, e := range events {
		out[i] = e.N
	}
	return out
}

func TestAPlainRolloutCountsEachRequestOnceAndResumesAfterItsCutLine(t *testing.T) {
	f := newFixture(t)
	unit := unitOf(t, f.reader, rollout(threadA))
	cur := readAll(t, f.reader, unit, source.Cursor{})
	// Hand count: input minus cached, cw5 = cw1h = 0, cached, output with reasoning inside it.
	want := [][5]int64{{60, 0, 0, 40, 10}, {90, 0, 0, 60, 20}, {150, 0, 0, 0, 5}}
	if got := ns(cur.Events); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if cur.Lines != 9 || cur.Skipped != 1 || cur.Offset != int64(len(threadALines)) {
		t.Fatalf("lines %d skipped %d offset %d; want 9, 1, %d", cur.Lines, cur.Skipped, cur.Offset, len(threadALines))
	}
	models := []string{cur.Events[0].Text, cur.Events[1].Text, cur.Events[2].Text}
	if fmt.Sprint(models) != "[gpt-5.1-codex gpt-5.1-codex o4-mini]" {
		t.Fatalf("models %v", models)
	}
	write(t, f.liveA, []byte(threadALines+threadALast+"\n"))
	unit = unitOf(t, f.reader, rollout(threadA))
	cur = readAll(t, f.reader, unit, cur)
	if len(cur.Events) != 4 || cur.Events[3].N != [5]int64{100, 0, 0, 0, 5} || cur.Events[3].Text != "o4-mini" {
		t.Fatalf("resumed events %v", cur.Events)
	}
	if cur.Offset != unit.Size || cur.Lines != 10 {
		t.Fatalf("offset %d lines %d; want %d, 10", cur.Offset, cur.Lines, unit.Size)
	}
}

func TestCompressedAndMisnamedPlainRolloutsDecodeBySniffingTheMagic(t *testing.T) {
	f := newFixture(t)
	b := unitOf(t, f.reader, rollout(threadB)+".zst")
	cur := readAll(t, f.reader, b, source.Cursor{})
	if got := ns(cur.Events); fmt.Sprint(got) != "[[200 0 0 800 50] [300 0 0 800 70]]" || cur.Offset != b.Size {
		t.Fatalf("zstd events %v offset %d size %d", got, cur.Offset, b.Size)
	}
	again := readAll(t, f.reader, b, cur)
	if fmt.Sprint(again.Events) != fmt.Sprint(cur.Events) || again.Lines != cur.Lines {
		t.Fatalf("a compressed re-read added to its cursor: %v", again.Events)
	}
	c := unitOf(t, f.reader, rollout(threadC)+".zst")
	cur = readAll(t, f.reader, c, source.Cursor{})
	if got := ns(cur.Events); fmt.Sprint(got) != "[[10 0 0 0 5]]" || cur.Events[0].Text != "unknown" ||
		cur.Skipped != 1 {
		t.Fatalf("plain .zst events %v", cur.Events)
	}
}

func readErr(t *testing.T, r *Reader, unit source.Unit) error {
	t.Helper()
	f, _, err := source.Open(r.Roots(), unit.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = r.Read(f, unit, source.Cursor{}, source.Options{MaxItems: 1000})
	return err
}

func TestACorruptCompressedStreamFailsTheReadSoThePreviousShardStays(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.home, "archived_sessions", rollout(threadB)+".zst")
	data := compress(t, threadBLines)
	write(t, path, data[:len(data)-6])
	if err := readErr(t, f.reader, unitOf(t, f.reader, filepath.Base(path))); err == nil {
		t.Fatal("a stream cut mid-frame read as a success")
	}
	write(t, path, append(append([]byte(nil), data[:4]...), bytes.Repeat([]byte{0xff}, 64)...))
	if err := readErr(t, f.reader, unitOf(t, f.reader, filepath.Base(path))); err == nil {
		t.Fatal("a stream corrupt before its first record read as a success")
	}
}

// A rollout cut at the cap is unreadable, never stored as complete; one exactly at the cap reads whole.
func TestACompressedStreamOverTheDecodedCapFailsAsUnreadable(t *testing.T) {
	if maxDecoded != 256<<20 {
		t.Errorf("decoded cap %d, want 256 MiB", maxDecoded)
	}
	f := newFixture(t)
	was := maxDecoded
	defer func() { maxDecoded = was }()
	unit := unitOf(t, f.reader, rollout(threadB)+".zst")
	maxDecoded = int64(len(threadBLines) - 10)
	if err := readErr(t, f.reader, unit); !errors.Is(err, ErrTooLarge) || codelog.Of(err) != "EDECODECAP" {
		t.Fatalf("capped stream read: %v", err)
	}
	maxDecoded = int64(len(threadBLines))
	if cur := readAll(t, f.reader, unit, source.Cursor{}); cur.Skipped != 0 || len(cur.Events) == 0 {
		t.Fatalf("stream at the cap: events %d skipped %d", len(cur.Events), cur.Skipped)
	}
}

func TestAUsageWithoutNumericInputOrOutputIsSkippedNotAZeroCall(t *testing.T) {
	f := newFixture(t)
	renamed := `{"timestamp":"2026-05-19T11:00:00Z","type":"event_msg","payload":{"type":"token_count","info":` +
		`{"total_token_usage":{"in":5,"output_tokens":"7"},"last_token_usage":{"in":5,"out":7}}}}`
	path := filepath.Join(f.home, "archived_sessions", rollout(threadE))
	write(t, path, []byte(lines(meta(threadE, ""), renamed, tokens("2026-05-19T11:01:00Z", a1, a1))))
	cur := readAll(t, f.reader, unitOf(t, f.reader, filepath.Base(path)), source.Cursor{})
	if got := ns(cur.Events); fmt.Sprint(got) != "[[60 0 0 40 10]]" || cur.Skipped != 1 {
		t.Fatalf("events %v skipped %d", got, cur.Skipped)
	}
}

func TestATokenCountWithAnUnparsableTimestampCountsASkip(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.home, "archived_sessions", rollout(threadE))
	write(t, path, []byte(lines(meta(threadE, ""), tokens("not a date", a1, a1))))
	cur := readAll(t, f.reader, unitOf(t, f.reader, filepath.Base(path)), source.Cursor{})
	if len(cur.Events) != 1 || cur.Skipped != 1 {
		t.Fatalf("events %v skipped %d", ns(cur.Events), cur.Skipped)
	}
}

func TestALargeRolloutWithNoKnownUsageRecordIsUnreadable(t *testing.T) {
	f := newFixture(t)
	other := `{"timestamp":"2026-05-19T11:00:00Z","type":"event_msg","payload":{"type":"usage_v2","n":` +
		strings.Repeat("1", 200) + `}}`
	var b strings.Builder
	b.WriteString(meta(threadE, "") + "\n")
	for b.Len() < source.FormatFloor {
		b.WriteString(other + "\n")
	}
	path := filepath.Join(f.home, "archived_sessions", rollout(threadE))
	write(t, path, []byte(b.String()))
	if err := readErr(t, f.reader, unitOf(t, f.reader, filepath.Base(path))); !errors.Is(err, source.ErrUnrecognised) {
		t.Fatalf("drifted rollout: %v", err)
	}
}

// Imported threads hold one total_tokens-only usage among many known records; that is a skip, not an unknown format.
func TestAnImportedRolloutOfAnySizeIsASkipNotUnreadable(t *testing.T) {
	f := newFixture(t)
	msg := `{"timestamp":"2026-05-19T11:00:00Z","type":"response_item","payload":{"type":"message","n":` +
		strings.Repeat("1", 200) + `}}`
	imported := tokens("2026-05-19T11:00:01Z", [5]int64{0, 0, 0, 0, 9000}, [5]int64{0, 0, 0, 0, 9000})
	for _, size := range []int{1 << 10, source.FormatFloor} {
		var b strings.Builder
		b.WriteString(meta(threadE, "") + "\n" + imported + "\n")
		for b.Len() < size {
			b.WriteString(msg + "\n")
		}
		path := filepath.Join(f.home, "archived_sessions", rollout(threadE))
		write(t, path, []byte(b.String()))
		cur := readAll(t, f.reader, unitOf(t, f.reader, filepath.Base(path)), source.Cursor{})
		if len(cur.Events) != 0 || cur.Skipped != 1 {
			t.Fatalf("size %d: events %v skipped %d", size, ns(cur.Events), cur.Skipped)
		}
	}
}

func TestAnImportedTotalDoesNotHideMalformedUsageInALargeRollout(t *testing.T) {
	f := newFixture(t)
	imported := tokens("2026-05-19T11:00:01Z", [5]int64{0, 0, 0, 0, 9000}, [5]int64{0, 0, 0, 0, 9000})
	drifted := `{"timestamp":"2026-05-19T11:00:02Z","type":"event_msg","payload":{"type":"token_count","info":` +
		`{"total_token_usage":{"in":"` + strings.Repeat("1", 200) + `"},"last_token_usage":{}}}}`
	var b strings.Builder
	b.WriteString(meta(threadE, "") + "\n" + imported + "\n")
	for b.Len() < source.FormatFloor {
		b.WriteString(drifted + "\n")
	}
	path := filepath.Join(f.home, "archived_sessions", rollout(threadE))
	write(t, path, []byte(b.String()))
	if err := readErr(t, f.reader, unitOf(t, f.reader, filepath.Base(path))); !errors.Is(err, source.ErrUnrecognised) {
		t.Fatalf("drift after an imported total: %v", err)
	}
}

func TestIdentifyFailsUntilTheFirstRecordIsASessionMeta(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.home, "archived_sessions", rollout(threadE))
	for _, content := range []string{"", meta(threadE, "")[:30], lines(turn("2026-05-19T09:59:00Z", "gpt-5"))} {
		write(t, path, []byte(content))
		if _, err := f.reader.Identify(unitOf(t, f.reader, filepath.Base(path)), nil); err == nil {
			t.Fatalf("%q: a default identity was given", content)
		}
	}
	write(t, path, []byte(lines(meta(threadE, ""))))
	id, err := f.reader.Identify(unitOf(t, f.reader, filepath.Base(path)), nil)
	if err != nil || id.Session != threadE[:8] {
		t.Fatalf("identity %+v, %v", id, err)
	}
}

// replay mirrors the scanner: the max per event ID, so a copied request counts once.
func replay(units []source.Cursor) (n [5]int64, unique int) {
	best := map[string][5]int64{}
	for _, c := range units {
		for _, e := range c.Events {
			m := best[e.ID]
			for i := range m {
				m[i] = max(m[i], e.N[i])
			}
			best[e.ID] = m
		}
	}
	for _, m := range best {
		for i := range n {
			n[i] += m[i]
		}
	}
	return n, len(best)
}

func TestAForkThatReplaysItsParentAddsOnlyItsOwnRequest(t *testing.T) {
	f := newFixture(t)
	parent := readAll(t, f.reader, unitOf(t, f.reader, rollout(threadB)+".zst"), source.Cursor{})
	fork := readAll(t, f.reader, unitOf(t, f.reader, rollout(threadD)), source.Cursor{})
	if len(fork.Events) != 3 || fork.Events[0].ID != parent.Events[0].ID || fork.Events[1].ID != parent.Events[1].ID {
		t.Fatalf("fork replay IDs differ from the parent's")
	}
	if n, unique := replay([]source.Cursor{parent, fork}); unique != 3 || n != [5]int64{700, 0, 0, 1600, 130} {
		t.Fatalf("parent plus fork %v over %d calls; want [700 0 0 1600 130] over 3", n, unique)
	}
}

func TestAMovedRolloutKeepsItsKeyAndIsListedOnce(t *testing.T) {
	f := newFixture(t)
	before := unitOf(t, f.reader, rollout(threadA))
	archived := filepath.Join(f.home, "archived_sessions", rollout(threadA))
	data, err := os.ReadFile(f.liveA)
	if err != nil {
		t.Fatal(err)
	}
	write(t, archived, data)
	d, err := f.reader.Discover()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, u := range d.Units {
		if u.Key == before.Key {
			count++
			if u.Path != archived {
				t.Errorf("tie between copies kept %s, want the archived one", u.Rel)
			}
		}
	}
	if count != 1 || len(d.Units) != 4 {
		t.Fatalf("thread listed %d times over %d units; want 1 over 4", count, len(d.Units))
	}
	if err := os.Remove(f.liveA); err != nil {
		t.Fatal(err)
	}
	if after := unitOf(t, f.reader, rollout(threadA)); after.Key != before.Key {
		t.Fatalf("moving the rollout changed its key")
	}
}

func TestTheStoreCountsEveryRequestOnceAcrossAllFixtures(t *testing.T) {
	f := newFixture(t)
	cache, err := scan.NewCacheFor([]source.Reader{f.reader}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Update(nil); err != nil {
		t.Fatal(err)
	}
	// A's cut line completes, and B is rewritten compressed with the same content in two frames.
	write(t, f.liveA, []byte(threadALines+threadALast+"\n"))
	half := strings.Index(threadBLines, "\n") + 1
	write(t, filepath.Join(f.home, "archived_sessions", rollout(threadB)+".zst"),
		append(compress(t, threadBLines[:half]), compress(t, threadBLines[half:])...))
	st, err := cache.Update(nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unreadable != 0 || len(st.Entries) != 4 || st.Changed != 2 {
		t.Fatalf("unreadable %d (%v), entries %d", st.Unreadable, st.FileErr, len(st.Entries))
	}
	var cursors []source.Cursor
	if err := st.Shards(func(s *scan.Shard) error {
		if s.Project != "other" || s.Scope != "main" || len(s.Session) != 8 {
			t.Errorf("identity %q %q %q", s.Project, s.Session, s.Scope)
		}
		cursors = append(cursors, source.Cursor{Events: s.Events})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Hand count: A 60+90+150+100 / 40+60 / 10+20+5+5, B 200+300 / 800+800 / 50+70, C 10/0/5, D's own 200/0/10.
	if n, unique := replay(cursors); unique != 8 || n != [5]int64{1110, 0, 0, 1700, 175} {
		t.Fatalf("store total %v over %d calls; want [1110 0 0 1700 175] over 8", n, unique)
	}
}

func TestTheReaderOpensOnlyRolloutFiles(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"auth.json", "config.toml", "state_5.sqlite", "history.jsonl", "session_index.jsonl"} {
		write(t, filepath.Join(f.home, name), []byte("{}\n"))
		if _, _, err := source.Open(f.reader.Roots(), filepath.Join(f.home, name)); !errors.Is(err, source.ErrOutsideRoots) {
			t.Errorf("%s: %v, want ErrOutsideRoots", name, err)
		}
	}
	planted := filepath.Join(f.home, "archived_sessions", "auth.json")
	write(t, planted, []byte("{}\n"))
	if _, _, err := source.Open(f.reader.Roots(), planted); !errors.Is(err, source.ErrForbiddenName) {
		t.Errorf("planted auth.json: %v, want ErrForbiddenName", err)
	}
	for _, rel := range []string{"archived_sessions/notes.jsonl", "archived_sessions/rollout-x.json",
		"sessions/rollout-top.jsonl", "sessions/2026/05/19/rollout-x.jsonl.zst", "sessions/2026/5/19/rollout-y.jsonl",
		"archived_sessions/rollout-auth-token.jsonl"} {
		write(t, filepath.Join(f.home, rel), []byte("{}\n"))
	}
	if err := os.Symlink(filepath.Join(f.home, "auth.json"),
		filepath.Join(f.home, "archived_sessions", rollout(threadA[:35]+"f"))); err != nil {
		t.Fatal(err)
	}
	d, err := f.reader.Discover()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range d.Units {
		names = append(names, u.Rel)
	}
	if len(d.Units) != 5 {
		t.Fatalf("units %v; want the 4 fixtures and the rollout-auth-token name, which Open refuses", names)
	}
	for _, u := range d.Units {
		if strings.Contains(u.Rel, "auth") {
			if _, _, err := source.Open(f.reader.Roots(), u.Path); !errors.Is(err, source.ErrForbiddenName) {
				t.Errorf("%s: %v, want ErrForbiddenName", u.Rel, err)
			}
		}
	}
}

func TestModelLabelsAcceptOpenAINamesOnly(t *testing.T) {
	for in, want := range map[any]string{"gpt-5.1-codex-max": "gpt-5.1-codex-max", "o4-mini": "o4-mini",
		"gpt-4o-2024-08-06": "gpt-4o-2024-08-06", "codex-mini-latest": "codex-mini-latest", "claude-opus-4": "unknown",
		"/Users/me/secret": "unknown", 5.0: "unknown", "gpt-5 hello": "unknown", "codex": "codex"} {
		if got := modelLabel(in); got != want || got != labels.Model(clients.Codex, in) {
			t.Errorf("%v: %q, want %q", in, got, want)
		}
	}
}

func TestASmallRolloutIsUnreadableOnlyWhenNoRecordTypeIsKnown(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.home, "archived_sessions", rollout(threadE))
	write(t, path, []byte(lines(meta(threadE, ""), `{"timestamp":"2026-05-19T11:00:00Z","type":"turn_v2","payload":{}}`)))
	if err := readErr(t, f.reader, unitOf(t, f.reader, filepath.Base(path))); !errors.Is(err, source.ErrUnrecognised) {
		t.Fatalf("a small drifted rollout: %v", err)
	}
	known := lines(meta(threadE, ""), turn("2026-05-19T10:00:00Z", "gpt-5"))
	for _, content := range []string{lines(meta(threadE, "")), known} {
		write(t, path, []byte(content))
		if err := readErr(t, f.reader, unitOf(t, f.reader, filepath.Base(path))); err != nil {
			t.Fatalf("%q: %v", content, err)
		}
	}
}
