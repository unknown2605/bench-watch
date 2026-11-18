package server

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/transcripts"
)

var guardCanary = regexp.MustCompile(`(?i)canary`)

func guardLine(at time.Time, tool, d string, ms int, sub bool, extra string) string {
	return fmt.Sprintf(`{"v":1,"ts":%q,"tool":%q,"eng":"go","d":%q,"ms":%d,"sub":%t%s}`+"\n",
		at.UTC().Format("2006-01-02T15:04:05.000Z"), tool, d, ms, sub, extra)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}

// captureLogs collects what the log package and slog's default logger print until the test ends.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	out := &lockedBuffer{}
	prevSlog, prevOut, prevFlags := slog.Default(), log.Writer(), log.Flags()
	log.SetOutput(out)
	slog.SetDefault(slog.New(slog.NewTextHandler(out, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return out
}

func near(got *float64, want float64) bool { return got != nil && math.Abs(*got-want) < 1e-6 }

func TestGuardLogFiguresReachTheAPIAndNoLineTextIsKept(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	logged := captureLogs(t)

	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.Local)
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	at := func(days, hour, minute int) time.Time {
		return time.Date(y, m, d-days, hour, minute, 0, 0, time.Local)
	}
	big := `,"pad":"CANARYoversized` + strings.Repeat("x", 512) + `"`
	dir := t.TempDir()
	lines := []string{
		guardLine(at(0, 1, 15), "bash", "allow", 1, false, ""),
		guardLine(at(0, 1, 20), "bash", "allow", 1, true, `,"note":"CANARYunknown_key","cwd":"/Users/CANARYcwd"`),
		guardLine(at(1, 12, 0), "bash", "allow", 1, false, ""),
		guardLine(at(0, 3, 0), "bash", "deny", 120, true, `,"flags":["sub_deny"],"rule":"bash.rm-rf"`),
		guardLine(at(0, 3, 10), "file", "ask", 40000, false, `,"flags":["fail_closed","time_cap"],"rule":"file.env"`),
		guardLine(at(0, 5, 0), "file", "ask", 5, false, `,"rule":"CANARY bad rule"`),
		"CANARYmalformed line\n",
		guardLine(at(0, 6, 0), "bash", "allow", 1, false, big),
		guardLine(at(0, 6, 0), "bash", "deny", 1, false, `,"flags":["CANARYflag"]`),
		strings.Replace(guardLine(at(0, 6, 0), "bash", "deny", 1, false, `,"rule":"canary.v2"`), `"v":1`, `"v":2`, 1),
	}
	if err := os.WriteFile(filepath.Join(dir, "guard.log"), []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, h := newServer(t, pool, memoryRunner(now, 5, 2*time.Second))
	srv.Now = func() time.Time { return now }
	g := &transcripts.Guard{Dir: dir, DB: pool, Loc: time.Local}
	srv.Guard = g
	g.Tick(context.Background())
	if g.Status().State != transcripts.GuardOK {
		t.Fatalf("collector %+v", g.Status())
	}

	rec := get(h, AllowedHosts[0], "/api/guard?range=7d")
	body := rec.Body.Bytes()
	out := decode[api.GuardResponse](t, h, "/api/guard?range=7d")
	if out.Log.State != api.GuardResponseLogStateOk || out.Log.LastReadAt == nil || out.Calls.Value != 6 {
		t.Fatalf("log %+v calls %+v", out.Log, out.Calls)
	}
	sk := out.Log.Skipped
	if sk.Malformed != 2 || sk.Oversized != 1 || sk.BadVersion != 1 || sk.BadRule != 1 || sk.NotRegular != 0 {
		t.Fatalf("skipped %+v", sk)
	}
	if dc := out.Decisions; dc == nil || dc.Allow != 3 || dc.Ask != 2 || dc.Deny != 1 || dc.FailClosed != 1 ||
		dc.TimeCap != 1 || dc.SubDeny != 1 {
		t.Fatalf("decisions %+v", out.Decisions)
	}
	if out.SubAgent == nil || out.SubAgent.Calls != 6 || out.SubAgent.Sub != 2 {
		t.Fatalf("sub-agent %+v", out.SubAgent)
	}
	if len(out.ByTool) != 2 {
		t.Fatalf("by tool %+v", out.ByTool)
	}
	b, f := out.ByTool[0], out.ByTool[1]
	// bash: 3 calls in [1, 2) and 1 in [100, 150); file: 1 in [5, 7) and 1 in the open [30000, -) bucket.
	if b.Tool != "bash" || b.Calls != 4 || b.Ask != 0 || b.Deny != 1 || !near(b.P50Ms, 1+2.0/3) || !near(b.P95Ms, 140) ||
		!near(b.P99Ms, 148) {
		t.Fatalf("bash %+v p50 %v p95 %v p99 %v", b, *b.P50Ms, *b.P95Ms, *b.P99Ms)
	}
	if f.Tool != "file" || f.Calls != 2 || f.Ask != 2 || f.Deny != 0 || !near(f.P50Ms, 7) || !near(f.P95Ms, 30000) ||
		!near(f.P99Ms, 30000) {
		t.Fatalf("file %+v", f)
	}
	if len(out.CallsByDay) != 2 || out.CallsByDay[0].Calls != 1 || out.CallsByDay[1].Calls != 5 ||
		out.CallsByDay[1].Ask != 2 || out.CallsByDay[1].Deny != 1 ||
		out.CallsByDay[1].Day.Format(time.DateOnly) != today.Format(time.DateOnly) {
		t.Fatalf("by day %+v", out.CallsByDay)
	}
	if out.Hours == nil || len(*out.Hours) != 24 {
		t.Fatalf("hours %+v", out.Hours)
	}
	for _, hr := range *out.Hours {
		want := map[int32][2]int64{1: {2, 0}, 3: {2, 1}, 5: {1, 0}}[hr.Hour]
		if hr.Calls != want[0] || hr.Deny != want[1] {
			t.Fatalf("hour %d: %+v, want %v", hr.Hour, hr, want)
		}
	}
	if len(out.Rules) != 2 || out.Rules[0].Rule != "bash.rm-rf" || out.Rules[0].Decision != "deny" ||
		out.Rules[0].Calls != 1 || out.Rules[1].Rule != "file.env" || out.Rules[1].Tool != "file" {
		t.Fatalf("rules %+v", out.Rules)
	}
	lb := out.LastBlock
	if lb == nil || !lb.At.Equal(at(0, 3, 0)) || lb.Tool != "bash" || lb.Rule == nil || *lb.Rule != "bash.rm-rf" {
		t.Fatalf("last block %+v", lb)
	}

	if guardCanary.Match(body) {
		t.Fatalf("line text in /api/guard: %s", body)
	}
	if guardCanary.Match(logged.Bytes()) {
		t.Fatalf("line text in the log: %s", logged.Bytes())
	}
	ctx := context.Background()
	rows, err := pool.Query(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'"+
		" AND table_type = 'BASE TABLE'")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if len(tables) < len(fixtureTables) {
		t.Fatalf("tables %v", tables)
	}
	for _, table := range tables {
		var text string
		q := "SELECT coalesce(string_agg(t::text, E'\\n'), '') FROM " + pgx.Identifier{table}.Sanitize() + " t"
		if err := pool.QueryRow(ctx, q).Scan(&text); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if guardCanary.MatchString(text) || strings.Contains(text, "xxxx") {
			t.Fatalf("line text stored in %s", table)
		}
	}
}

func TestMissingGuardLogGivesNullFigures(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.Local)
	srv, h := newServer(t, pool, memoryRunner(now, 5, 2*time.Second))
	srv.Now = func() time.Time { return now }
	g := &transcripts.Guard{Dir: filepath.Join(t.TempDir(), "absent"), DB: pool}
	srv.Guard = g
	g.Tick(context.Background())
	if g.Status().State != transcripts.GuardUnavailable {
		t.Fatalf("collector %+v", g.Status())
	}
	out := decode[api.GuardResponse](t, h, "/api/guard?range=7d")
	if out.Log.State != api.GuardResponseLogStateAbsent || out.Log.LastReadAt != nil || out.Decisions != nil ||
		out.SubAgent != nil || out.Hours != nil || out.LastBlock != nil || len(out.ByTool) != 0 ||
		len(out.CallsByDay) != 0 || len(out.Rules) != 0 {
		t.Fatalf("missing log: %+v", out)
	}
}

// guardAPI serves /api/guard from a fresh server whose clock is frozen at now.
func guardAPI(t *testing.T, pool *pgxpool.Pool, now time.Time) (*Server, http.Handler) {
	t.Helper()
	srv, h := newServer(t, pool, memoryRunner(now, 5, 2*time.Second))
	srv.Now = func() time.Time { return now }
	return srv, h
}

func guardAt(now time.Time, daysBack int) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d-daysBack, 12, 0, 0, 0, time.Local)
}

