package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/codelog"
	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/runner"
	"benchwatch/server/internal/history"
	"benchwatch/server/internal/transcripts"
)

// fakeDB answers the migration check with fixed versions and fails every other query with queryErr.
type fakeDB struct {
	versions []int64
	queryErr error
}

type versionRow struct{ versions []int64 }

func (r versionRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("fake: unexpected row")
	}
	p, ok := dest[0].(*[]int64)
	if !ok {
		return errors.New("fake: unexpected row")
	}
	*p = r.versions
	return nil
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func (d *fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, d.queryErr
}
func (d *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, d.queryErr }
func (d *fakeDB) QueryRow(_ context.Context, q string, _ ...any) pgx.Row {
	if strings.Contains(q, "goose_db_version") {
		return versionRow{d.versions}
	}
	return errRow{d.queryErr}
}
func (d *fakeDB) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, d.queryErr
}
func (d *fakeDB) Begin(context.Context) (pgx.Tx, error) { return nil, d.queryErr }
func (d *fakeDB) Ping(context.Context) error            { return nil }

func allMigrations() *fakeDB { return &fakeDB{versions: Migrations, queryErr: io.EOF} }

func status(t *testing.T, s *Server) api.StatusResponse {
	t.Helper()
	s.dbAt = time.Time{}
	return s.StatusNow(context.Background())
}

func okRunner() *runner.Runner {
	r := runner.New(runner.Options{}, nil, nil, nil, nil)
	r.Status.OK(collect.SourceMachine)
	r.Status.OK(collect.SourceDocker)
	r.Status.OK(collect.SourceHistory)
	return r
}

// Health is nominal only while every part does its whole job.
func TestDegradedRules(t *testing.T) {
	nominal := func() api.StatusResponse {
		return api.StatusResponse{Database: api.DatabaseStatus{State: api.DatabaseStateOk},
			Machine: api.CollectorStatus{State: api.CollectorStateOk},
			Docker:  api.CollectorStatus{State: api.CollectorStateDisabled},
			Guard:   api.CollectorStatus{State: api.CollectorStateDisabled}, Scan: api.ScanStatus{State: api.ScanStateOk}}
	}
	s := &Server{}
	if s.degraded(nominal(), false, false) {
		t.Fatal("a nominal status reads degraded")
	}
	code := ptr("EPG_57P01")
	for name, change := range map[string]func(*api.StatusResponse){
		"days not stored":     func(o *api.StatusResponse) { o.Scan.WriteErrorCode = code },
		"history rejected":    func(o *api.StatusResponse) { o.Database.HistoryErrorCode = code },
		"upkeep failed":       func(o *api.StatusResponse) { o.Database.UpkeepErrorCode = code },
		"scan never ran":      func(o *api.StatusResponse) { o.Scan.State = api.ScanStateNeverRan },
		"scan locked":         func(o *api.StatusResponse) { o.Scan.State = api.ScanStateLocked },
		"items dropped":       func(o *api.StatusResponse) { o.Scan.ItemsDropped = 1 },
		"docker unavailable":  func(o *api.StatusResponse) { o.Docker.State = api.CollectorStateUnavailable },
		"schema behind":       func(o *api.StatusResponse) { o.Database.State = api.DatabaseStateMigrating },
		"guard report broken": func(o *api.StatusResponse) { o.Guard.State = api.CollectorStateFailed },
	} {
		o := nominal()
		change(&o)
		if !s.degraded(o, false, false) {
			t.Errorf("%s: not degraded", name)
		}
	}
	if !s.degraded(nominal(), true, false) {
		t.Error("a collector with missing parts: not degraded")
	}
}

// A binary newer than the database's schema reports migrating, not ok.
func TestStatusSaysMigratingWhileAnEmbeddedMigrationIsMissing(t *testing.T) {
	if len(Migrations) < 2 {
		t.Fatalf("embedded migrations: %v", Migrations)
	}
	s := &Server{DB: &fakeDB{versions: Migrations[:len(Migrations)-1]}}
	if st := status(t, s); st.Database.State != api.DatabaseStateMigrating {
		t.Errorf("one migration missing: state %s, want migrating", st.Database.State)
	}
	s.DB = allMigrations()
	if st := status(t, s); st.Database.State != api.DatabaseStateOk ||
		*st.Database.SchemaVersion != Migrations[len(Migrations)-1] {
		t.Errorf("all applied: state %s", st.Database.State)
	}
}

