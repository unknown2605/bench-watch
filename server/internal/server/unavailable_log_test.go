package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"benchwatch/server/internal/codelog"
)

func captureLogEvery(t *testing.T, every time.Duration) (*bytes.Buffer, *time.Time) {
	t.Helper()
	var buf bytes.Buffer
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	oldOut, oldFlags, oldLimiter := log.Writer(), log.Flags(), apiErrors
	log.SetOutput(&buf)
	log.SetFlags(0)
	apiErrors = &codelog.Limiter{Every: every, Now: func() time.Time { return now }}
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags); apiErrors = oldLimiter })
	return &buf, &now
}

func captureLog(t *testing.T) (*bytes.Buffer, *time.Time) { return captureLogEvery(t, time.Minute) }

var unavailableRoutes = []struct{ target, route string }{
	{"/api/meta", "/api/meta"}, {"/api/overview", "/api/overview"}, {"/api/tokens", "/api/tokens"},
	{"/api/context", "/api/context"}, {"/api/sessions", "/api/sessions"},
	{"/api/sessions/abcd1234?project=benchwatch", "/api/sessions/{session}"},
	{"/api/subagents", "/api/subagents"}, {"/api/fills", "/api/fills"}, {"/api/guard", "/api/guard"},
}

// /api/machine and /api/docker are not here: a failed database read there falls back to the rings and answers 200.
func TestEveryUnavailableAnswerLogsOneCodeLinePerRoute(t *testing.T) {
	buf, now := captureLog(t)
	const secret = "password authentication details"
	db := &fakeDB{versions: Migrations, queryErr: &pgconn.PgError{Code: "57P01", Message: secret}}
	_, h := newServer(t, db, okRunner())
	for _, c := range unavailableRoutes {
		buf.Reset()
		if rec := get(h, AllowedHosts[0], c.target); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503", c.target, rec.Code)
			continue
		}
		if want := "api " + c.route + ": EPG_57P01\n"; buf.String() != want {
			t.Errorf("%s: log %q, want %q", c.target, buf.String(), want)
		}
		buf.Reset()
		get(h, AllowedHosts[0], c.target)
		if buf.Len() != 0 {
			t.Errorf("%s: repeat within the minute logged %q", c.target, buf.String())
		}
		*now = now.Add(time.Minute)
		get(h, AllowedHosts[0], c.target)
		if strings.Count(buf.String(), "\n") != 1 || strings.Contains(buf.String(), secret) {
			t.Errorf("%s: after a minute log %q, want one code line", c.target, buf.String())
		}
	}
}

// The limiter would hide a second log call for the same failure, so this one never suppresses.
func TestAnUnavailableAnswerLogsExactlyOneLinePerRequest(t *testing.T) {
	buf, _ := captureLogEvery(t, 0)
	failing := &fakeDB{versions: Migrations, queryErr: &pgconn.PgError{Code: "57P01"}}
	for name, c := range map[string]struct {
		db   DB
		code string
	}{"database down": {failing, "EPG_57P01"}, "no database": {nil, "ENODB"}} {
		_, h := newServer(t, c.db, okRunner())
		for _, r := range unavailableRoutes {
			buf.Reset()
			if rec := get(h, AllowedHosts[0], r.target); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("%s %s: status %d, want 503", name, r.target, rec.Code)
				continue
			}
			if want := "api " + r.route + ": " + c.code + "\n"; buf.String() != want {
				t.Errorf("%s %s: log %q, want exactly %q", name, r.target, buf.String(), want)
			}
		}
	}
}

type retryable struct{}

func (retryable) Error() string      { return "retry me" }
func (retryable) SafeToRetry() bool  { return true }
func wrap(err error) error           { return fmt.Errorf("reading: %w", err) }
func pg(code string) *pgconn.PgError { return &pgconn.PgError{Code: code, Message: "secret text"} }

