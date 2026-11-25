package server

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"benchwatch/server/internal/store"
)

// oldListStacks is the query before CM-00032, kept to compare answers and plans.
const oldListStacks = `SELECT stack FROM docker_container_rollup_1h WHERE bucket >= $1::timestamptz
UNION
SELECT stack FROM docker_container_rollup_1m
WHERE bucket >= greatest($1::timestamptz,
    coalesce((SELECT max(bucket) FROM docker_container_rollup_1h), '-infinity'::timestamptz))
UNION
SELECT stack FROM docker_container_samples
WHERE at >= greatest($1::timestamptz,
    coalesce((SELECT max(bucket) FROM docker_container_rollup_1m), '-infinity'::timestamptz))
ORDER BY 1`

func TestStackParamsStartEachTierAtTheLaterOfSinceAndTheCoarserMax(t *testing.T) {
	since := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	at := func(h int) pgtype.Timestamptz { return ts(since.Add(time.Duration(h) * time.Hour)) }
	for name, c := range map[string]struct {
		in   store.StackCutoffsRow
		want store.ListStacksParams
	}{
		"tiers empty": {store.StackCutoffsRow{},
			store.ListStacksParams{HourSince: at(0), MinuteSince: at(0), RawSince: at(0)}},
		"newer than since": {store.StackCutoffsRow{HourMax: at(5), MinuteMax: at(9)},
			store.ListStacksParams{HourSince: at(0), MinuteSince: at(5), RawSince: at(9)}},
		"older than since": {store.StackCutoffsRow{HourMax: at(-50), MinuteMax: at(-40)},
			store.ListStacksParams{HourSince: at(0), MinuteSince: at(0), RawSince: at(0)}},
	} {
		if got := stackParams(since, c.in); got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}

func ensurePartitionAt(t *testing.T, pool *pgxpool.Pool, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "SELECT ensure_partitions($1, 0)", at); err != nil {
		t.Fatalf("partitions: %v", err)
	}
}

func seedStack(t *testing.T, pool *pgxpool.Pool, tier, stack string, at time.Time) {
	t.Helper()
	ensurePartitionAt(t, pool, at)
	sql := map[string]string{
		"raw": `INSERT INTO docker_container_samples (at, container, stack, state, health, restarts)
			VALUES ($1, 'c-' || $2::text, $2, 'running', 'none', 0)`,
		"minute": `INSERT INTO docker_container_rollup_1m (bucket, container, stack, samples, rate_samples, mem_samples,
			restarts_max, merged_names_max) VALUES ($1, 'c-' || $2::text, $2, 1, 0, 0, 0, 0)`,
		"hour": `INSERT INTO docker_container_rollup_1h (bucket, container, stack, samples, rate_samples, mem_samples,
			restarts_max, merged_names_max) VALUES ($1, 'c-' || $2::text, $2, 1, 0, 0, 0, 0)`,
	}[tier]
	if _, err := pool.Exec(context.Background(), sql, at, stack); err != nil {
		t.Fatalf("seed %s %s: %v", tier, stack, err)
	}
}

func oldStacks(t *testing.T, pool *pgxpool.Pool, since time.Time) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), oldListStacks, since)
	if err != nil {
		t.Fatalf("old ListStacks: %v", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("old ListStacks: %v", err)
	}
	return out
}

func cleanFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
}

type seed struct {
	tier, stack string
	ago         time.Duration
}

