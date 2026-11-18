package copilot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/source"
)

const sessA = "3f2a9c41-0000-4000-8000-000000000001"

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

// A cwd under the home folder gets a project label; the home folder itself is never read.
var cwd = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "work", "proj")
}()

func start(id string) string {
	return fmt.Sprintf(`{"id":"s-%s","type":"session.start","timestamp":"2026-10-10T08:00:00Z",`+
		`"data":{"sessionId":%q,"context":{"cwd":%q}}}`, id, id, cwd)
}

func usage(id string, in, read, write, out int64, nano string) string {
	cost := ""
	if nano != "" {
		cost = `,"copilotUsage":{"totalNanoAiu":` + nano + `}`
	}
	return fmt.Sprintf(`{"id":%q,"type":"session.usage_record","timestamp":"2026-10-10T08:01:00Z","data":{"usage":`+
		`{"model":"gpt-5","inputTokens":%d,"cacheReadTokens":%d,"cacheWriteTokens":%d,"outputTokens":%d%s}}}`,
		id, in, read, write, out, cost)
}

func shutdown(in, out int64) string {
	return fmt.Sprintf(`{"id":"sd","type":"session.shutdown","timestamp":"2026-10-10T09:00:00Z","data":`+
		`{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":%d,"outputTokens":%d}}}}}`, in, out)
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, r *Reader, from source.Cursor) source.Cursor {
	t.Helper()
	d, err := r.Discover()
	if err != nil || len(d.Units) != 1 {
		t.Fatalf("units %v, %v", d.Units, err)
	}
	f, _, err := source.Open(r.Roots(), d.Units[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cur, err := r.Read(f, d.Units[0], from, source.Options{MaxItems: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func sum(c source.Cursor) (n [5]int64, usd float64, priced int) {
	for _, e := range c.Events {
		for i := range n {
			n[i] += e.N[i]
		}
		if e.HasUSD {
			usd += e.USD
			priced++
		}
	}
	return n, usd, priced
}

func TestTheShutdownTotalIsNotAddedOnTopOfTheRequests(t *testing.T) {
	home := t.TempDir()
	events := filepath.Join(home, "session-state", sessA, eventsFile)
	write(t, events, lines(start(sessA), usage("u1", 1000, 600, 100, 50, "2500000000"),
		usage("u2", 200, 0, 0, 20, ""), shutdown(1200, 70)))
	r := New(home)
	cur := read(t, r, source.Cursor{})
	n, usd, priced := sum(cur)
	if len(cur.Events) != 2 || n != [5]int64{500, 100, 0, 600, 70} {
		t.Fatalf("events %d, counts %v; want 2 and [500 100 0 600 70]", len(cur.Events), n)
	}
	// 2.5e9 nano-AIU is 2.5 credits, $0.025; u2 recorded no cost and stays unpriced.
	if usd != 0.025 || priced != 1 {
		t.Errorf("usd %v over %d calls; want 0.025 over 1", usd, priced)
	}
	if cur.Skipped != 0 || cur.Lines != 4 {
		t.Errorf("lines %d skipped %d", cur.Lines, cur.Skipped)
	}

	// A resume appends more requests and a second cumulative shutdown; a re-sent u1 still counts once.
	appendTo(t, events, lines(usage("u3", 10, 0, 0, 1, "0"), usage("u1", 1000, 600, 100, 50, "2500000000"),
		shutdown(1210, 71)))
	cur = read(t, r, cur)
	n, usd, priced = sum(cur)
	if len(cur.Events) != 3 || n != [5]int64{510, 100, 0, 600, 71} || usd != 0.025 || priced != 2 {
		t.Errorf("after resume: %d events, %v, usd %v over %d", len(cur.Events), n, usd, priced)
	}
	if want := extract.HashID("copilot-cli:event:u1"); cur.Events[0].ID != want {
		t.Errorf("call id %q, want %q", cur.Events[0].ID, want)
	}
}

func TestIdentityAndAPartialLastLine(t *testing.T) {
	home := t.TempDir()
	events := filepath.Join(home, "session-state", sessA, eventsFile)
	write(t, events, lines(start(sessA))+usage("u1", 10, 0, 0, 1, "")[:40])
	r := New(home)
	d, _ := r.Discover()
	id, err := r.Identify(d.Units[0], nil)
	if err != nil || id.Session != "3f2a9c41" || id.Scope != "main" || id.Project != "work-proj" {
		t.Fatalf("identity %+v, %v", id, err)
	}
	cur := read(t, r, source.Cursor{})
	if len(cur.Events) != 0 || cur.Lines != 1 {
		t.Fatalf("partial line read: %+v", cur)
	}
	appendTo(t, events, usage("u1", 10, 0, 0, 1, "")[40:]+"\n")
	if cur = read(t, r, cur); len(cur.Events) != 1 {
		t.Errorf("the finished line was not read: %+v", cur)
	}
}

func TestTheReaderOpensOnlyEventLogs(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "session-state", sessA, eventsFile), lines(start(sessA)))
	r := New(home)
	for _, name := range []string{"config.json", "session-store.db", ".env", "github-token.json"} {
		write(t, filepath.Join(home, name), "{}\n")
		if _, _, err := source.Open(r.Roots(), filepath.Join(home, name)); !errors.Is(err, source.ErrOutsideRoots) {
			t.Errorf("%s: %v, want ErrOutsideRoots", name, err)
		}
	}
	for _, rel := range []string{"auth.json", "x/token.jsonl", "x/workspace.yaml", sessA + "/auth_token.jsonl"} {
		full := filepath.Join(home, "session-state", rel)
		write(t, full, "{}\n")
		if _, _, err := source.Open(r.Roots(), full); strings.Contains(rel, "auth") || strings.Contains(rel, "token") {
			if !errors.Is(err, source.ErrForbiddenName) {
				t.Errorf("%s: %v, want ErrForbiddenName", rel, err)
			}
		}
	}
	link := filepath.Join(home, "session-state", "y", eventsFile)
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "config.json"), link); err != nil {
		t.Fatal(err)
	}
	d, err := r.Discover()
	if err != nil || len(d.Units) != 1 || d.Units[0].Rel != filepath.Join("session-state", sessA, eventsFile) {
		t.Errorf("units %+v, %v; want only the event log", d.Units, err)
	}
}

func TestTheStoreKeepsTheRecordedCost(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "session-state", sessA, eventsFile), lines(start(sessA),
		usage("u1", 100, 0, 0, 5, "1e9"), shutdown(100, 5)))
	cache, err := scan.NewCacheFor([]source.Reader{New(home)}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := cache.Update(nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []extract.Event
	if err := st.Shards(func(s *scan.Shard) error { got = append(got, s.Events...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].HasUSD || got[0].USD != 0.01 {
		t.Errorf("stored events %+v; want one call at $0.01", got)
	}
}

func TestACostAddedToARepeatedCallReachesTheIncrementalBuild(t *testing.T) {
	home := t.TempDir()
	events := filepath.Join(home, "session-state", sessA, eventsFile)
	write(t, events, lines(start(sessA), usage("u1", 100, 0, 0, 5, "")))
	cache, err := scan.NewCacheFor([]source.Reader{New(home)}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Update(nil); err != nil {
		t.Fatal(err)
	}
	appendTo(t, events, lines(usage("u1", 100, 0, 0, 5, "1e9")))
	st, err := cache.Update(nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []extract.Event
	if err := st.Shards(func(s *scan.Shard) error { got = append(got, s.Events...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].HasUSD || got[0].USD != 0.01 {
		t.Fatalf("stored events %+v; want one call at $0.01", got)
	}
	for key := range st.Entries {
		if a, ok := st.AppendedTo(key); !ok || a.From != 0 {
			t.Errorf("appended %v from %d; want the cost-only change at event 0 rebuilt", ok, a.From)
		}
	}
}

func TestARenamedTokenFieldIsSkippedNotAZeroCall(t *testing.T) {
	home := t.TempDir()
	renamed := `{"id":"u2","type":"session.usage_record","timestamp":"2026-10-10T08:01:00Z","data":{"usage":` +
		`{"model":"gpt-5","input":10,"output":"3"}}}`
	write(t, filepath.Join(home, "session-state", sessA, eventsFile), lines(start(sessA), usage("u1", 10, 0, 0, 1, ""),
		renamed))
	cur := read(t, New(home), source.Cursor{})
	if len(cur.Events) != 1 || cur.Skipped != 1 {
		t.Fatalf("events %+v skipped %d", cur.Events, cur.Skipped)
	}
}

func TestAHugeRecordedCostSkipsItsCall(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "session-state", sessA, eventsFile), lines(start(sessA),
		usage("u1", 10, 0, 0, 1, "1e24")))
	cur := read(t, New(home), source.Cursor{})
	if len(cur.Events) != 0 || cur.Skipped != 1 {
		t.Fatalf("events %+v skipped %d", cur.Events, cur.Skipped)
	}
}

func TestAnUnparsableUsageTimestampCountsASkip(t *testing.T) {
	home := t.TempDir()
	bad := strings.Replace(usage("u1", 10, 0, 0, 1, ""), "2026-10-10T08:01:00Z", "yesterday", 1)
	write(t, filepath.Join(home, "session-state", sessA, eventsFile), lines(start(sessA), bad))
	cur := read(t, New(home), source.Cursor{})
	if len(cur.Events) != 1 || cur.Skipped != 1 {
		t.Fatalf("events %+v skipped %d", cur.Events, cur.Skipped)
	}
}

func TestIdentifyFailsUntilTheFirstRecordIsASessionStart(t *testing.T) {
	home := t.TempDir()
	events := filepath.Join(home, "session-state", sessA, eventsFile)
	r := New(home)
	for _, content := range []string{"", start(sessA)[:30], lines(usage("u1", 10, 0, 0, 1, ""))} {
		write(t, events, content)
		d, _ := r.Discover()
		if _, err := r.Identify(d.Units[0], nil); err == nil {
			t.Fatalf("%q: a default identity was given", content)
		}
	}
}

func TestALargeLogWithNoKnownUsageRecordIsUnreadable(t *testing.T) {
	home := t.TempDir()
	var b strings.Builder
	b.WriteString(start(sessA) + "\n")
	for b.Len() < source.FormatFloor {
		b.WriteString(`{"id":"x","type":"session.usage_v2","timestamp":"2026-10-10T08:01:00Z","data":{"n":1}}` + "\n")
	}
	write(t, filepath.Join(home, "session-state", sessA, eventsFile), b.String())
	r := New(home)
	d, _ := r.Discover()
	f, _, err := source.Open(r.Roots(), d.Units[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := r.Read(f, d.Units[0], source.Cursor{}, source.Options{MaxItems: 1000}); !errors.Is(err,
		source.ErrUnrecognised) {
		t.Fatalf("drifted log: %v", err)
	}
}

func TestASmallLogIsUnreadableOnlyWhenNoEventTypeIsKnown(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "session-state", sessA, eventsFile)
	write(t, path, lines(start(sessA), `{"id":"x","type":"telemetry.v2","timestamp":"2026-10-10T08:01:00Z","data":{}}`))
	r := New(home)
	d, _ := r.Discover()
	f, _, err := source.Open(r.Roots(), d.Units[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := r.Read(f, d.Units[0], source.Cursor{}, source.Options{MaxItems: 1000}); !errors.Is(err,
		source.ErrUnrecognised) {
		t.Fatalf("a small drifted log: %v", err)
	}
	write(t, path, lines(start(sessA), `{"id":"y","type":"user.message","timestamp":"2026-10-10T08:01:00Z","data":{}}`))
	read(t, r, source.Cursor{})
}
