package transcripts

import (
	"context"
	"fmt"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/testdb"
)

func noon(daysAgo int) int64 {
	y, m, d := time.Now().AddDate(0, 0, -daysAgo).Date()
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local).Unix()
}

func call(id string, ts int64, model string, input, cacheRead, output int64) extract.Event {
	return extract.Event{Kind: extract.Call, Ts: ts, ID: id, Text: model, N: [5]int64{input, 10, 0, cacheRead, output}}
}

func result(id string, ts int64, tool string, tokens int64, errClass string) extract.Event {
	return extract.Event{Kind: extract.Result, Ts: ts, ID: id, Text: tool, N: [5]int64{tokens}, Error: errClass}
}

func fixtureShards() []*scan.Shard {
	agent := "Explore"
	d1, d0 := noon(1), noon(0)
	return []*scan.Shard{
		{Client: clients.ClaudeCode, Project: "benchwatch", Session: "abcd1234", Scope: "main", Lines: 10, Events: []extract.Event{
			call("c1", d1, "claude-opus-5-5", 100, 1000, 50),
			result("1111111111111111", d1+1, "Read: whole", 400, ""),
			call("c2", d1+2, "claude-opus-5-5", 10, 2000, 20),
			result("2222222222222222", d1+3, "Bash: git", 200, "non-zero exit"),
			call("c3", d1+4, "claude-opus-5-5", 10, 3000, 20),
			{Kind: extract.Boundary, Ts: d1 + 5, ID: "3333333333333333", Text: "auto", N: [5]int64{150000}},
			call("c4", d0, "claude-opus-5-5", 5, 500, 5),
			call("c1", d1, "claude-opus-5-5", 100, 1000, 50),
		}},
		{Client: clients.ClaudeCode, Project: "benchwatch", Session: "abcd1234", Scope: "sub", AgentType: &agent, Events: []extract.Event{
			call("s1", d0+1, "claude-haiku-5-5", 50, 0, 10),
			call("s2", d0+2, "claude-haiku-5-5", 50, 100, 10),
			result("4444444444444444", d0+3, "/etc/passwd", 9, ""),
		}},
	}
}

func source(shards []*scan.Shard) aggregate.ShardSource {
	return func(visit func(*scan.Shard) error) error {
		for _, s := range shards {
			if err := visit(s); err != nil {
				return err
			}
		}
		return nil
	}
}

func build(t *testing.T, shards []*scan.Shard) *aggregate.Rows {
	t.Helper()
	rows, err := aggregate.BuildRows(source(shards), aggregate.MaxRunItems, nil, nil)
	if err != nil {
		t.Fatalf("build rows: %v", err)
	}
	return rows
}

func TestBuildRowsFollowsAggregateRules(t *testing.T) {
	rows := build(t, fixtureShards())
	if rows.UniqueCalls != 6 || rows.DuplicateLines != 1 {
		t.Errorf("unique calls %d dupes %d, want 6 and 1", rows.UniqueCalls, rows.DuplicateLines)
	}
	if len(rows.Days()) != 2 {
		t.Fatalf("days %v", rows.Days())
	}
	var weighted, results int64
	tools := map[string]bool{}
	for _, r := range rows.Results {
		weighted += r.WeightedTokens
		results += r.Results
		tools[r.Tool] = true
	}
	// Read's 400 is re-sent by c2 and c3, git's 200 by c3; the boundary closes the segment before c4.
	if weighted != 400*2+200 || results != 3 {
		t.Errorf("weighted %d results %d, want 1000 and 3", weighted, results)
	}
	if !tools["other"] || tools["/etc/passwd"] {
		t.Errorf("a label the schema refuses must become other: %v", tools)
	}
	if len(rows.Boundaries) != 1 || rows.Boundaries[0].TokensAfter == nil || *rows.Boundaries[0].TokensAfter != 515 {
		t.Errorf("boundary %+v, want tokens after 515 (context of c4)", rows.Boundaries)
	}
	if len(rows.SubagentRuns) != 1 || rows.SubagentRuns[0].Runs != 1 || rows.SubagentRuns[0].Calls != 2 {
		t.Errorf("subagent runs %+v", rows.SubagentRuns)
	}
	if len(rows.Errors) != 1 || len(rows.TopResults) != 3 {
		t.Errorf("errors %d top %d", len(rows.Errors), len(rows.TopResults))
	}
	again := build(t, fixtureShards())
	for day, d := range rows.Digests() {
		if again.Digests()[day] != d {
			t.Errorf("digest of %v differs between two identical builds", day)
		}
	}
	shards := fixtureShards()
	shards[1].Events = append(shards[1].Events, call("s3", noon(0)+9, "claude-haiku-5-5", 1, 1, 1))
	changed := build(t, shards).Digests()
	for day, d := range rows.Digests() {
		same := changed[day] == d
		if today := day.Equal(dayOfTime(time.Now())); same == today {
			t.Errorf("day %v: digest same=%v after a call was added today", day, same)
		}
	}
}

