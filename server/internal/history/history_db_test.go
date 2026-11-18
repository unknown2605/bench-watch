package history

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/testdb"
)

// These tests need a migrated fixture database; bin/test-db.sh sets CM_TEST_DSN to the one it creates and drops.
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

var historyTables = []string{"machine_samples", "machine_core_samples", "machine_interface_samples",
	"machine_volume_samples", "machine_app_samples", "docker_vm_samples", "docker_container_samples", "docker_events",
	"machine_rollup_1m", "machine_rollup_1h", "machine_core_rollup_1m", "machine_core_rollup_1h",
	"machine_interface_rollup_1m", "machine_interface_rollup_1h", "docker_vm_rollup_1m", "docker_vm_rollup_1h",
	"docker_container_rollup_1m", "docker_container_rollup_1h"}

func empty(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	for _, table := range historyTables {
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

func TestSinkStoresCollectorBatchOnce(t *testing.T) {
	conn := testConn(t)
	empty(t, conn)
	t.Cleanup(func() { empty(t, conn) })
	at := time.Now().UTC().Truncate(time.Second)
	b := sampleBatch(at)
	s := Sink{DB: conn}
	for range 2 {
		if err := s.Write(context.Background(), b); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	want := map[string]int64{"machine_samples": 1, "machine_core_samples": 2, "machine_interface_samples": 1,
		"machine_volume_samples": 2, "machine_app_samples": 3, "docker_vm_samples": 1, "docker_container_samples": 4,
		"docker_events": 1}
	for table, n := range want {
		if got := count(t, conn, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()); got != n {
			t.Errorf("%s: %d rows after two writes, want %d", table, got, n)
		}
	}
	b.Machine[0].Missing = []string{collect.PartSwap}
	b.Machine[0].At, b.Docker[0].At = at.Add(10*time.Second), at.Add(10*time.Second)
	if err := s.Write(context.Background(), b); err != nil {
		t.Fatalf("write without swap: %v", err)
	}
	if got := count(t, conn, "SELECT count(*) FROM machine_samples WHERE mem_total_bytes IS NULL"); got != 1 {
		t.Errorf("rows with the memory group NULL: %d, want 1", got)
	}
}

// Scenario "Old data is dropped": partitions past retention go, current ones and the longer tiers stay.
func TestUpkeepDropsExpiredPartitionsOnly(t *testing.T) {
	conn := testConn(t)
	empty(t, conn)
	t.Cleanup(func() { empty(t, conn) })
	now := time.Now().UTC()
	old := now.Add(-20 * 24 * time.Hour).Truncate(time.Minute)
	recent := now.Add(-3 * time.Minute).Truncate(time.Minute)
	for _, at := range []time.Time{old, old.Add(10 * time.Second), recent, recent.Add(10 * time.Second)} {
		if err := (Sink{DB: conn}).Write(context.Background(), sampleBatch(at)); err != nil {
			t.Fatalf("write %v: %v", at, err)
		}
	}
	oldDay := "machine_samples_p" + old.Format("20060102")
	nowDay := "machine_samples_p" + recent.Format("20060102")
	exists := func(rel string) bool {
		return count(t, conn, "SELECT count(*) FROM pg_class WHERE relname = $1", rel) == 1
	}
	if !exists(oldDay) || !exists(nowDay) {
		t.Fatalf("fixture partitions missing")
	}
	u := &Upkeep{DB: conn, Now: func() time.Time { return now }}
	if err := u.Tick(context.Background()); err != nil {
		t.Fatalf("upkeep: %v", err)
	}
	if exists(oldDay) {
		t.Errorf("%s past the 7-day detail retention was not dropped", oldDay)
	}
	if !exists("docker_events_p" + old.Format("200601")) {
		t.Errorf("docker events kept for 30 days lost their month partition")
	}
	if !exists(nowDay) {
		t.Errorf("current partition %s dropped", nowDay)
	}
	if got := count(t, conn, "SELECT count(*) FROM machine_samples WHERE at >= $1", recent); got != 2 {
		t.Errorf("current detail rows %d, want 2", got)
	}
	if got := count(t, conn, "SELECT coalesce(sum(samples), 0) FROM machine_rollup_1m WHERE bucket = $1", old); got != 2 {
		t.Errorf("minute bucket of dropped day holds %d samples, want 2 (rolled up before the drop)", got)
	}
	if got := count(t, conn, "SELECT count(*) FROM machine_rollup_1m WHERE bucket = $1", recent); got != 1 {
		t.Errorf("current minute buckets %d, want 1", got)
	}
	hour := recent.Truncate(time.Hour)
	if got := count(t, conn, "SELECT count(*) FROM machine_rollup_1h WHERE bucket = $1", hour); got != 1 {
		t.Errorf("current hour buckets %d, want 1", got)
	}
	if got := count(t, conn, "SELECT count(*) FROM docker_container_rollup_1m WHERE bucket = $1", recent); got != 4 {
		t.Errorf("container minute rows %d, want 4", got)
	}
	if err, _, dropped := u.State(); err != nil || dropped == 0 {
		t.Errorf("upkeep state err %v dropped %d", err, dropped)
	}
	before := count(t, conn, "SELECT count(*) FROM list_partitions()")
	if err := u.Tick(context.Background()); err != nil {
		t.Fatalf("second upkeep: %v", err)
	}
	if after := count(t, conn, "SELECT count(*) FROM list_partitions()"); after != before {
		t.Errorf("second round changed partitions %d -> %d", before, after)
	}
}

// A batch the sink stores after the rollup cursor passed its minutes is still rolled up on the next round.
func TestUpkeepRollsUpBatchesFlushedLate(t *testing.T) {
	conn := testConn(t)
	empty(t, conn)
	t.Cleanup(func() { empty(t, conn) })
	ctx := context.Background()
	now := time.Now().UTC()
	written := &Written{}
	sink := Sink{DB: conn, Written: written}
	u := &Upkeep{DB: conn, Written: written, Now: func() time.Time { return now }}
	if err := sink.Write(ctx, sampleBatch(now.Add(-time.Minute).Truncate(time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := u.Tick(ctx); err != nil {
		t.Fatalf("upkeep: %v", err)
	}
	late := now.Add(-3 * time.Hour).Truncate(time.Minute)
	if err := sink.Write(ctx, sampleBatch(late)); err != nil {
		t.Fatal(err)
	}
	if err := u.Tick(ctx); err != nil {
		t.Fatalf("upkeep after the late batch: %v", err)
	}
	if got := count(t, conn, "SELECT count(*) FROM machine_rollup_1m WHERE bucket = $1", late); got != 1 {
		t.Errorf("minute buckets for the late batch: %d, want 1", got)
	}
	hour := late.Truncate(time.Hour)
	if got := count(t, conn, "SELECT count(*) FROM machine_rollup_1h WHERE bucket = $1", hour); got != 1 {
		t.Errorf("hour buckets for the late batch: %d, want 1", got)
	}
}