// No Docker wanted reads disabled, not starting; a Docker client that failed to start says so.
func TestDockerThatNeverStartedIsNotStarting(t *testing.T) {
	r := runner.New(runner.Options{}, nil, nil, nil, nil)
	s := &Server{DB: allMigrations(), Runner: r}
	if st := status(t, s); st.Docker.State != api.CollectorStateDisabled {
		t.Errorf("no Docker: state %s, want disabled", st.Docker.State)
	}
	r.Status.Fail(collect.SourceDocker, collect.StateUnavailable, errors.New("x"))
	if st := status(t, s); st.Docker.State != api.CollectorStateUnavailable || st.Health != api.HealthDegraded {
		t.Errorf("client failed: state %s health %s, want unavailable and degraded", st.Docker.State, st.Health)
	}
}

// A partial Docker state left from before the socket vanished must not degrade health while Docker reads absent.
func TestDockerViewHealthFollowsTheReportedState(t *testing.T) {
	partial, okState := collect.SourceStatus{State: collect.StatePartial}, collect.SourceStatus{State: collect.StateOK}
	for name, tc := range map[string]struct {
		st                collect.SourceStatus
		notWanted, absent bool
		state             api.CollectorState
		partial           bool
	}{
		"stale partial, socket absent": {partial, false, true, api.CollectorStateDisabled, false},
		"partial, present":             {partial, false, false, api.CollectorStateOk, true},
		"ok, present":                  {okState, false, false, api.CollectorStateOk, false},
		"never ran, not wanted":        {collect.SourceStatus{}, true, false, api.CollectorStateDisabled, false},
		"never ran, wanted":            {collect.SourceStatus{}, false, false, api.CollectorStateStarting, false},
	} {
		got, p := dockerView(tc.st, time.Second, tc.notWanted, tc.absent, false)
		if got.State != tc.state || p != tc.partial {
			t.Errorf("%s: state %s partial %v, want %s %v", name, got.State, p, tc.state, tc.partial)
		}
	}
	nominal := api.StatusResponse{Database: api.DatabaseStatus{State: api.DatabaseStateOk},
		Machine: api.CollectorStatus{State: api.CollectorStateOk},
		Guard:   api.CollectorStatus{State: api.CollectorStateDisabled}, Scan: api.ScanStatus{State: api.ScanStateOk}}
	s := &Server{}
	var partialNow bool
	nominal.Docker, partialNow = dockerView(partial, time.Second, false, true, false)
	if s.degraded(nominal, partialNow, false) {
		t.Error("stale partial with absent Docker degrades health")
	}
	nominal.Docker, partialNow = dockerView(partial, time.Second, false, false, false)
	if !s.degraded(nominal, partialNow, false) {
		t.Error("partial with present Docker does not degrade health")
	}
}

func scannedScanner(t *testing.T) *transcripts.Scanner {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	sc := transcripts.New(transcripts.Options{Projects: filepath.Join(dir, "projects"),
		Cache: filepath.Join(dir, "cache")}, nil)
	sc.Tick(context.Background())
	return sc
}

// Days the scanner rebuilt but could not store show as a write code and degrade health.
func TestStatusSaysWhenScannedDaysAreNotStored(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "-Users-x-workspace-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","timestamp":"` + time.Now().UTC().Format("2006-01-02T15:04:05.000Z") +
		`","message":{"id":"m1","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`
	if err := os.WriteFile(filepath.Join(proj, "aaaaaaaa-0000-0000-0000-000000000000.jsonl"), []byte(line+"\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	sc := transcripts.New(transcripts.Options{Projects: filepath.Join(dir, "projects"),
		Cache: filepath.Join(dir, "cache")}, nil)
	sc.Tick(context.Background())
	s := &Server{DB: allMigrations(), Runner: okRunner(), Scanner: sc}
	st := status(t, s)
	if st.Scan.State != api.ScanStateOk || st.Scan.WriteErrorCode == nil || !st.Scan.WritePending ||
		st.Health != api.HealthDegraded {
		t.Errorf("scan %s write code %v pending %v health %s; want ok, a code, pending, degraded", st.Scan.State,
			st.Scan.WriteErrorCode, st.Scan.WritePending, st.Health)
	}
}