func pipeConn(t *testing.T) *pgconn.PgConn {
	t.Helper()
	cfg, err := pgconn.ParseConfig("postgres://u@127.0.0.1:1/d")
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	go io.Copy(io.Discard, server)
	t.Cleanup(func() { server.Close() })
	pc, err := pgconn.Construct(&pgconn.HijackedConn{Conn: client, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return pc
}

func TestUnavailableCodeNamesEveryAcceptedErrorAndNothingElse(t *testing.T) {
	ctx := context.Background()
	closed := pipeConn(t)
	if err := closed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, closedConn := closed.Prepare(ctx, "s", "select 1", nil)
	if !errors.Is(closedConn, pgconn.ErrConnClosed) || !pgconn.SafeToRetry(closedConn) {
		t.Fatalf("setup: %v is not a safe-to-retry closed connection", closedConn)
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	_, doneErr := pipeConn(t).Prepare(done, "s", "select 1", nil)
	if !pgconn.SafeToRetry(doneErr) || !errors.Is(doneErr, context.Canceled) {
		t.Fatalf("setup: %v is not a safe-to-retry cancelled context", doneErr)
	}
	cfg, err := pgconn.ParseConfig("postgres://u@127.0.0.1:1/d")
	if err != nil {
		t.Fatal(err)
	}
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("boom") }
	cfg.Fallbacks = nil
	_, dialErr := pgconn.ConnectConfig(ctx, cfg)
	var dialCE *pgconn.ConnectError
	if !errors.As(dialErr, &dialCE) {
		t.Fatalf("setup: %v is not a connect error", dialErr)
	}
	pool, err := pgxpool.New(ctx, "postgres://u@127.0.0.1:1/d")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	_, closedPool := pool.Acquire(ctx)
	_, connectErr := pgconn.Connect(ctx, "host=/nonexistent-benchwatch-test port=1 connect_timeout=1")
	var ce *pgconn.ConnectError
	if !errors.As(connectErr, &ce) {
		t.Fatalf("setup: %v is not a connect error", connectErr)
	}
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"no database":          {errNoDB, "ENODB"},
		"EOF":                  {io.EOF, "EEOF"},
		"unexpected EOF":       {io.ErrUnexpectedEOF, "EUNEXPECTED_EOF"},
		"deadline":             {context.DeadlineExceeded, "ETIMEDOUT"},
		"closed pool":          {closedPool, "ECLOSED_POOL"},
		"closed connection":    {closedConn, "ECLOSED_CONN"},
		"conn closed text":     {errors.New("conn closed"), "ECLOSED_CONN"},
		"network error":        {&net.OpError{Op: "read", Err: errors.New("boom")}, "ENET"},
		"network timeout":      {&net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, "ETIMEDOUT"},
		"cancelled context":    {doneErr, "ECANCELED"},
		"connect fallback":     {dialErr, "EPG_CONNECT"},
		"DNS error":            {&net.DNSError{Err: "no such host", Name: "db"}, "ENET"},
		"canceled network":     {&net.OpError{Op: "dial", Err: context.Canceled}, "ECANCELED"},
		"refused":              {&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "ECONNREFUSED"},
		"reset":                {syscall.ECONNRESET, "ECONNRESET"},
		"other errno":          {syscall.EAGAIN, "ERRNO"},
		"connect error":        {connectErr, "ENOENT"},
		"safe to retry":        {retryable{}, "ERETRY"},
		"shutdown":             {pg("57P01"), "EPG_57P01"},
		"connection exception": {pg("08006"), "EPG_08006"},
		"resources":            {pg("53300"), "EPG_53300"},
		"missing table":        {pg("42P01"), "EPG_42P01"},
		"missing function":     {pg("42883"), "EPG_42883"},
		"short SQLSTATE":       {pg("08"), "EPG_OTHER"},
	} {
		for form, err := range map[string]error{"bare": c.err, "wrapped": wrap(wrap(c.err))} {
			if got := unavailableCode(err); got != c.want {
				t.Errorf("%s (%s): code %q, want %q", name, form, got, c.want)
			}
			if !unavailable(err) {
				t.Errorf("%s (%s): not unavailable", name, form)
			}
		}
	}
	refused := map[string]error{"nil": nil, "plain": errors.New("boom"), "no rows": pgx.ErrNoRows,
		"canceled":                         context.Canceled,
		"unique violation beside EOF":      errors.Join(pg("23505"), io.EOF),
		"unique violation beside deadline": errors.Join(pg("23505"), context.DeadlineExceeded)}
	for _, code := range []string{"23505", "42601", "42P02", "22P02", "40001", "54000", "58000", "0A000"} {
		refused["SQLSTATE "+code] = pg(code)
	}
	for name, err := range refused {
		for form, e := range map[string]error{"bare": err, "wrapped": wrap(err)} {
			if err == nil && form == "wrapped" {
				continue
			}
			if got := unavailableCode(e); got != "" || unavailable(e) {
				t.Errorf("%s (%s): code %q, unavailable %v, want none", name, form, got, unavailable(e))
			}
		}
	}
}

type countingDB struct {
	*fakeDB
	reads int
}

func (d *countingDB) Query(ctx context.Context, q string, a ...any) (pgx.Rows, error) {
	d.reads++
	return d.fakeDB.Query(ctx, q, a...)
}

func (d *countingDB) QueryRow(ctx context.Context, q string, a ...any) pgx.Row {
	d.reads++
	return d.fakeDB.QueryRow(ctx, q, a...)
}

func TestOtherAnswersLogNothing(t *testing.T) {
	buf, _ := captureLogEvery(t, 0)
	_, h := newServer(t, nil, nil)
	for target, want := range map[string]int{
		"/api/overview?range=all&from=2026-01-01&to=2026-01-02": http.StatusBadRequest,
		"/api/tokens?from=2026-01-01":                           http.StatusBadRequest,
		"/api/status":                                           http.StatusOK,
		"/api/context?client=codex":                             http.StatusUnprocessableEntity,
		"/api/subagents?client=codex":                           http.StatusUnprocessableEntity,
		"/api/fills?client=codex":                               http.StatusUnprocessableEntity,
		"/api/guard?client=codex":                               http.StatusUnprocessableEntity,
		"/api/nothing-here":                                     http.StatusNotFound,
	} {
		if rec := get(h, AllowedHosts[0], target); rec.Code != want {
			t.Errorf("%s: status %d, want %d", target, rec.Code, want)
		}
	}
	missing := &fakeDB{versions: Migrations, queryErr: pgx.ErrNoRows}
	_, h = newServer(t, missing, okRunner())
	if rec := get(h, AllowedHosts[0], "/api/sessions/abcd1234?project=benchwatch"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown session: status %d, want 404", rec.Code)
	}
	if buf.Len() != 0 {
		t.Errorf("log %q, want nothing", buf.String())
	}
}

func TestAnAnswerFromMemoryAfterAFailedDatabaseReadLogsNothing(t *testing.T) {
	buf, _ := captureLogEvery(t, 0)
	now := time.Now()
	db := &countingDB{fakeDB: &fakeDB{versions: Migrations, queryErr: &pgconn.PgError{Code: "57P01"}}}
	s, h := newServer(t, db, memoryRunner(now, 10, 10*time.Second))
	s.Now = func() time.Time { return now }
	for _, target := range []string{"/api/machine?range=1h", "/api/docker?range=1h"} {
		db.reads = 0
		if rec := get(h, AllowedHosts[0], target); rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200 from memory", target, rec.Code)
		}
		if db.reads == 0 {
			t.Errorf("%s: the database was never read, so the case proves nothing", target)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("log %q, want nothing", buf.String())
	}
}