func tick(t *testing.T, srv *Server, dir string, pool *pgxpool.Pool) *transcripts.Guard {
	t.Helper()
	g := &transcripts.Guard{Dir: dir, DB: pool, Loc: time.Local}
	srv.Guard = g
	g.Tick(context.Background())
	return g
}

func TestGuardLogStateAndCodeThroughTheAPI(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.Local)
	srv, h := guardAPI(t, pool, now)
	state := func() api.GuardResponse { return decode[api.GuardResponse](t, h, "/api/guard?range=7d") }
	want := func(name string, out api.GuardResponse, st api.GuardResponseLogState, code string) {
		t.Helper()
		got := ""
		if out.Log.Code != nil {
			got = *out.Log.Code
		}
		if out.Log.State != st || got != code {
			t.Fatalf("%s: state %q code %q, want %q %q", name, out.Log.State, got, st, code)
		}
	}
	line := func(days int) string { return guardLine(guardAt(now, days), "bash", "allow", 3, false, "") }
	good := t.TempDir()
	if err := os.WriteFile(filepath.Join(good, "guard.log"), []byte(line(0)+line(1)), 0o600); err != nil {
		t.Fatal(err)
	}

	srv.Guard = nil
	want("no collector", state(), api.GuardResponseLogStateDisabled, "")
	srv.Guard = &transcripts.Guard{Dir: good, DB: pool}
	want("before the first pass", state(), api.GuardResponseLogStateStarting, "")

	tick(t, srv, filepath.Join(t.TempDir(), "absent"), pool)
	want("empty database, no log", state(), api.GuardResponseLogStateAbsent, "")

	if g := tick(t, srv, good, pool); g.Status().State != transcripts.GuardOK {
		t.Fatalf("collector %+v", g.Status())
	}
	out := state()
	want("read", out, api.GuardResponseLogStateOk, "")
	if out.Calls.Value != 2 {
		t.Fatalf("calls %v", out.Calls)
	}

	tick(t, srv, filepath.Join(t.TempDir(), "absent"), pool)
	out = state()
	want("log gone, figures stored", out, api.GuardResponseLogStateMissing, "")
	if out.Calls.Value != 2 || len(out.ByTool) != 1 || out.Log.LastReadAt == nil {
		t.Fatalf("stored figures are not served while the log is missing: %+v", out)
	}

	if os.Getuid() != 0 {
		locked := t.TempDir()
		path := filepath.Join(locked, "guard.log")
		if err := os.WriteFile(path, []byte(line(0)), 0o000); err != nil {
			t.Fatal(err)
		}
		g := tick(t, srv, locked, pool)
		out = state()
		want("unreadable log", out, api.GuardResponseLogStateUnreadable, "EACCES")
		if g.Status().State != transcripts.GuardFailed || out.Calls.Value != 2 {
			t.Fatalf("collector %+v, calls %v: stored figures must still be served", g.Status(), out.Calls)
		}
	}
}

