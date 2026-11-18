package transcripts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"benchwatch/server/internal/guardlog"
)

var guardTables = []string{"guard_call_hourly", "guard_call_latency_daily", "guard_rule_daily", "guard_log_state"}

func emptyGuard(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	for _, table := range guardTables {
		if _, err := conn.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatalf("empty %s: %v", table, err)
		}
	}
}

// failingDB breaks one step of the pass's transaction: the first Exec whose text holds failOn, the bucket lookup
// (queryFails or emptyBuckets), or the commit that reaches the server and then reports an error (commitLost).
type failingDB struct {
	*pgx.Conn
	failOn       string
	queryFails   bool
	emptyBuckets bool
	commitLost   bool
}

func (d failingDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.Conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failingTx{Tx: tx, d: d}, nil
}

type failingTx struct {
	pgx.Tx
	d failingDB
}

func (t failingTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if t.d.failOn != "" && strings.Contains(sql, t.d.failOn) {
		return pgconn.CommandTag{}, errors.New("forced write failure")
	}
	return t.Tx.Exec(ctx, sql, args...)
}

func (t failingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "guard_latency_buckets") {
		if t.d.queryFails {
			return nil, errors.New("forced bucket failure")
		}
		if t.d.emptyBuckets {
			return t.Tx.Query(ctx, "SELECT lo_ms, hi_ms FROM guard_latency_buckets WHERE false")
		}
	}
	return t.Tx.Query(ctx, sql, args...)
}

func (t failingTx) Commit(ctx context.Context) error {
	if err := t.Tx.Commit(ctx); err != nil {
		return err
	}
	if t.d.commitLost {
		return errors.New("connection lost after commit")
	}
	return nil
}

func guardEntry(ts, tool, d string, ms int, extra string) string {
	return fmt.Sprintf(`{"v":1,"ts":%q,"tool":%q,"eng":"go","d":%q,"ms":%d,"sub":false%s}`+"\n", ts, tool, d, ms, extra)
}

// guardFigures is every stored figure of the guard log tables, as one comparable string.
func guardFigures(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var out strings.Builder
	for _, q := range []string{
		"SELECT coalesce(string_agg(concat_ws(',', day, hour, tool, decision, sub, calls, time_caps, fail_closed," +
			" sub_deny), ';' ORDER BY day, hour, tool, decision, sub), '') FROM guard_call_hourly",
		"SELECT coalesce(string_agg(concat_ws(',', day, tool, lo_ms, calls), ';' ORDER BY day, tool, lo_ms), '')" +
			" FROM guard_call_latency_daily",
		"SELECT coalesce(string_agg(concat_ws(',', day, tool, decision, rule, calls), ';' ORDER BY rule), '')" +
			" FROM guard_rule_daily",
		"SELECT coalesce((SELECT concat_ws(',', files, malformed, oversized, bad_version, bad_rule, not_regular," +
			" truncated, last_deny_at, last_deny_tool, last_deny_rule) FROM guard_log_state), 'none')",
	} {
		var s string
		if err := conn.QueryRow(context.Background(), q).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		out.WriteString(s + "\n")
	}
	return out.String()
}

