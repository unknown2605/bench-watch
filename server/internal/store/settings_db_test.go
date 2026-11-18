package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"benchwatch/server/db"
)

const migration00006 = "migrations/00006_save_every_30s_keep_30_days.sql"

// sections returns the statements of a goose file's Up and Down parts; it holds no StatementBegin blocks.
func sections(t *testing.T, name string) (up, down []string) {
	t.Helper()
	data, err := db.Migrations.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	upText, downText, ok := strings.Cut(string(data), "-- +goose Down")
	if !ok || strings.Contains(string(data), "StatementBegin") {
		t.Fatalf("%s: unexpected layout", name)
	}
	split := func(s string) []string {
		var out []string
		for _, stmt := range strings.Split(s, ";\n") {
			var lines []string
			for _, l := range strings.Split(stmt, "\n") {
				if !strings.HasPrefix(strings.TrimSpace(l), "--") {
					lines = append(lines, l)
				}
			}
			if body := strings.TrimSpace(strings.Join(lines, "\n")); body != "" {
				out = append(out, body)
			}
		}
		return out
	}
	return split(upText), split(downText)
}

type settingsState struct{ collector, retention string }

func readSettings(t *testing.T, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) settingsState {
	t.Helper()
	ctx := context.Background()
	var s settingsState
	if err := q.QueryRow(ctx, `SELECT detail_interval_seconds || ',' || disk_interval_seconds || ',' ||
		top_apps_interval_seconds FROM collector_settings`).Scan(&s.collector); err != nil {
		t.Fatal(err)
	}
	if err := q.QueryRow(ctx, `SELECT string_agg(tier::text || '=' || coalesce(keep::text, 'forever'), ','
		ORDER BY tier) FROM retention_settings`).Scan(&s.retention); err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	retention00006 = "raw=7 days,rollup_1m=30 days,rollup_1h=30 days,events=30 days"
	retention00001 = "raw=14 days,rollup_1m=2 years,rollup_1h=forever,events=2 years"
)

// The fixture database is migrated to the latest version, so it holds 00006's values.
func TestMigration00006SavesEvery30sAndKeeps30Days(t *testing.T) {
	conn := testConn(t)
	got := readSettings(t, conn)
	if got.collector != "30,300,30" || got.retention != retention00006 {
		t.Fatalf("settings %+v; want collector 30,300,30 and %s", got, retention00006)
	}
}

// Down and Up run inside one transaction that is rolled back, so the fixture's settings never change.
func TestMigration00006DownRestoresThePreviousValues(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	before := readSettings(t, conn)
	t.Logf("settings before: %+v", before)
	t.Cleanup(func() {
		if after := readSettings(t, conn); after != before {
			t.Fatalf("settings changed: %+v, want %+v", after, before)
		}
		t.Logf("settings restored: %+v", before)
	})
	up, down := sections(t, migration00006)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	run := func(stmts ...string) {
		t.Helper()
		for _, s := range append(stmts, "SET CONSTRAINTS ALL IMMEDIATE") {
			if _, err := tx.Exec(ctx, s); err != nil {
				t.Fatalf("%.60s: %v", s, err)
			}
		}
	}
	run(down...)
	if got := readSettings(t, tx); got.collector != "10,300,10" || got.retention != retention00001 {
		t.Fatalf("after down %+v; want collector 10,300,10 and %s", got, retention00001)
	}
	run(up...)
	if got := readSettings(t, tx); got.collector != "30,300,30" || got.retention != retention00006 {
		t.Fatalf("after up again %+v; want collector 30,300,30 and %s", got, retention00006)
	}
	run(append(down, "UPDATE collector_settings SET detail_interval_seconds = 15")...)
	run(up...)
	if got := readSettings(t, tx); got.collector != "15,300,30" {
		t.Fatalf("an interval set by hand moved: %s, want 15,300,30", got.collector)
	}
}

