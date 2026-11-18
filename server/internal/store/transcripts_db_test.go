package store

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

var transcriptTableNames = []string{"usage_daily", "context_band_daily", "compaction_daily", "compaction_events",
	"session_daily", "subagent_daily", "subagent_run_daily", "tool_result_daily", "tool_result_size_daily",
	"tool_result_top", "tool_error_daily"}

// One row per transcript table on day $1; the two hash keys are the day's digits padded to 16.
var seedTranscriptDay = []string{
	`INSERT INTO usage_daily VALUES ($1::date, 'benchwatch', 'main', 'unknown', 1, 0, 0, 0, 0, 0, 0, 0, 0, 0)`,
	`INSERT INTO context_band_daily VALUES ($1::date, 'benchwatch', 'main', 0, 100000, 1, 0, 0)`,
	`INSERT INTO compaction_daily VALUES ($1::date, 'benchwatch', 'main', 'auto', 1, 0)`,
	`INSERT INTO compaction_events VALUES (lpad(to_char($1::date, 'YYYYMMDD'), 16, '0'), $1::date::timestamptz,
		$1::date, 'benchwatch', '0a1b2c3d', 'main', 'auto', 0, NULL)`,
	`INSERT INTO session_daily VALUES ($1::date, 'benchwatch', '0a1b2c3d', 'main', 'unknown', 1, 0, 0, 0, 0, 0, 0,
		0, 0, $1::date::timestamptz, $1::date::timestamptz)`,
	`INSERT INTO subagent_daily VALUES ($1::date, 'benchwatch', '0a1b2c3d', 'worker', 'unknown', 1, 0, 0, 0, 0)`,
	`INSERT INTO subagent_run_daily VALUES ($1::date, 'benchwatch', '0a1b2c3d', 'worker', 1, 0, 0, 1)`,
	`INSERT INTO tool_result_daily VALUES ($1::date, 'benchwatch', 'main', 'Read', 1, 0, 0, 0, 0, 0)`,
	`INSERT INTO tool_result_size_daily VALUES ($1::date, 'benchwatch', 'main', 'Read', 0, 1000, 1, 0)`,
	`INSERT INTO tool_result_top VALUES (lpad(to_char($1::date, 'YYYYMMDD'), 16, '0'), $1::date, 'benchwatch',
		'main', 'Read', 0, 0, 0)`,
	`INSERT INTO tool_error_daily VALUES ($1::date, 'benchwatch', 'main', 'Read', 'other', 1)`,
}

func utcDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func rowsPerDay(t *testing.T, conn *pgx.Conn) map[time.Time]int64 {
	t.Helper()
	sql := ""
	for i, table := range transcriptTableNames {
		if i > 0 {
			sql += " UNION ALL "
		}
		sql += "SELECT day FROM " + pgx.Identifier{table}.Sanitize()
	}
	rows, err := conn.Query(context.Background(), "SELECT day, count(*) FROM ("+sql+") d GROUP BY day")
	if err != nil {
		t.Fatal(err)
	}
	out := map[time.Time]int64{}
	for rows.Next() {
		var day time.Time
		var n int64
		if err := rows.Scan(&day, &n); err != nil {
			t.Fatal(err)
		}
		out[utcDay(day)] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestClearTranscriptDaysKeepsEveryDayItDidNotRebuild(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	emptyTables(t, conn, transcriptTableNames...)
	t.Cleanup(func() { emptyTables(t, conn, transcriptTableNames...) })
	q := New(conn)

	today := utcDay(time.Now())
	recent, old := today.AddDate(0, 0, -1), []time.Time{today.AddDate(0, 0, -40), today.AddDate(0, 0, -41),
		today.AddDate(0, 0, -400)}
	seed := func(day time.Time) {
		t.Helper()
		for _, stmt := range seedTranscriptDay {
			if _, err := conn.Exec(ctx, stmt, day); err != nil {
				t.Fatalf("seed %s: %v", day.Format(time.DateOnly), err)
			}
		}
	}
	for _, day := range append([]time.Time{recent}, old...) {
		seed(day)
	}
	perDay := int64(len(transcriptTableNames))
	expect := func(why string, want map[time.Time]int64) {
		t.Helper()
		got := rowsPerDay(t, conn)
		for day, n := range want {
			if got[day] != n {
				t.Errorf("%s: %s holds %d rows, want %d", why, day.Format(time.DateOnly), got[day], n)
			}
		}
		if len(got) > len(want) {
			t.Errorf("%s: rows on %d days, want %d", why, len(got), len(want))
		}
	}
	all := func(recentRows int64) map[time.Time]int64 {
		m := map[time.Time]int64{recent: recentRows}
		for _, day := range old {
			m[day] = perDay
		}
		if recentRows == 0 {
			delete(m, recent)
		}
		return m
	}
	expect("seeded", all(perDay))

	n, err := q.ClearTranscriptDays(ctx, []time.Time{recent})
	if err != nil || n != perDay {
		t.Fatalf("a scan that rebuilt one day cleared %d rows, %v; want %d", n, err, perDay)
	}
	expect("a scan whose transcripts cover fewer days", all(0))
	seed(recent)

	n, err = q.ClearTranscriptDays(ctx, []time.Time{})
	if err != nil || n != 0 {
		t.Fatalf("a scan that saw no transcripts cleared %d rows, %v; want 0", n, err)
	}
	expect("a scan that saw no transcripts", all(perDay))

	n, err = q.ClearTranscriptDays(ctx, []time.Time{today.AddDate(0, 0, -2)})
	if err != nil || n != 0 {
		t.Fatalf("rebuilding a day with no stored rows cleared %d rows, %v; want 0", n, err)
	}
	expect("a rebuilt day with nothing stored", all(perDay))

	for _, bad := range []string{"SELECT clear_transcript_days(NULL)", "SELECT clear_transcript_days(ARRAY[NULL::date])"} {
		if _, err := conn.Exec(ctx, bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
	expect("refused calls", all(perDay))
}

func TestSessionSummaryCoversWholeHistoryAndMissingIsNoRow(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	tables := []string{"session_daily", "subagent_run_daily", "compaction_events"}
	emptyTables(t, conn, tables...)
	t.Cleanup(func() { emptyTables(t, conn, tables...) })
	q := New(conn)

	d1 := utcDay(time.Now()).AddDate(0, 0, -30)
	d2 := d1.AddDate(0, 0, 1)
	at := func(d time.Time, h int) time.Time { return d.Add(time.Duration(h) * time.Hour) }
	insert := `INSERT INTO session_daily (day, project, session, scope, model, calls, input_tokens, cache_write_tokens,
		cache_read_tokens, output_tokens, context_tokens, peak_context_tokens, last_context_tokens, cost_usd, first_at,
		last_at) VALUES ($1, 'benchwatch', $2, $3, $4, $5, 0, 0, 0, $6, $7, $8, $9, $10, $11, $12)`
	for _, r := range [][]any{
		{d1, "0a1b2c3d", "main", "claude-opus-5-5", 3, 10, 100, 50, 40, 0.5, at(d1, 10), at(d1, 11)},
		{d2, "0a1b2c3d", "main", "claude-sonnet-4-6", 5, 20, 200, 70, 60, 1.25, at(d2, 9), at(d2, 12)},
		{d2, "0a1b2c3d", "sub", "claude-opus-5-5", 4, 3, 30, 30, 30, 0.25, at(d2, 9), at(d2, 13)},
		{d2, "ffffffff", "main", "claude-opus-5-5", 9, 0, 0, 0, 0, 0, at(d2, 1), at(d2, 2)},
	} {
		if _, err := conn.Exec(ctx, insert, r...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO subagent_run_daily VALUES ($1, 'benchwatch', '0a1b2c3d', 'worker', 2, 0, 0, 4),
		($2, 'benchwatch', '0a1b2c3d', 'worker', 1, 0, 0, 1)`, d1, d2); err != nil {
		t.Fatal(err)
	}

	got, err := q.SessionSummaryByKey(ctx, SessionSummaryByKeyParams{Project: "benchwatch", Session: "0a1b2c3d"})
	if err != nil {
		t.Fatalf("a session stored over two days: %v", err)
	}
	if got.Calls != 12 || got.Tokens != 363 || got.PeakContextTokens != 70 || math.Abs(got.CostUsd-2) > 1e-9 ||
		got.Model != "claude-opus-5-5" || got.LastContextTokens != 60 || got.SubagentRuns != 3 || got.Compactions != 0 ||
		!got.FirstAt.Valid || !got.FirstAt.Time.Equal(at(d1, 10)) || !got.LastAt.Time.Equal(at(d2, 13)) {
		t.Errorf("summary %+v", got)
	}

	for _, key := range []SessionSummaryByKeyParams{{Project: "benchwatch", Session: "00000000"},
		{Project: "other", Session: "0a1b2c3d"}} {
		if _, err := q.SessionSummaryByKey(ctx, key); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("%+v: %v, want pgx.ErrNoRows (404)", key, err)
		}
	}
}

func TestGuardCallLatencyPercentilesInterpolateInsideOneBucket(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	emptyTables(t, conn, "guard_call_latency_daily")
	t.Cleanup(func() { emptyTables(t, conn, "guard_call_latency_daily") })
	q := New(conn)

	d1 := utcDay(time.Now()).AddDate(0, 0, -5)
	d2, outside := d1.AddDate(0, 0, 1), d1.AddDate(0, 0, -1)
	// Day 2 used other bounds, so two buckets start at 0 ms; the open top bucket reports its lower bound.
	if _, err := conn.Exec(ctx, `INSERT INTO guard_call_latency_daily (day, tool, lo_ms, hi_ms, calls) VALUES
		($1, 'bash', 0, 1, 2), ($1, 'bash', 1, 2, 4), ($2, 'bash', 0, 2, 4), ($3, 'bash', 5, 7, 100),
		($1, 'file', 0, 1, 1), ($2, 'file', 30000, NULL, 1)`, d1, d2, outside); err != nil {
		t.Fatal(err)
	}
	rows, err := q.GuardCallLatencyPercentiles(ctx, GuardCallLatencyPercentilesParams{DayFrom: d1, DayTo: d2})
	if err != nil {
		t.Fatal(err)
	}
	want := []GuardCallLatencyPercentilesRow{
		{GuardToolBash, 0.5, 1.5}, {GuardToolBash, 0.95, 1.875}, {GuardToolBash, 0.99, 1.975},
		{GuardToolFile, 0.5, 1}, {GuardToolFile, 0.95, 30000}, {GuardToolFile, 0.99, 30000},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want one per tool and quantile (%d): %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		r := rows[i]
		if r.Tool != w.Tool || math.Abs(r.Quantile-w.Quantile) > 1e-9 || math.Abs(r.Ms-w.Ms) > 1e-9 {
			t.Errorf("row %d: %+v, want %+v", i, r, w)
		}
	}

	rows, err = q.GuardCallLatencyPercentiles(ctx, GuardCallLatencyPercentilesParams{DayFrom: d2.AddDate(0, 0, 1),
		DayTo: d2.AddDate(0, 0, 2)})
	if err != nil || len(rows) != 0 {
		t.Errorf("a period with no calls: %d rows, %v; want none", len(rows), err)
	}
}