func TestRefusedGuardLogFilesGiveUnreadableAndRecover(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.Local)
	srv, h := guardAPI(t, pool, now)
	state := func() api.GuardResponse { return decode[api.GuardResponse](t, h, "/api/guard?range=7d") }
	line := func(days int) string { return guardLine(guardAt(now, days), "bash", "allow", 3, false, "") }
	other, dir := t.TempDir(), t.TempDir()
	target := filepath.Join(other, "real.log")
	if err := os.WriteFile(target, []byte(line(0)+line(1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cur := filepath.Join(dir, "guard.log")
	if err := os.Symlink(target, cur); err != nil {
		t.Fatal(err)
	}
	tick(t, srv, dir, pool)
	out := state()
	if out.Log.State != api.GuardResponseLogStateUnreadable || out.Log.Code == nil || *out.Log.Code != "ENOTREGULAR" ||
		out.Log.Skipped.NotRegular != 1 || out.Calls.Value != 0 {
		t.Fatalf("symlinked guard.log: %+v calls %v", out.Log, out.Calls)
	}
	if err := os.Remove(cur); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, []byte(line(0)), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &transcripts.Guard{Dir: dir, DB: pool, Loc: time.Local}
	srv.Guard = g
	g.Tick(context.Background())
	if out = state(); out.Log.State != api.GuardResponseLogStateOk || out.Log.Code != nil ||
		out.Log.Skipped.NotRegular != 0 || out.Calls.Value != 1 {
		t.Fatalf("a regular file after the link: %+v calls %v", out.Log, out.Calls)
	}

	if err := os.Symlink(target, filepath.Join(dir, "guard.log.1")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, []byte(line(0)+line(2)), 0o600); err != nil {
		t.Fatal(err)
	}
	g.Tick(context.Background())
	if out = state(); out.Log.State != api.GuardResponseLogStateUnreadable || out.Log.Code == nil ||
		*out.Log.Code != "ENOTREGULAR" || out.Log.Skipped.NotRegular != 1 || out.Calls.Value != 2 {
		t.Fatalf("symlinked guard.log.1: %+v calls %v; the regular file must still count", out.Log, out.Calls)
	}
}

func TestGuardPreviousPeriodCoversTheEqualPeriodBefore(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.Local)
	srv, h := guardAPI(t, pool, now)
	// The previous period is null before the first day with history, so one old usage row is needed.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO usage_daily VALUES ($1::date, 'benchwatch', 'main', 'unknown', 1, 0, 0, 0, 0, 0, 0, 0, 0, 0)`,
		guardAt(now, 60).Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for days, n := range map[int]int{0: 1, 1: 1, 6: 1, 7: 1, 10: 2, 13: 1, 14: 1} {
		for i := 0; i < n; i++ {
			lines = append(lines, guardLine(guardAt(now, days), "bash", "allow", 3, false, ""))
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "guard.log"), []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	tick(t, srv, dir, pool)
	for _, c := range []struct {
		rng       string
		cur, prev float64
	}{
		{"today", 1, 1},
		{"7d", 3, 4},
		{"14d", 7, 1},
	} {
		out := decode[api.GuardResponse](t, h, "/api/guard?range="+c.rng)
		if out.Calls.Value != c.cur || out.Calls.Previous == nil || *out.Calls.Previous != c.prev {
			prev := -1.0
			if out.Calls.Previous != nil {
				prev = *out.Calls.Previous
			}
			t.Errorf("range %s: %v calls, previous %v, want %v and %v", c.rng, out.Calls.Value, prev, c.cur, c.prev)
		}
	}
}