func TestScannerThatNeverRanDegrades(t *testing.T) {
	dir := t.TempDir()
	sc := transcripts.New(transcripts.Options{Projects: dir, Cache: filepath.Join(dir, "cache")}, nil)
	s := &Server{DB: allMigrations(), Runner: okRunner(), Scanner: sc}
	if st := status(t, s); st.Scan.State != api.ScanStateNeverRan || st.Health != api.HealthDegraded {
		t.Errorf("scan %s health %s, want never_ran and degraded", st.Scan.State, st.Health)
	}
}

// History that drops batches carries its code and the drop counts, and degrades health.
func TestStatusSaysWhenHistoryWritesFail(t *testing.T) {
	r := okRunner()
	rejected := &pgconn.PgError{Code: "23514"}
	r.Status.Fail(collect.SourceHistory, collect.StatePartial, rejected)
	s := &Server{DB: allMigrations(), Runner: r, Scanner: scannedScanner(t)}
	st := status(t, s)
	want := collect.ErrorCode(rejected)
	if st.Database.HistoryErrorCode == nil || *st.Database.HistoryErrorCode != want ||
		st.Database.HistoryPaused || st.Health != api.HealthDegraded {
		t.Errorf("history code %v paused %v health %s; want %s, not paused, degraded",
			st.Database.HistoryErrorCode, st.Database.HistoryPaused, st.Health, want)
	}
	if _, err := json.Marshal(st.Database.DroppedRows); err != nil {
		t.Fatal(err)
	}
}

// A failing upkeep round shows its code and degrades health; the last good round is kept.
func TestStatusSaysWhenUpkeepFails(t *testing.T) {
	u := &history.Upkeep{DB: &fakeDB{queryErr: &pgconn.PgError{Code: "42501"}}}
	_ = u.Tick(context.Background())
	s := &Server{DB: allMigrations(), Runner: okRunner(), Scanner: scannedScanner(t), Upkeep: u}
	st := status(t, s)
	if st.Database.UpkeepErrorCode == nil || *st.Database.UpkeepErrorCode != "EPG_42501" ||
		st.Database.UpkeepLastAt != nil || st.Health != api.HealthDegraded {
		t.Errorf("upkeep code %v last %v health %s; want EPG_42501, none, degraded", st.Database.UpkeepErrorCode,
			st.Database.UpkeepLastAt, st.Health)
	}
}

// Live samples read no processes, so the live snapshot takes top apps from the latest history sample.
func TestLiveMachineSnapshotCarriesTopApps(t *testing.T) {
	now := time.Now()
	r := runner.New(runner.Options{}, nil, nil, nil, nil)
	hist := machineSample(now.Add(-4 * time.Second))
	r.MachineHist.Push(hist.At, hist)
	live := machineSample(now)
	live.TopCPU, live.TopMem = nil, nil
	r.MachineLive.Push(now, live)
	s, h := newServer(t, nil, r)
	s.Now = func() time.Time { return now }
	var m api.MachineResponse
	if rec := get(h, AllowedHosts[0], "/api/machine"); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
		t.Fatalf("status %d", rec.Code)
	}
	if m.Now == nil || len(m.Now.Apps) == 0 {
		t.Fatal("/api/machine now carries no top apps")
	}
	if snap := s.MachineNow(); snap == nil || len(snap.Apps) != len(m.Now.Apps) {
		t.Error("the live machine frame carries no top apps")
	}
	stale := runner.New(runner.Options{}, nil, nil, nil, nil)
	old := machineSample(now.Add(-3 * stale.Options().Hist))
	stale.MachineHist.Push(old.At, old)
	stale.MachineLive.Push(now, live)
	if snap := (&Server{Runner: stale}).MachineNow(); snap == nil || len(snap.Apps) != 0 {
		t.Error("top apps three history steps old are shown as current")
	}
}