func TestListStacksAnswersLikeTheOldQueryForEveryTierShape(t *testing.T) {
	pool := testPool(t)
	cleanFixtures(t, pool)
	h, d := time.Hour, 24*time.Hour
	for _, c := range []struct {
		name string
		rows []seed
		want []string
	}{
		{"only raw", []seed{{"raw", "a", h}}, []string{"a"}},
		{"only minute", []seed{{"minute", "b", h}}, []string{"b"}},
		{"only hour", []seed{{"hour", "c", h}}, []string{"c"}},
		{"all tiers empty", nil, []string{}},
		{"one stack per tier", []seed{{"raw", "a", h}, {"minute", "b", 2 * h}, {"hour", "c", 3 * h}},
			[]string{"a", "b", "c"}},
		{"finer rows older than the coarser tier are skipped",
			[]seed{{"hour", "h", h}, {"minute", "m", 5 * h}, {"raw", "r", 6 * h}}, []string{"h"}},
		{"rows exactly at a cutoff count", []seed{{"hour", "hh", 3 * h}, {"minute", "edge", 3 * h},
			{"minute", "mm", 2 * h}, {"raw", "edge2", 2 * h}}, []string{"edge", "edge2", "hh", "mm"}},
		{"older than seven days", []seed{{"hour", "old1", 8 * d}, {"minute", "old2", 9 * d}, {"raw", "old3", 8 * d},
			{"hour", "keep", d}}, []string{"keep"}},
		{"hour tier only ancient: minute and raw still start at seven days",
			[]seed{{"hour", "old1", 8 * d}, {"minute", "old2", 7*d + 12*h}, {"minute", "m3", 3 * d},
				{"raw", "old3", 8 * d}, {"raw", "r1", h}}, []string{"m3", "r1"}},
	} {
		emptyFixtures(t, pool)
		now := time.Now().UTC().Truncate(time.Minute)
		for _, r := range c.rows {
			seedStack(t, pool, r.tier, r.stack, now.Add(-r.ago))
		}
		since := now.Add(-7 * d)
		got, err := listStacks(context.Background(), store.New(pool), since)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slices.Equal(nonNil(got), c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if old := oldStacks(t, pool, since); !slices.Equal(nonNil(old), c.want) {
			t.Errorf("%s: the old query gives %v, the test's list %v is wrong", c.name, old, c.want)
		}
	}
}

// Cutoffs read before the list only ever read more, so a rollup in between loses no stack; cutoffs read after it
// would skip x, whose hour row does not exist yet.
func TestARollupBetweenTheCutoffsAndTheListLosesNoStack(t *testing.T) {
	pool := testPool(t)
	cleanFixtures(t, pool)
	now := time.Now().UTC()
	hour := now.Truncate(time.Hour)
	seedStack(t, pool, "hour", "base", hour.Add(-3*time.Hour))
	seedStack(t, pool, "minute", "x", hour.Add(-150*time.Minute))
	got, err := listStacksWith(context.Background(), store.New(pool), now.Add(-7*24*time.Hour), func() {
		seedStack(t, pool, "hour", "other", hour.Add(-time.Hour))
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"base", "other", "x"}; !slices.Equal(got, want) {
		t.Errorf("stacks %v, want %v", got, want)
	}
}

// explainPrepared explains the statement pgx prepared for the query containing every fragment, with literal arguments.
func explainPrepared(t *testing.T, conn *pgx.Conn, args string, fragments ...string) (string, int64, int64) {
	t.Helper()
	ctx := context.Background()
	where := "statement NOT LIKE '%pg_prepared_statements%'"
	for _, f := range fragments {
		where += " AND statement LIKE '%" + f + "%'"
	}
	var name string
	var generic, custom int64
	err := conn.QueryRow(ctx, "SELECT name, generic_plans, custom_plans FROM pg_prepared_statements WHERE "+where).
		Scan(&name, &generic, &custom)
	if err != nil {
		t.Fatalf("prepared statement %v: %v", fragments, err)
	}
	rows, err := conn.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) EXECUTE "+pgx.Identifier{name}.Sanitize()+"("+args+")")
	if err != nil {
		t.Fatalf("explain %v: %v", fragments, err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("explain %v: %v", fragments, err)
	}
	return strings.Join(lines, "\n"), generic, custom
}

func literal(t time.Time) string { return "'" + t.UTC().Format(time.RFC3339Nano) + "'::timestamptz" }

func seedExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed: %v\n%s", err, sql)
	}
}