// Transcript and Guard figures are kept forever: no retention tier may reach their tables.
func TestRetentionReachesOnlyMachineAndDockerTables(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	rows, err := conn.Query(ctx, "SELECT parent FROM partition_policy ORDER BY parent")
	if err != nil {
		t.Fatal(err)
	}
	parents, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 18 {
		t.Errorf("%d partition policies, want the 18 Machine and Docker tables", len(parents))
	}
	for _, p := range parents {
		if !strings.HasPrefix(p, "machine_") && !strings.HasPrefix(p, "docker_") {
			t.Errorf("retention reaches %s", p)
		}
	}
	for _, table := range []string{"usage_daily", "context_band_daily", "compaction_daily", "compaction_events",
		"session_daily", "subagent_daily", "subagent_run_daily", "tool_result_daily", "tool_result_size_daily",
		"tool_result_top", "tool_error_daily", "guard_daily", "guard_latency_buckets", "guard_latency_daily",
		"guard_mismatch_daily", "guard_window_checks", "guard_call_hourly", "guard_call_latency_daily", "guard_rule_daily",
		"guard_log_state"} {
		if got := count(t, conn, "SELECT count(*) FROM pg_class WHERE relname = $1 AND relkind = 'r'", table); got != 1 {
			t.Errorf("%s is not a plain table", table)
		}
		if got := count(t, conn, "SELECT count(*) FROM pg_inherits WHERE inhrelid = $1::regclass", table); got != 0 {
			t.Errorf("%s is a partition", table)
		}
	}
}

// At 30 s a minute holds two samples, and the hour weights each minute by them.
func TestRollupsOf30SecondSamples(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	emptyTables(t, conn, sampleTableNames...)
	t.Cleanup(func() { emptyTables(t, conn, sampleTableNames...) })
	q := New(conn)

	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	var b SampleBatch
	for i := range 120 {
		b.Machine = append(b.Machine, machine(hour.Add(time.Duration(i)*30*time.Second), float32(i%2*20+10)))
	}
	if _, err := WriteSamples(ctx, conn, b); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RollupMinutes(ctx, RollupMinutesParams{Since: ts(hour), Until: ts(hour.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RollupHours(ctx, RollupHoursParams{Since: ts(hour), Until: ts(hour.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, conn, "SELECT count(*) FROM machine_rollup_1m WHERE samples = 2 AND cpu_samples = 2"+
		" AND cpu_busy_avg = 20 AND cpu_busy_max = 30"); got != 60 {
		t.Fatalf("%d minutes with 2 samples averaging 20 and peaking at 30, want 60", got)
	}
	var avg float64
	var samples int64
	if err := conn.QueryRow(ctx, "SELECT cpu_busy_avg, samples FROM machine_rollup_1h WHERE bucket = $1",
		hour).Scan(&avg, &samples); err != nil {
		t.Fatal(err)
	}
	if avg != 20 || samples != 120 {
		t.Fatalf("hour cpu avg %v over %d samples, want 20 over 120", avg, samples)
	}
	series, err := q.MachineSeries1m(ctx, MachineSeries1mParams{Since: ts(hour), Until: ts(hour.Add(time.Hour))})
	if err != nil || len(series) != 60 {
		t.Fatalf("24h series reads %d minute points (%v), want 60", len(series), err)
	}
}

// useRetention sets tiers for one test (nil keeps forever) and puts every tier back afterwards, checking it did.
func useRetention(t *testing.T, conn *pgx.Conn, keeps map[string]*string) {
	t.Helper()
	ctx := context.Background()
	before := readSettings(t, conn).retention
	rows, err := conn.Query(ctx, "SELECT tier::text, keep::text FROM retention_settings")
	if err != nil {
		t.Fatal(err)
	}
	type tier struct {
		name string
		keep *string
	}
	saved, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (tier, error) {
		var x tier
		return x, r.Scan(&x.name, &x.keep)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retention before: %s", before)
	apply := func(set map[string]*string) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		for name, keep := range set {
			if _, err := tx.Exec(ctx, "UPDATE retention_settings SET keep = $2::interval WHERE tier = $1::retention_tier",
				name, keep); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	t.Cleanup(func() {
		set := map[string]*string{}
		for _, x := range saved {
			set[x.name] = x.keep
		}
		if err := apply(set); err != nil {
			t.Fatalf("restore retention: %v", err)
		}
		if after := readSettings(t, conn).retention; after != before {
			t.Fatalf("retention not restored: %s, want %s", after, before)
		}
		t.Logf("retention restored: %s", before)
	})
	if err := apply(keeps); err != nil {
		t.Fatalf("set retention: %v", err)
	}
}