// A connection lost mid-request answers from memory, flagged, instead of 500.
func TestMetricsFallBackToMemoryWhenTheDatabaseDrops(t *testing.T) {
	now := time.Now()
	r := memoryRunner(now, 10, 10*time.Second)
	r.Status.OK(collect.SourceHistory)
	r.Status.OK(collect.SourceMachine)
	r.Status.OK(collect.SourceDocker)
	s, h := newServer(t, &fakeDB{versions: Migrations, queryErr: io.ErrUnexpectedEOF}, r)
	s.Now = func() time.Time { return now }
	for _, p := range []string{"/api/machine?range=1h", "/api/docker?range=1h"} {
		rec := get(h, AllowedHosts[0], p)
		var body struct {
			HistoryPaused bool `json:"historyPaused"`
			Partial       bool `json:"partial"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || !body.HistoryPaused || !body.Partial {
			t.Errorf("%s: status %d paused %v partial %v; want 200 from memory, flagged", p, rec.Code,
				body.HistoryPaused, body.Partial)
		}
	}
}

func TestFrameThatBreaksTheSpecIsNotReplayed(t *testing.T) {
	hub, err := NewHub()
	if err != nil {
		t.Fatal(err)
	}
	good := (&Server{}).StatusNow(context.Background())
	hub.Status(good)
	bad := good
	bad.Health = "bogus"
	hub.Status(bad)
	c := hub.Open(context.Background())
	defer c.Close()
	if bytes.Contains(c.pending, []byte("event: status")) {
		t.Error("a new stream replays a status the hub could not send")
	}
}

func rawStream(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	fmt.Fprintf(conn, "GET /api/live HTTP/1.1\r\nHost: %s\r\n\r\n", AllowedHosts[1])
	return conn
}

func serve(t *testing.T, h http.Handler, ln net.Listener) string {
	t.Helper()
	hs := &http.Server{Handler: h}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = hs.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = hs.Close()
		<-done
	})
	return ln.Addr().String()
}

func TestStreamClientThatNeverReadsIsClosed(t *testing.T) {
	r := memoryRunner(time.Now(), 3, 2*time.Second)
	s, _ := newServer(t, nil, r)
	s.LiveWriteTimeout = 200 * time.Millisecond
	h, err := Handler(s)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	done := false
	hs := &http.Server{Handler: h, ConnState: func(_ net.Conn, st http.ConnState) {
		if st == http.StateClosed {
			mu.Lock()
			done = true
			mu.Unlock()
		}
	}}
	go func() { _ = hs.Serve(ln) }()
	defer hs.Close()
	conn := rawStream(t, ln.Addr().String())
	defer conn.Close()
	waitFor(t, func() bool { return s.Hub.Clients() == 1 })
	snap := s.MachineNow()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				s.Hub.Machine(*snap)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	end := time.Now().Add(15 * time.Second)
	for time.Now().Before(end) {
		mu.Lock()
		d := done
		mu.Unlock()
		if d {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the server still holds a stream that has not been read for 15 s")
}

// Past the connection cap a new connection is closed at once, and a freed slot is usable again.
func TestConnectionsPastTheCapAreClosed(t *testing.T) {
	s, h := newServer(t, nil, nil)
	s.Hub.Ping = 20 * time.Millisecond
	inner, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, h, CapListener(inner, 2))
	var held []net.Conn
	for range 2 {
		c := rawStream(t, addr)
		defer c.Close()
		br := bufio.NewReader(c)
		if l, err := br.ReadString('\n'); err != nil || !strings.Contains(l, "200") {
			t.Fatalf("held stream: %q %v", l, err)
		}
		held = append(held, c)
	}
	extra, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	_ = extra.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := extra.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("connection past the cap: read %d %v, want EOF", n, err)
	}
	_ = held[0].Close()
	ok := false
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !ok; time.Sleep(50 * time.Millisecond) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			continue
		}
		fmt.Fprintf(c, "GET /api/status HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", AllowedHosts[0])
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		l, _ := bufio.NewReader(c).ReadString('\n')
		ok = strings.Contains(l, "200")
		_ = c.Close()
	}
	if !ok {
		t.Error("a freed slot does not serve again")
	}
}

func TestHTTPErrorLogWritesCodesOnly(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	errorLog := log.New(errorCodes{&codelog.Limiter{Every: time.Minute}}, "", 0)
	errorLog.Printf("http: panic serving 127.0.0.1:5000: /Users/someone/secret-path")
	if out := buf.String(); !strings.Contains(out, "http: EPANIC") || strings.Contains(out, "secret-path") {
		t.Errorf("log line %q", out)
	}
}

func TestStatusCarriesTheScanInterval(t *testing.T) {
	dir := t.TempDir()
	opts := transcripts.Options{Projects: dir, Cache: filepath.Join(dir, "cache"), Every: 30 * time.Second}
	sc := transcripts.New(opts, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	s := &Server{DB: allMigrations(), Runner: okRunner(), Scanner: sc}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := status(t, s)
		if st.Scan.IntervalSeconds != nil {
			if *st.Scan.IntervalSeconds != 30 {
				t.Fatalf("interval %d, want 30", *st.Scan.IntervalSeconds)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no scan interval after 10 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
