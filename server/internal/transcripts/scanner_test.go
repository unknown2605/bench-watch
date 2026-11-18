package transcripts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/scan"
)

func TestRebuildWaitFollowsCPUNotWallTime(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects", "-Users-x-workspace-proj")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","timestamp":"` + time.Now().UTC().Format("2006-01-02T15:04:05.000Z") +
		`","message":{"id":"m1","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":1,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}` + "\n"
	file := filepath.Join(projects, "aaaaaaaa-0000-0000-0000-000000000000.jsonl")
	if err := os.WriteFile(file, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var cpu time.Duration
	step := 1500 * time.Millisecond
	s := New(Options{Projects: filepath.Join(dir, "projects"), Cache: filepath.Join(dir, "cache"),
		CPU: func() time.Duration { cpu += step; return cpu }}, nil)

	if wait := s.Tick(context.Background()); wait != Every || s.kind != aggregate.Scheduled {
		t.Fatalf("the first run (%v) waits %v, want the %v floor: a start never stalls live updates", s.kind, wait,
			Every)
	}
	if st := s.Status(); st.State != StateOK || st.Transcripts != 1 {
		t.Fatalf("status after the rebuild: state %q, transcripts %d", st.State, st.Transcripts)
	}
	// Without a database the write stays pending, so every next run rebuilds again.
	if wait := s.Tick(context.Background()); wait != 15*time.Second || s.kind != aggregate.Incremental {
		t.Fatalf("an incremental rebuild (%v) that used 1.5 s of CPU waits %v, want 15s", s.kind, wait)
	}
	s.start = s.start.Add(-aggregate.FullEvery)
	if wait := s.Tick(context.Background()); wait != Every || s.kind != aggregate.Scheduled {
		t.Fatalf("the hourly check (%v) waits %v, want the %v floor", s.kind, wait, Every)
	}
	step = 0
	if wait := s.Tick(context.Background()); wait != Every {
		t.Fatalf("a rebuild that used no CPU waits %v, want the %v floor", wait, Every)
	}
}

// acceptDB takes every write and holds nothing, so a run's write succeeds and the next idle run can skip.
type acceptDB struct{}

type falseRow struct{}

func (falseRow) Scan(dest ...any) error {
	for _, d := range dest {
		if b, ok := d.(*bool); ok {
			*b = false
		}
	}
	return nil
}

type acceptTx struct {
	pgx.Tx
}

func (acceptTx) QueryRow(context.Context, string, ...any) pgx.Row { return falseRow{} }
func (acceptTx) Commit(context.Context) error                     { return nil }
func (acceptTx) Rollback(context.Context) error                   { return nil }
func (acceptTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (acceptTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	var n int64
	for src.Next() {
		if _, err := src.Values(); err != nil {
			return n, err
		}
		n++
	}
	return n, src.Err()
}

func (acceptDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (acceptDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return emptyRows{}, nil }
func (acceptDB) QueryRow(context.Context, string, ...any) pgx.Row        { return falseRow{} }
func (d acceptDB) CopyFrom(ctx context.Context, n pgx.Identifier, c []string, s pgx.CopyFromSource) (int64,
	error) {
	return acceptTx{}.CopyFrom(ctx, n, c, s)
}
func (acceptDB) Begin(context.Context) (pgx.Tx, error) { return acceptTx{}, nil }

// Another process (scan-dump) can absorb a transcript change into the shared cache; the next run must still replay it.
func TestChangeAbsorbedByAnotherProcessIsReplayed(t *testing.T) {
	projects, file := oneTranscript(t)
	cache := filepath.Join(t.TempDir(), "cache")
	s := New(Options{Projects: projects, Cache: cache}, acceptDB{})
	ctx := context.Background()
	s.Tick(ctx)
	first := s.Status()
	if first.State != StateOK || first.DBErrorCode != "" || first.UniqueCalls != 1 {
		t.Fatalf("first run: state %q write %q calls %d", first.State, first.DBErrorCode, first.UniqueCalls)
	}
	s.Tick(ctx)
	if st := s.Status(); !st.LastChange.Equal(first.LastChange) {
		t.Fatal("a run with nothing changed rebuilt")
	}
	appendLines(t, file, callLine("m2", time.Now().Unix()))
	other, err := scan.UpdateShards(projects, cache, nil)
	if err != nil || other.Changed != 1 {
		t.Fatalf("the other process saw %d changes, err %v", other.Changed, err)
	}
	if again, err := scan.UpdateShards(projects, cache, nil); err != nil || again.Changed != 0 {
		t.Fatalf("the cache still reports %d changes, err %v", again.Changed, err)
	}
	s.Tick(ctx)
	if st := s.Status(); st.UniqueCalls != 2 || st.LastChange.Equal(first.LastChange) {
		t.Fatalf("after the absorbed change: calls %d, rebuilt %v; want 2 calls and a rebuild", st.UniqueCalls,
			!st.LastChange.Equal(first.LastChange))
	}
}

// The heap a rebuild grew goes back to the system once, on the first run with nothing to rebuild, and again when a
// quiet spell lets Live drop the shards it keeps.
func TestIdleRunReleasesWhatTheLastRebuildGrew(t *testing.T) {
	projects, file := oneTranscript(t)
	releases := 0
	s := New(Options{Projects: projects, Cache: filepath.Join(t.TempDir(), "cache"),
		Release: func() { releases++ }}, acceptDB{})
	ctx := context.Background()
	type step struct {
		name   string
		change func()
		want   int
	}
	idle := func() {}
	grow := func() { appendLines(t, file, callLine(fmt.Sprintf("m%d", time.Now().UnixNano()), time.Now().Unix())) }
	steps := []step{{"first, full replay", idle, 1}, {"idle after a full replay", idle, 1}, {"incremental", grow, 1},
		{"first idle run", idle, 2}, {"still idle", idle, 2}, {"incremental again", grow, 2},
		{"incremental in a row", grow, 2}, {"idle at last", idle, 3}}
	for range restAfter - 2 {
		steps = append(steps, step{"quiet", idle, 3})
	}
	steps = append(steps, step{"quiet long enough to drop the kept shards", idle, 4}, step{"quiet after", idle, 4},
		step{"a change after the rest", grow, 4}, step{"idle again", idle, 5})
	for i, st := range steps {
		st.change()
		s.Tick(ctx)
		if releases != st.want {
			t.Fatalf("run %d, %s: %d releases, want %d", i, st.name, releases, st.want)
		}
	}
	if got := s.Status(); got.UniqueCalls != 5 {
		t.Fatalf("after the rest: %d calls, want 5", got.UniqueCalls)
	}
}