// The fixed late-October time keeps all 30 days in one monthly partition; early in a month the current one is nearly
// empty and plans as a Seq Scan whichever query runs. The plans are logged for the ticket.
func TestStackListUsesTheMinuteIndexAlsoAfterTenRuns(t *testing.T) {
	pool := testPool(t)
	cleanFixtures(t, pool)
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 23, 50, 0, 0, time.UTC)
	start := now.Add(-30 * 24 * time.Hour)
	for d := start; !d.After(now.Add(24 * time.Hour)); d = d.Add(24 * time.Hour) {
		ensurePartitionAt(t, pool, d)
	}
	ensurePartitionAt(t, pool, now)
	seedExec(t, pool, `INSERT INTO docker_container_rollup_1m (bucket, container, stack, samples, rate_samples,
		mem_samples, restarts_max, merged_names_max)
		SELECT b, 'c' || c, 'stk' || (c % 3), 1, 0, 0, 0, 0
		FROM generate_series($1::timestamptz, $2::timestamptz, interval '1 minute') b, generate_series(1, 2) c`, start, now)
	seedExec(t, pool, `INSERT INTO docker_container_rollup_1h (bucket, container, stack, samples, rate_samples,
		mem_samples, restarts_max, merged_names_max)
		SELECT b, 'c' || c, 'stk' || (c % 3), 1, 0, 0, 0, 0
		FROM generate_series(date_trunc('hour', $1::timestamptz), date_trunc('hour', $2::timestamptz), interval '1 hour') b,
			generate_series(1, 2) c`, start, now)
	seedExec(t, pool, `INSERT INTO docker_container_samples (at, container, stack, state, health, restarts)
		SELECT b, 'c' || c, 'stk' || (c % 3), 'running', 'none', 0
		FROM generate_series($1::timestamptz - interval '1 day', $1::timestamptz, interval '1 minute') b,
			generate_series(1, 2) c`, now)
	for _, table := range []string{"docker_container_rollup_1m", "docker_container_rollup_1h",
		"docker_container_samples"} {
		seedExec(t, pool, "ANALYZE "+table)
	}
	partition := "docker_container_rollup_1m_p" + now.Format("200601")
	var inPartition, total int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+partition).Scan(&inPartition); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM docker_container_rollup_1m").Scan(&total); err != nil {
		t.Fatal(err)
	}
	t.Logf("minute rows seeded: %d, %d of them in %s", total, inPartition, partition)
	if inPartition != total || inPartition < 80000 {
		t.Fatalf("%d of %d minute rows are in %s, want all of the 30 days", inPartition, total, partition)
	}
	since := now.Add(-7 * 24 * time.Hour)
	onPartition := regexp.QuoteMeta(partition) + `\s`
	seqScan := regexp.MustCompile(`Seq Scan on ` + onPartition)
	indexName := regexp.QuoteMeta(partition) + `_\w+`
	indexScan := regexp.MustCompile(`Index Scan using ` + indexName + ` on |Bitmap Index Scan on ` + indexName)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	q := store.New(conn.Conn())
	var c store.StackCutoffsRow
	for run := 1; run <= 12; run++ {
		if run == 1 || run == 12 {
			var err error
			if c, err = q.StackCutoffs(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := listStacks(ctx, q, since); err != nil {
			t.Fatal(err)
		}
		if run != 1 && run != 12 {
			continue
		}
		p := stackParams(since, c)
		args := literal(p.HourSince.Time) + "," + literal(p.MinuteSince.Time) + "," + literal(p.RawSince.Time)
		plan, generic, custom := explainPrepared(t, conn.Conn(), args, "docker_container_rollup_1h",
			"docker_container_samples", "$3::timestamptz")
		t.Logf("new ListStacks after %d runs (generic plans %d, custom plans %d):\n%s", run, generic, custom, plan)
		if seqScan.MatchString(plan) {
			t.Errorf("after %d runs the month's minute partition is read by a Seq Scan", run)
		}
		if !indexScan.MatchString(plan) {
			t.Errorf("after %d runs no index scan on the month's minute partition", run)
		}
	}
	for run := 1; run <= 12; run++ {
		rows, err := conn.Query(ctx, oldListStacks, since)
		if err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	plan, generic, custom := explainPrepared(t, conn.Conn(), literal(since), "greatest($1")
	t.Logf("old ListStacks after 12 runs (generic plans %d, custom plans %d):\n%s", generic, custom, plan)
	if !seqScan.MatchString(plan) {
		t.Errorf("the old query does not Seq Scan %s, so this test cannot tell it from the new one", partition)
	}
}