func guardCalls(t *testing.T, conn *pgx.Conn) int64 {
	t.Helper()
	var n int64
	if err := conn.QueryRow(context.Background(),
		"SELECT coalesce(sum(calls), 0)::bigint FROM guard_call_hourly").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGuardPassesCountEachLineOnceAcrossFailuresAndRestarts(t *testing.T) {
	conn := testConn(t)
	emptyGuard(t, conn)
	t.Cleanup(func() { emptyGuard(t, conn) })
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "SET TIME ZONE 'UTC'"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "guard.log")
	loc := time.FixedZone("plus2", 2*3600)
	newGuard := func(db guardlog.DB) *Guard {
		return &Guard{Dir: dir, DB: db, Loc: loc, Now: func() time.Time { return time.Unix(1_800_000_000, 0) }}
	}

	absent := &Guard{Dir: filepath.Join(dir, "absent"), DB: conn}
	if absent.Tick(ctx); absent.Status().State != GuardUnavailable || guardFigures(t, conn) != "\n\n\nnone\n" {
		t.Fatalf("missing log: %+v, stored %q", absent.Status(), guardFigures(t, conn))
	}

	appendLines(t, log,
		guardEntry("2026-11-15T21:30:00.000Z", "bash", "allow", 3, ""),
		guardEntry("2026-11-16T08:00:00.000Z", "file", "deny", 40, `,"rule":"file.env"`),
		"not json\n",
		guardEntry("2026-11-16T08:05:00.000Z", "bash", "ask", 120, `,"flags":["time_cap"]`))
	g := newGuard(conn)
	g.Tick(ctx)
	if g.Status().State != GuardOK || guardCalls(t, conn) != 3 {
		t.Fatalf("first pass: %+v, %d calls", g.Status(), guardCalls(t, conn))
	}
	want := "2026-11-15,23,bash,allow,f,1,0,0,0;2026-11-16,10,bash,ask,f,1,1,0,0;2026-11-16,10,file,deny,f,1,0,0,0\n" +
		"2026-11-15,bash,3.000,1;2026-11-16,bash,100.000,1;2026-11-16,file,30.000,1\n" +
		"2026-11-16,file,deny,file.env,1\n"
	if got := guardFigures(t, conn); !strings.HasPrefix(got, want) ||
		!strings.Contains(got, ",1,0,0,0,0,0,2026-11-16 08:00:00+00,file,file.env") {
		t.Fatalf("stored after the first pass:\n%s\nwant prefix\n%s", got, want)
	}
	files, err := guardlog.Load(ctx, conn)
	if st, _ := os.Stat(log); err != nil || len(files) != 1 || files[0].Offset != st.Size() || files[0].Ino == 0 {
		t.Fatalf("stored positions %+v, %v", files, err)
	}

	appendLines(t, log, guardEntry("2026-11-16T09:00:00.000Z", "bash", "deny", 7, `,"rule":"bash.rm-rf"`),
		"{broken\n", guardEntry("2026-11-16T07:00:00.000Z", "file", "deny", 1, ""))
	before := guardFigures(t, conn)
	for _, failOn := range []string{"INSERT INTO guard_call_hourly", "guard_log_state", "guard_rule_daily",
		"guard_call_latency_daily"} {
		g.DB = failingDB{Conn: conn, failOn: failOn}
		g.Tick(ctx)
		if g.Status().State != GuardFailed || g.Status().ErrorCode == "" {
			t.Fatalf("failing %s: %+v", failOn, g.Status())
		}
		if got := guardFigures(t, conn); got != before {
			t.Fatalf("a failed write on %s changed the store:\n%s\nwas\n%s", failOn, got, before)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	g.DB = testConn(t) // pgx closes a connection whose context is cancelled
	if g.Tick(cancelled); g.Status().State != GuardFailed || guardFigures(t, conn) != before {
		t.Fatalf("cancelled pass: %+v", g.Status())
	}
	g.DB = conn
	g.Tick(ctx)
	if g.Status().State != GuardOK || guardCalls(t, conn) != 5 {
		t.Fatalf("retry after failures: %+v, %d calls, want 5", g.Status(), guardCalls(t, conn))
	}
	got := guardFigures(t, conn)
	if !strings.Contains(got, ",2,0,0,0,0,0,2026-11-16 09:00:00+00,bash,bash.rm-rf") {
		t.Fatalf("skip counters or last deny after the retry:\n%s", got)
	}

	restarted := newGuard(conn)
	if restarted.Tick(ctx); restarted.Status().State != GuardOK || guardFigures(t, conn) != got {
		t.Fatalf("a restart re-counted stored lines:\n%s\nwas\n%s", guardFigures(t, conn), got)
	}
	appendLines(t, log, guardEntry("2026-11-16T09:30:00.000Z", "bash", "allow", 2, ""))
	again := newGuard(conn)
	if again.Tick(ctx); guardCalls(t, conn) != 6 {
		t.Fatalf("after a restart and a new line: %d calls, want 6", guardCalls(t, conn))
	}

	bad := `UPDATE guard_log_state SET files = '[{"dev":1,"ino":2,"first":"zz","off":0}]'`
	if _, err := conn.Exec(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := guardlog.Load(ctx, conn); !errors.Is(err, guardlog.ErrState) {
		t.Fatalf("a bad stored position: %v", err)
	}
	broken := newGuard(conn)
	if broken.Tick(ctx); broken.Status().State != GuardFailed || broken.Status().ErrorCode != "EGUARDSTATE" ||
		guardCalls(t, conn) != 6 {
		t.Fatalf("bad state row: %+v, %d calls", broken.Status(), guardCalls(t, conn))
	}
}

func guardLines(n int, from int) []string {
	var out []string
	for i := from; i < from+n; i++ {
		out = append(out, guardEntry(fmt.Sprintf("2026-11-16T08:%02d:00.000Z", i), "bash", "allow", 3, ""))
	}
	return out
}

func TestACommitThatReachedTheServerIsNotCountedAgain(t *testing.T) {
	conn := testConn(t)
	emptyGuard(t, conn)
	t.Cleanup(func() { emptyGuard(t, conn) })
	dir := t.TempDir()
	log := filepath.Join(dir, "guard.log")
	appendLines(t, log, guardLines(3, 0)...)
	g := &Guard{Dir: dir, DB: failingDB{Conn: conn, commitLost: true}, Loc: time.UTC}
	g.Tick(context.Background())
	if g.Status().State != GuardFailed || guardCalls(t, conn) != 3 {
		t.Fatalf("lost commit: %+v, %d calls stored", g.Status(), guardCalls(t, conn))
	}
	g.DB = conn
	g.Tick(context.Background())
	if g.Status().State != GuardOK || guardCalls(t, conn) != 3 {
		t.Fatalf("next pass: %+v, %d calls, want 3", g.Status(), guardCalls(t, conn))
	}
	appendLines(t, log, guardLines(1, 3)...)
	if g.Tick(context.Background()); guardCalls(t, conn) != 4 {
		t.Fatalf("a new line after it: %d calls, want 4", guardCalls(t, conn))
	}
}

func TestAnyStepOfTheWriteFailingLeavesTheStoreAsItWasAndTheRetryCountsOnce(t *testing.T) {
	conn := testConn(t)
	emptyGuard(t, conn)
	t.Cleanup(func() { emptyGuard(t, conn) })
	dir := t.TempDir()
	appendLines(t, filepath.Join(dir, "guard.log"), guardLines(2, 0)...)
	for name, db := range map[string]failingDB{
		"first statement":     {Conn: conn, failOn: "INSERT INTO guard_call_hourly"},
		"bucket lookup":       {Conn: conn, queryFails: true},
		"empty bucket table":  {Conn: conn, emptyBuckets: true},
		"state row statement": {Conn: conn, failOn: "guard_log_state"},
	} {
		g := &Guard{Dir: dir, DB: db, Loc: time.UTC}
		g.Tick(context.Background())
		if g.Status().State != GuardFailed || g.Status().ErrorCode == "" || guardFigures(t, conn) != "\n\n\nnone\n" {
			t.Fatalf("%s: %+v, stored %q", name, g.Status(), guardFigures(t, conn))
		}
		if name == "empty bucket table" && g.Status().ErrorCode != "EGUARDSTATE" {
			t.Fatalf("%s: code %q", name, g.Status().ErrorCode)
		}
		g.DB = conn
		g.Tick(context.Background())
		if g.Status().State != GuardOK || guardCalls(t, conn) != 2 {
			t.Fatalf("%s retry: %+v, %d calls, want 2", name, g.Status(), guardCalls(t, conn))
		}
		emptyGuard(t, conn)
	}
}

func TestRestartsWithSeveralStoredFilesCountEachLineOnce(t *testing.T) {
	conn := testConn(t)
	emptyGuard(t, conn)
	t.Cleanup(func() { emptyGuard(t, conn) })
	ctx := context.Background()
	dir := t.TempDir()
	cur := filepath.Join(dir, "guard.log")
	rename := func(from, to string) {
		t.Helper()
		if err := os.Rename(filepath.Join(dir, from), filepath.Join(dir, to)); err != nil {
			t.Fatal(err)
		}
	}
	tick := func() *Guard {
		t.Helper()
		g := &Guard{Dir: dir, DB: conn, Loc: time.UTC}
		if g.Tick(ctx); g.Status().State != GuardOK {
			t.Fatalf("pass: %+v", g.Status())
		}
		return g
	}
	appendLines(t, cur, guardLines(2, 0)...)
	tick()
	rename("guard.log", "guard.log.1")
	appendLines(t, cur, guardLines(1, 2)...)
	tick()
	if n := guardCalls(t, conn); n != 3 {
		t.Fatalf("after the first rotation: %d calls, want 3", n)
	}
	appendLines(t, cur, guardLines(1, 3)...)
	rename("guard.log.1", "guard.log.2")
	rename("guard.log", "guard.log.1")
	appendLines(t, cur, guardLines(1, 4)...)
	tick()
	files, err := guardlog.Load(ctx, conn)
	if n := guardCalls(t, conn); err != nil || n != 5 || len(files) != 3 {
		t.Fatalf("after the second rotation: %d calls, %d positions, %v; want 5 and 3", n, len(files), err)
	}
	before := guardFigures(t, conn)
	if tick(); guardFigures(t, conn) != before {
		t.Fatalf("a restart without new lines changed the store:\n%s\nwas\n%s", guardFigures(t, conn), before)
	}
}

func TestSaveGuardLogStateKeepsTheLaterDenyAndReplacesNotRegular(t *testing.T) {
	conn := testConn(t)
	emptyGuard(t, conn)
	t.Cleanup(func() { emptyGuard(t, conn) })
	ctx := context.Background()
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	state := func() string {
		t.Helper()
		var s string
		if err := conn.QueryRow(ctx, "SELECT concat_ws(',', malformed, not_regular, truncated, last_deny_at, last_deny_tool,"+
			" coalesce(last_deny_rule, '-')) FROM guard_log_state").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	write := func(p guardlog.Pass) {
		t.Helper()
		if err := guardlog.Write(ctx, conn, p, t0); err != nil {
			t.Fatal(err)
		}
	}
	write(guardlog.Pass{Skipped: guardlog.Skipped{Malformed: 1, NotRegular: 2, Truncated: 1},
		Deny: &guardlog.Deny{At: t0.Add(time.Hour), Tool: "bash", Rule: "bash.rm-rf"}})
	if got, want := state(), "1,2,1,2026-11-16 09:00:00+00,bash,bash.rm-rf"; got != want {
		t.Fatalf("first pass %q, want %q", got, want)
	}
	write(guardlog.Pass{Skipped: guardlog.Skipped{Malformed: 2, NotRegular: 1, Truncated: 2},
		Deny: &guardlog.Deny{At: t0, Tool: "file", Rule: "file.env"}})
	if got, want := state(), "3,1,3,2026-11-16 09:00:00+00,bash,bash.rm-rf"; got != want {
		t.Fatalf("an older deny: %q, want %q (notRegular replaced, truncated added, deny kept)", got, want)
	}
	write(guardlog.Pass{})
	if got, want := state(), "3,0,3,2026-11-16 09:00:00+00,bash,bash.rm-rf"; got != want {
		t.Fatalf("no deny and no refusals: %q, want %q", got, want)
	}
	write(guardlog.Pass{Deny: &guardlog.Deny{At: t0.Add(2 * time.Hour), Tool: "file"}})
	if got, want := state(), "3,0,3,2026-11-16 10:00:00+00,file,-"; got != want {
		t.Fatalf("a later deny without a rule: %q, want %q (the rule is cleared)", got, want)
	}
}

func TestGoRulePatternAndTheSQLCheckAgree(t *testing.T) {
	conn := testConn(t)
	for _, v := range []string{"bash.rm-rf", "file.env", "a", "0", "a.b_c-d", "", "A", "-x", ".x", "a b", "é", "a/b", "a\n",
		"\na", strings.Repeat("a", 48), strings.Repeat("a", 49), "a_b", "a,b", "ａ"} {
		var sql bool
		if err := conn.QueryRow(context.Background(), "SELECT is_guard_rule_label($1)", v).Scan(&sql); err != nil {
			t.Fatal(err)
		}
		if got := guardlog.RulePattern.MatchString(v); got != sql {
			t.Errorf("%q: Go %v, SQL %v", v, got, sql)
		}
	}
}