func testConn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := testdb.DSN(t)
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

var transcriptTables = []string{"usage_daily", "context_band_daily", "compaction_daily", "compaction_events",
	"session_daily", "subagent_daily", "subagent_run_daily", "tool_result_daily", "tool_result_size_daily",
	"tool_result_top", "tool_error_daily"}

func emptyTranscripts(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), "SELECT clear_transcript_days(ARRAY(SELECT DISTINCT day FROM usage_daily"+
		" UNION SELECT day FROM tool_result_daily UNION SELECT day FROM compaction_events))"); err != nil {
		t.Fatalf("empty: %v", err)
	}
}

func counts(t *testing.T, conn *pgx.Conn) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, table := range transcriptTables {
		var n int64
		if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

func TestWriteDaysReplacesOnlyRebuiltDays(t *testing.T) {
	conn := testConn(t)
	emptyTranscripts(t, conn)
	t.Cleanup(func() { emptyTranscripts(t, conn) })
	ctx := context.Background()
	cb, rb, err := Bands(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := aggregate.BuildRows(source(fixtureShards()), aggregate.MaxRunItems, cb, rb)
	if err != nil {
		t.Fatal(err)
	}
	days := rows.Days()
	if err := WriteDays(ctx, conn, rows, days); err != nil {
		t.Fatalf("write all days: %v", err)
	}
	first := counts(t, conn)
	for _, table := range transcriptTables {
		if first[table] == 0 {
			t.Errorf("%s: no rows written", table)
		}
	}
	// A second scan sees only today's transcripts (yesterday's were deleted) and rebuilds today alone.
	today := fixtureShards()[1:]
	later, err := aggregate.BuildRows(source(today), aggregate.MaxRunItems, cb, rb)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteDays(ctx, conn, later, []time.Time{days[1]}); err != nil {
		t.Fatalf("write today: %v", err)
	}
	var kept, todayCalls int64
	_ = conn.QueryRow(ctx, "SELECT coalesce(sum(calls), 0) FROM usage_daily WHERE day = $1", days[0]).Scan(&kept)
	_ = conn.QueryRow(ctx, "SELECT coalesce(sum(calls), 0) FROM usage_daily WHERE day = $1", days[1]).Scan(&todayCalls)
	if kept != 3 || todayCalls != 2 {
		t.Errorf("calls yesterday %d (want 3, kept) today %d (want 2, rebuilt)", kept, todayCalls)
	}
	if err := WriteDays(ctx, conn, later, []time.Time{days[1]}); err != nil {
		t.Fatalf("rewrite is not idempotent: %v", err)
	}
}

func TestSchemaAcceptsEveryBashFamily(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	names := []string{"other", "shell loop"}
	for f := range labels.Families {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		if _, err := tx.Exec(ctx, "SAVEPOINT s"); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, "INSERT INTO tool_result_daily VALUES (current_date, 'x', 'main', $1, 0, 0, 0, 0, 0, 0)",
			"Bash: "+f)
		if err != nil {
			t.Errorf("schema refuses tool label for Bash family %q", f)
		}
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT s"); err != nil {
			t.Fatal(err)
		}
	}
}

// A repeat counts once, for the transcript that owns the result; a fork's copy of the same result adds nothing.
func TestBuildRowsCountsRepeatsOncePerOwnedResult(t *testing.T) {
	d := noon(0)
	repeat := func(id string, ts int64, tool string) extract.Event {
		e := result(id, ts, tool, 40, "")
		e.Repeat = true
		return e
	}
	events := []extract.Event{
		result("1111111111111111", d, "Read: whole", 40, ""),
		repeat("2222222222222222", d+1, "Read: whole"),
		repeat("3333333333333333", d+2, "Read: whole"),
		result("4444444444444444", d+3, "Grep", 40, ""),
	}
	rows := build(t, []*scan.Shard{
		{Client: clients.ClaudeCode, Project: "benchwatch", Session: "abcd1234", Scope: "main", Events: events},
		{Client: clients.ClaudeCode, Project: "benchwatch", Session: "ffff0000", Scope: "main", Events: events[:3]},
	})
	got := map[string][2]int64{}
	for _, r := range rows.Results {
		got[r.Tool] = [2]int64{r.Results, r.RepeatResults}
	}
	if got["Read: whole"] != [2]int64{3, 2} || got["Grep"] != [2]int64{1, 0} || len(got) != 2 {
		t.Fatalf("results and repeats by tool: %v", got)
	}
}

// A transcript left unread is named in the log by a fixed code, wrapped or not.
func TestUnreadTranscriptHasItsOwnCode(t *testing.T) {
	if got := codeOf(fmt.Errorf("read: %w", scan.ErrSeenFull)); got != "ESEENFULL" {
		t.Fatalf("code %q, want ESEENFULL", got)
	}
	if got := codeOf(syscall.EACCES); got != "EACCES" {
		t.Fatalf("code %q, want EACCES", got)
	}
}
