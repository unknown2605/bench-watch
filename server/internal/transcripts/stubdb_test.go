package transcripts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// stubDB fails or blocks on demand and counts transactions, for runs that must not reach a write.
type stubDB struct {
	mu       sync.Mutex
	queryErr error
	rowErr   error
	block    bool
	begins   int
}

type stubRow struct {
	ctx   context.Context
	err   error
	block bool
}

func (r stubRow) Scan(dest ...any) error {
	if r.block {
		<-r.ctx.Done()
		return r.ctx.Err()
	}
	if r.err != nil {
		return r.err
	}
	for _, d := range dest {
		if b, ok := d.(*bool); ok {
			*b = false
		}
	}
	return nil
}

type emptyRows struct{}

func (emptyRows) Close()                                       {}
func (emptyRows) Err() error                                   { return nil }
func (emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (emptyRows) Next() bool                                   { return false }
func (emptyRows) Scan(...any) error                            { return nil }
func (emptyRows) Values() ([]any, error)                       { return nil, nil }
func (emptyRows) RawValues() [][]byte                          { return nil }
func (emptyRows) Conn() *pgx.Conn                              { return nil }
func (emptyRows) TypeMap() *pgtype.Map                         { return nil }

func (d *stubDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("stub")
}

func (d *stubDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if d.queryErr != nil {
		return nil, d.queryErr
	}
	return emptyRows{}, nil
}

func (d *stubDB) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	return stubRow{ctx: ctx, err: d.rowErr, block: d.block}
}

func (d *stubDB) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("stub")
}

func (d *stubDB) Begin(ctx context.Context) (pgx.Tx, error) {
	d.mu.Lock()
	d.begins++
	d.mu.Unlock()
	if d.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, errors.New("stub")
}

func (d *stubDB) beginCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.begins
}

func oneTranscript(t *testing.T) (projects, file string) {
	t.Helper()
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "-Users-x-workspace-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(proj, "aaaaaaaa-0000-0000-0000-000000000000.jsonl")
	appendLines(t, file, callLine("m1", time.Now().Add(-time.Minute).Unix()))
	return filepath.Join(dir, "projects"), file
}

var errRefused = &pgconn.PgError{Code: "57P03"}

// A failed token_bands read must not store rows banded by the defaults.
func TestFailedBandsReadWritesNothing(t *testing.T) {
	projects, _ := oneTranscript(t)
	db := &stubDB{queryErr: errRefused}
	s := New(Options{Projects: projects, Cache: filepath.Join(t.TempDir(), "cache")}, db)
	s.Tick(context.Background())
	st := s.Status()
	if st.State != StateOK || st.DBErrorCode != "EPG_57P03" || !st.DBPending || db.beginCount() != 0 {
		t.Fatalf("state %q write code %q pending %v transactions %d; want ok, EPG_57P03, pending, none",
			st.State, st.DBErrorCode, st.DBPending, db.beginCount())
	}
}

// A failed check of the database must not pass for an empty one and rewrite the whole history.
func TestFailedDigestCheckIsNotAnEmptyDatabase(t *testing.T) {
	projects, _ := oneTranscript(t)
	db := &stubDB{rowErr: errRefused}
	s := New(Options{Projects: projects, Cache: filepath.Join(t.TempDir(), "cache")}, db)
	s.Tick(context.Background())
	st := s.Status()
	s.mu.RLock()
	loaded := s.loaded
	s.mu.RUnlock()
	if loaded || st.DBErrorCode != "EPG_57P03" || !st.DBPending || db.beginCount() != 0 {
		t.Fatalf("loaded %v write code %q pending %v transactions %d; want not loaded, EPG_57P03, pending, none",
			loaded, st.DBErrorCode, st.DBPending, db.beginCount())
	}
}

func TestStalledDatabaseTimesTheWriteOut(t *testing.T) {
	projects, _ := oneTranscript(t)
	db := &stubDB{block: true}
	s := New(Options{Projects: projects, Cache: filepath.Join(t.TempDir(), "cache"),
		WriteTimeout: 50 * time.Millisecond}, db)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Tick(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Tick still blocked on the database after 10 s")
	}
	if st := s.Status(); st.State != StateOK || st.DBErrorCode != "ETIMEDOUT" || !st.DBPending {
		t.Fatalf("state %q write code %q pending %v; want ok, ETIMEDOUT, pending", st.State, st.DBErrorCode, st.DBPending)
	}
}

// A run that cannot rebuild every shard says why its days were not stored, without retrying every tick.
func TestPartialRunReportsWhyNothingWasStored(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 000 file")
	}
	projects, file := oneTranscript(t)
	cache := filepath.Join(t.TempDir(), "cache")
	s := New(Options{Projects: projects, Cache: cache}, nil)
	s.Tick(context.Background())
	shards, err := filepath.Glob(filepath.Join(cache, "shards", "*.json"))
	if err != nil || len(shards) != 1 {
		t.Fatalf("shards %v %v", shards, err)
	}
	data, err := os.ReadFile(shards[0])
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		if data[i] == '1' {
			data[i] = '2'
		}
	}
	if err := os.WriteFile(shards[0], data, 0o600); err != nil {
		t.Fatal(err)
	}
	appendLines(t, file, callLine("m2", time.Now().Unix()))
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o600) })
	db := &stubDB{}
	p := New(Options{Projects: projects, Cache: cache}, db)
	p.Tick(context.Background())
	st := p.Status()
	if st.Unrebuilt != 1 || st.DBErrorCode != "EPARTIAL" || st.DBPending || db.beginCount() != 0 {
		t.Fatalf("unrebuilt %d write code %q pending %v transactions %d; want 1, EPARTIAL, not pending, none",
			st.Unrebuilt, st.DBErrorCode, st.DBPending, db.beginCount())
	}
}
