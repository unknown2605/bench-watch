package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"benchwatch/server/internal/testdb"
)

// These tests need a migrated fixture database; bin/test-db.sh sets CM_TEST_DSN to the one it creates and drops.
func testConn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := testdb.DSN(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func emptyTables(t *testing.T, conn *pgx.Conn, tables ...string) {
	t.Helper()
	for _, table := range tables {
		if _, err := conn.Exec(context.Background(), "DELETE FROM "+pgx.Identifier{table}.Sanitize()); err != nil {
			t.Fatalf("empty %s: %v", table, err)
		}
	}
}

func count(t *testing.T, conn *pgx.Conn, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func ptr[T any](v T) *T { return &v }

var sampleTableNames = []string{"machine_samples", "machine_core_samples", "machine_interface_samples",
	"machine_volume_samples", "machine_app_samples", "docker_vm_samples", "docker_container_samples", "docker_events",
	"machine_rollup_1m", "machine_rollup_1h", "machine_core_rollup_1m", "machine_core_rollup_1h",
	"machine_interface_rollup_1m", "machine_interface_rollup_1h", "docker_vm_rollup_1m", "docker_vm_rollup_1h",
	"docker_container_rollup_1m", "docker_container_rollup_1h"}

func machine(at time.Time, cpu float32) MachineSample {
	return MachineSample{At: ts(at), CpuBusyPct: ptr(cpu), Load1: ptr[float32](1), Load5: ptr[float32](1),
		Load15: ptr[float32](1), MemTotalBytes: ptr[int64](100), MemUsedBytes: ptr[int64](50), MemAppBytes: ptr[int64](30),
		MemCachedBytes: ptr[int64](10), MemCompressedBytes: ptr[int64](5), SwapUsedBytes: ptr[int64](0),
		SwapTotalBytes: ptr[int64](0), MemPressure: ptr(MemoryPressureNormal),
		DiskReadBps: ptr[int64](1), DiskWriteBps: ptr[int64](2)}
}

func everyReadFailed(at time.Time) MachineSample { return MachineSample{At: ts(at)} }

func batchAt(at time.Time) SampleBatch {
	return SampleBatch{
		Machine: []MachineSample{machine(at, 10), machine(at, 99)},
		Cores: []MachineCoreSample{{At: ts(at), Core: 0, Kind: CoreKindPerformance, BusyPct: 10},
			{At: ts(at), Core: 1, Kind: CoreKindEfficiency, BusyPct: 5}},
		Interfaces: []MachineInterfaceSample{{At: ts(at), Interface: "Wi-Fi", RxBps: 1, TxBps: 2},
			{At: ts(at), Interface: "other", RxBps: 3, TxBps: 4, MergedNames: 2}},
		Volumes: []MachineVolumeSample{{At: ts(at), Volume: "Macintosh HD",
			Kind: ptr(VolumeKindInternal), Filesystem: ptr("apfs"), TotalBytes: 10,
			FreeBytes: 5}},
		Apps: []MachineAppSample{{At: ts(at), App: "Finder", CpuPct: 1, MemBytes: 2}},
		DockerVM: []DockerVmSample{{At: ts(at), MemTotalBytes: ptr[int64](10), MemUsedBytes: ptr[int64](5),
			CpuPct: ptr[float32](3), Cpus: ptr[int16](4)}},
		Containers: []DockerContainerSample{{At: ts(at), Container: "web_1", Stack: "web",
			State: ContainerStateRunning, Health: ContainerHealthHealthy, CpuPct: ptr[float32](1), MemBytes: ptr[int64](2),
			RxBps: ptr[int64](0), TxBps: ptr[int64](0)}},
		Events: []DockerEvent{{At: ts(at), Container: "web_1", Kind: DockerEventKindStart},
			{At: ts(at), Container: "web_1", Kind: DockerEventKindHealthHealthy}},
	}
}

func TestWriteSamplesIsRetrySafe(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	emptyTables(t, conn, sampleTableNames...)
	t.Cleanup(func() { emptyTables(t, conn, sampleTableNames...) })

	at := time.Now().UTC().Truncate(time.Second)
	b := batchAt(at)
	n, err := WriteSamples(ctx, conn, b)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if n != 11 {
		t.Fatalf("first write inserted %d rows, want 11 (two machine rows share a key)", n)
	}
	if got := count(t, conn, "SELECT count(*) FROM machine_samples WHERE cpu_busy_pct = 10"); got != 1 {
		t.Fatalf("the first row of a key must win, got %d", got)
	}

	n, err = WriteSamples(ctx, conn, b)
	if err != nil || n != 0 {
		t.Fatalf("retried batch: %d rows, %v; want 0 and no error", n, err)
	}

	later := batchAt(at.Add(10 * time.Second))
	later.Machine = append(later.Machine, b.Machine...)
	n, err = WriteSamples(ctx, conn, later)
	if err != nil || n != 11 {
		t.Fatalf("partly stored batch: %d rows, %v; want 11", n, err)
	}

	merged := batchAt(at.Add(15 * time.Second))
	merged.Interfaces[0].MergedNames = 1
	if _, err := WriteSamples(ctx, conn, merged); err == nil || !IsPermanent(err) {
		t.Fatalf("a named row with merged names: %v, want a permanent error", err)
	}

	bad := batchAt(at.Add(20 * time.Second))
	bad.Containers[0].Container = "/Users/x"
	if _, err := WriteSamples(ctx, conn, bad); err == nil || !IsPermanent(err) {
		t.Fatalf("label violation: %v, want a permanent error", err)
	}
	if got := count(t, conn, "SELECT count(*) FROM machine_samples WHERE at = $1", at.Add(20*time.Second)); got != 0 {
		t.Fatalf("a failed batch left %d machine rows", got)
	}

	old := at.AddDate(0, 0, -3)
	if _, err := WriteSamples(ctx, conn, SampleBatch{Machine: []MachineSample{machine(old, 1)}}); err != nil {
		t.Fatalf("a sample on a day with no partition yet: %v", err)
	}
}

func TestIsPermanent(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"check violation":    {&pgconn.PgError{Code: "23514"}, true},
		"invalid text":       {&pgconn.PgError{Code: "22P02"}, true},
		"serialization":      {&pgconn.PgError{Code: "40001"}, false},
		"connection failure": {&pgconn.PgError{Code: "08006"}, false},
		"admin shutdown":     {&pgconn.PgError{Code: "57P01"}, false},
		"timeout":            {context.DeadlineExceeded, false},
		"wrapped check":      {errors.Join(errors.New("write"), &pgconn.PgError{Code: "23505"}), true},
		"nil":                {nil, false},
	}
	for name, c := range cases {
		if got := IsPermanent(c.err); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// One raw table per rollup pair; each holds one row per sample time here, so every bucket has one key.
var rollupPairs = []struct{ raw, minute, hour string }{
	{"machine_samples", "machine_rollup_1m", "machine_rollup_1h"},
	{"machine_core_samples", "machine_core_rollup_1m", "machine_core_rollup_1h"},
	{"machine_interface_samples", "machine_interface_rollup_1m", "machine_interface_rollup_1h"},
	{"docker_vm_samples", "docker_vm_rollup_1m", "docker_vm_rollup_1h"},
	{"docker_container_samples", "docker_container_rollup_1m", "docker_container_rollup_1h"},
}

func oneOfEach(at time.Time, cpu float32) SampleBatch {
	b := batchAt(at)
	b.Machine = []MachineSample{machine(at, cpu)}
	b.Cores, b.Interfaces, b.Volumes, b.Apps, b.Events = b.Cores[:1], b.Interfaces[:1], nil, nil, nil
	return b
}

func writeEvery10s(t *testing.T, conn *pgx.Conn, from time.Time, offset time.Duration, n int, cpu float32) {
	t.Helper()
	for i := 0; i < n; i++ {
		at := from.Add(offset + time.Duration(i)*10*time.Second)
		if _, err := WriteSamples(context.Background(), conn, oneOfEach(at, cpu)); err != nil {
			t.Fatal(err)
		}
	}
}

func expectSamples(t *testing.T, conn *pgx.Conn, table string, bucket time.Time, want int64, why string) {
	t.Helper()
	got := count(t, conn, "SELECT samples FROM "+pgx.Identifier{table}.Sanitize()+" WHERE bucket = $1", bucket)
	if got != want {
		t.Errorf("%s: %s bucket holds %d samples, want %d", why, table, got, want)
	}
}

func TestRollupsNeverShrinkABucket(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	emptyTables(t, conn, sampleTableNames...)
	t.Cleanup(func() { emptyTables(t, conn, sampleTableNames...) })
	q := New(conn)
	minutes := func(since, until time.Time) {
		t.Helper()
		if _, err := q.RollupMinutes(ctx, RollupMinutesParams{Since: ts(since), Until: ts(until)}); err != nil {
			t.Fatal(err)
		}
	}
	hours := func(since, until time.Time) {
		t.Helper()
		if _, err := q.RollupHours(ctx, RollupHoursParams{Since: ts(since), Until: ts(until)}); err != nil {
			t.Fatal(err)
		}
	}

	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	a, b := hour, hour.Add(time.Minute)
	writeEvery10s(t, conn, a, 0, 6, 10)
	writeEvery10s(t, conn, b, 0, 3, 10)
	minutes(a, b.Add(time.Minute))
	minutes(a.Add(30*time.Second), a.Add(40*time.Second))
	hours(a, a.Add(time.Second))
	for _, p := range rollupPairs {
		expectSamples(t, conn, p.minute, a, 6, "first rollup and an unaligned rerun")
		expectSamples(t, conn, p.minute, b, 3, "first rollup")
		expectSamples(t, conn, p.hour, hour, 9, "first rollup")
	}

	for _, p := range rollupPairs {
		if _, err := conn.Exec(ctx, "DELETE FROM "+pgx.Identifier{p.raw}.Sanitize()+" WHERE at >= $1 AND at < $2",
			a.Add(30*time.Second), b); err != nil {
			t.Fatal(err)
		}
	}
	minutes(a, a.Add(time.Minute))
	for _, p := range rollupPairs {
		expectSamples(t, conn, p.minute, a, 6, "a rerun over thinned raw rows")
	}

	for _, p := range rollupPairs {
		if _, err := conn.Exec(ctx, "DELETE FROM "+pgx.Identifier{p.minute}.Sanitize()+" WHERE bucket = $1", b); err != nil {
			t.Fatal(err)
		}
	}
	hours(hour, hour.Add(time.Hour))
	for _, p := range rollupPairs {
		expectSamples(t, conn, p.hour, hour, 9, "a rerun over a lost minute")
	}

	writeEvery10s(t, conn, a, 5*time.Second, 6, 40)
	minutes(a, b.Add(time.Minute))
	hours(hour, hour.Add(time.Hour))
	for _, p := range rollupPairs {
		expectSamples(t, conn, p.minute, a, 9, "a rerun over more raw rows")
		expectSamples(t, conn, p.minute, b, 3, "a rerun that restores a lost minute")
		expectSamples(t, conn, p.hour, hour, 12, "a rerun over more minutes")
	}
	var avg float64
	if err := conn.QueryRow(ctx, "SELECT cpu_busy_avg FROM machine_rollup_1m WHERE bucket = $1",
		a).Scan(&avg); err != nil {
		t.Fatal(err)
	}
	if avg != 30 {
		t.Errorf("a replaced bucket must carry the new averages too: cpu avg %v, want 30", avg)
	}
}

func TestRetentionKeepsCoarserTiersLonger(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	emptyTables(t, conn, sampleTableNames...)
	t.Cleanup(func() { emptyTables(t, conn, sampleTableNames...) })
	q := New(conn)

	type setting struct {
		tier string
		keep pgtype.Interval
	}
	read := func() []setting {
		rows, err := conn.Query(ctx, "SELECT tier::text, keep FROM retention_settings ORDER BY tier")
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (setting, error) {
			var s setting
			return s, r.Scan(&s.tier, &s.keep)
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := read()
	t.Logf("retention before: %v", before)
	t.Cleanup(func() {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		for _, s := range before {
			if _, err := tx.Exec(ctx, "UPDATE retention_settings SET keep = $2 WHERE tier = $1::retention_tier", s.tier,
				s.keep); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		after := read()
		if fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("retention not restored: %v, want %v", after, before)
		}
		t.Logf("retention restored: %v", after)
	})

	for name, stmt := range map[string]string{
		"hourly shorter than minute": "UPDATE retention_settings SET keep = interval '1 day' WHERE tier = 'rollup_1h'",
		"minute shorter than raw":    "UPDATE retention_settings SET keep = interval '1 day' WHERE tier = 'rollup_1m'",
		"raw longer than minute":     "UPDATE retention_settings SET keep = interval '3 years' WHERE tier = 'raw'",
	} {
		_, err := conn.Exec(ctx, stmt)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" {
			t.Errorf("%s: %v, want check_violation", name, err)
		}
	}
	if got := fmt.Sprint(read()); got != fmt.Sprint(before) {
		t.Fatalf("a refused change was stored: %s", got)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"UPDATE retention_settings SET keep = interval '1 year' WHERE tier = 'rollup_1h'",
		"UPDATE retention_settings SET keep = interval '1 year' WHERE tier = 'rollup_1m'",
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("moving both tiers in one transaction: %v", err)
	}

	old := time.Now().UTC().AddDate(0, -18, 0).Truncate(time.Minute)
	if _, err := WriteSamples(ctx, conn, SampleBatch{Machine: []MachineSample{machine(old, 7)}}); err != nil {
		t.Fatal(err)
	}
	dropped, err := q.DropExpiredPartitions(ctx, ts(time.Now()))
	if err != nil {
		t.Fatalf("an expiring minute month with an equally kept hour month: %v", err)
	}
	month := old.Format("200601")
	for _, want := range []string{"machine_rollup_1m_p" + month, "machine_rollup_1h_p" + month} {
		if !slices.Contains(dropped, want) {
			t.Errorf("%s not dropped (dropped %d partitions)", want, len(dropped))
		}
	}
}

func TestDropRollsUpFirstAndKeepsForeverTiers(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	emptyTables(t, conn, sampleTableNames...)
	t.Cleanup(func() { emptyTables(t, conn, sampleTableNames...) })
	useRetention(t, conn, map[string]*string{"raw": ptr("14 days"), "rollup_1m": ptr("2 years"), "rollup_1h": nil})
	q := New(conn)

	old := time.Now().UTC().AddDate(0, 0, -20).Truncate(time.Minute)
	if _, err := WriteSamples(ctx, conn, SampleBatch{Machine: []MachineSample{machine(old, 7)}}); err != nil {
		t.Fatal(err)
	}
	ancient := time.Now().UTC().AddDate(-3, 0, 0)
	if _, err := q.EnsurePartitions(ctx, EnsurePartitionsParams{At: ts(ancient), Ahead: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.DropExpiredPartitions(ctx, ts(time.Now().AddDate(1, 0, 0))); err != nil {
		t.Fatal(err)
	}
	if got := count(t, conn, "SELECT count(*) FROM machine_samples"); got != 0 {
		t.Fatalf("expired raw rows left: %d", got)
	}
	if got := count(t, conn, "SELECT samples FROM machine_rollup_1m WHERE bucket = $1", old); got != 1 {
		t.Fatalf("raw day dropped before its minute rollup: %d", got)
	}
	if got := count(t, conn,
		"SELECT count(*) FROM machine_rollup_1h WHERE bucket = date_trunc('hour', $1::timestamptz, 'UTC')",
		old); got != 1 {
		t.Fatalf("raw day dropped before its hour rollup: %d", got)
	}
	suffix := ancient.Format("200601")
	if got := count(t, conn, "SELECT count(*) FROM list_partitions() WHERE child = $1",
		"machine_rollup_1h_p"+suffix); got != 1 {
		t.Fatalf("an hourly partition kept forever was dropped")
	}
	if got := count(t, conn, "SELECT count(*) FROM list_partitions() WHERE child = $1",
		"machine_rollup_1m_p"+suffix); got != 0 {
		t.Fatalf("a 3-year-old minute partition outlived its 2-year retention")
	}
	today := "machine_samples_p" + time.Now().UTC().Format("20060102")
	if got := count(t, conn, "SELECT count(*) FROM list_partitions() WHERE child = $1", today); got != 1 {
		t.Fatalf("a future time dropped today's partition")
	}
}

func TestRollupsSkipMissingParts(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	emptyTables(t, conn, sampleTableNames...)
	t.Cleanup(func() { emptyTables(t, conn, sampleTableNames...) })
	q := New(conn)

	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	b := SampleBatch{Machine: []MachineSample{machine(hour, 10), machine(hour.Add(10*time.Second), 20),
		everyReadFailed(hour.Add(20 * time.Second)), machine(hour.Add(time.Minute), 60)}}
	if _, err := WriteSamples(ctx, conn, b); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RollupMinutes(ctx, RollupMinutesParams{Since: ts(hour),
		Until: ts(hour.Add(2 * time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RollupHours(ctx, RollupHoursParams{Since: ts(hour), Until: ts(hour.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	var samples, cpuSamples, loadSamples int64
	var avg float64
	if err := conn.QueryRow(ctx,
		"SELECT samples, cpu_samples, load_samples, cpu_busy_avg FROM machine_rollup_1m WHERE bucket = $1",
		hour).Scan(&samples, &cpuSamples, &loadSamples, &avg); err != nil {
		t.Fatal(err)
	}
	if samples != 3 || cpuSamples != 2 || loadSamples != 2 || avg != 15 {
		t.Fatalf("minute: %d samples, %d with cpu, %d with load, cpu avg %v; want 3, 2, 2, 15", samples, cpuSamples,
			loadSamples, avg)
	}
	if err := conn.QueryRow(ctx, "SELECT cpu_busy_avg FROM machine_rollup_1h WHERE bucket = $1",
		hour).Scan(&avg); err != nil {
		t.Fatal(err)
	}
	if avg != 30 {
		t.Fatalf("hour cpu avg %v, want 30: minutes must be weighted by the samples that measured CPU", avg)
	}
	if got := count(t, conn,
		"SELECT count(*) FROM machine_samples WHERE load_1 IS NULL AND mem_used_bytes IS NULL"); got != 1 {
		t.Fatalf("a missing part must be stored as NULL, got %d such rows", got)
	}
}

// Cleanup subtracts each keep from a timestamp, so a month is 28 to 31 days and a year 365 or 366; the order check
// must hold that way. Each case fires the deferred check, then rolls back, so nothing is stored.
func TestRetentionOrderUsesCalendarLengths(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	snapshot := func() string {
		var s string
		if err := conn.QueryRow(ctx, `SELECT string_agg(tier::text || '=' || coalesce(keep::text, 'forever'), ','
			ORDER BY tier) FROM retention_settings`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	t.Logf("retention before: %s", before)
	t.Cleanup(func() {
		if after := snapshot(); after != before {
			t.Fatalf("retention changed: %s, want %s", after, before)
		}
		t.Logf("retention restored: %s", before)
	})
	type keeps = map[string]string
	cases := []struct {
		name string
		keep keeps
		ok   bool
	}{
		{"hourly 725 days under minute 2 years", keeps{"rollup_1m": "2 years", "rollup_1h": "725 days"}, false},
		{"hourly 730 days under minute 2 years", keeps{"rollup_1m": "2 years", "rollup_1h": "730 days"}, false},
		{"hourly 731 days over minute 2 years", keeps{"rollup_1m": "2 years", "rollup_1h": "731 days"}, true},
		{"minute 1 month, hourly 30 days", keeps{"rollup_1m": "1 month", "rollup_1h": "30 days"}, false},
		{"minute 1 month, hourly 31 days", keeps{"rollup_1m": "1 month", "rollup_1h": "31 days"}, true},
		{"minute 1 year, hourly 365 days", keeps{"rollup_1m": "1 year", "rollup_1h": "365 days"}, false},
		{"minute 1 year, hourly 366 days", keeps{"rollup_1m": "1 year", "rollup_1h": "366 days"}, true},
		{"minute 12 months, hourly 1 year", keeps{"rollup_1m": "12 months", "rollup_1h": "1 year"}, true},
		{"raw 1 month, minute 30 days", keeps{"raw": "1 month", "rollup_1m": "30 days"}, false},
		{"raw 1 day 1 hour, minute 25 hours", keeps{"raw": "1 day 1 hour", "rollup_1m": "25 hours"}, true},
		{"defaults of 00001", keeps{"raw": "14 days", "rollup_1m": "2 years", "rollup_1h": "forever"}, true},
		{"defaults of 00006", keeps{"raw": "7 days", "rollup_1m": "30 days", "rollup_1h": "30 days"}, true},
	}
	for _, c := range cases {
		err := func() error {
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			for tier, keep := range c.keep {
				var v *string
				if keep != "forever" {
					v = &keep
				}
				const set = "UPDATE retention_settings SET keep = $2::interval WHERE tier = $1::retention_tier"
				if _, err := tx.Exec(ctx, set, tier, v); err != nil {
					return err
				}
			}
			start := time.Now()
			_, err = tx.Exec(ctx, "SET CONSTRAINTS retention_order IMMEDIATE")
			t.Logf("%s: check took %v", c.name, time.Since(start).Round(time.Millisecond))
			return err
		}()
		var pg *pgconn.PgError
		switch {
		case c.ok && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case !c.ok && (!errors.As(err, &pg) || pg.Code != "23514"):
			t.Errorf("%s: %v, want check_violation", c.name, err)
		}
	}
}
