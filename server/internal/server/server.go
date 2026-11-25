// Package server answers the generated API from the collectors' memory, the scanner and PostgreSQL.
package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"benchwatch/server/db"
	"benchwatch/server/internal/api"
	"benchwatch/server/internal/codelog"
	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/runner"
	"benchwatch/server/internal/guard"
	"benchwatch/server/internal/history"
	"benchwatch/server/internal/store"
	"benchwatch/server/internal/transcripts"
)

type DB interface {
	transcripts.DB
	Ping(ctx context.Context) error
}

type Server struct {
	DB      DB
	Runner  *runner.Runner
	Scanner *transcripts.Scanner
	Guard   *transcripts.Guard
	// GuardInstall reads the guard install; nil answers source none.
	GuardInstall *guard.InstallReader
	Upkeep       *history.Upkeep
	Hub          *Hub
	Now          func() time.Time
	// QueryTimeout bounds each handler's database work, so a stalled PostgreSQL answers 503 instead of hanging.
	QueryTimeout time.Duration
	// LiveWriteTimeout bounds each event stream write; tests shorten it.
	LiveWriteTimeout time.Duration
	// ReadTimeout bounds the read side of every request but the event stream (RequestReadTimeout by default).
	ReadTimeout time.Duration

	dbMu     sync.Mutex
	dbAt     time.Time
	dbState  api.DatabaseState
	dbSchema *int64
}

var _ api.StrictServerInterface = (*Server)(nil)

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) q() *store.Queries { return store.New(s.DB) }

func (s *Server) dbCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	d := s.QueryTimeout
	if d <= 0 {
		d = 10 * time.Second
	}
	return context.WithTimeout(ctx, d)
}

// AllowedHosts are the Host headers the server answers, against DNS rebinding; anything else gets 421.
var AllowedHosts = []string{"benchwatch-next.localhost", "benchwatch.localhost", "127.0.0.1:8790"}

// Handler wraps the generated handler with the Host allowlist and the headers every answer carries.
func Handler(s *Server) (http.Handler, error) {
	inner, err := api.NewHandlerWith(s, api.HandlerOptions{LiveWriteTimeout: s.LiveWriteTimeout,
		OnError: func(route string, err error) { apiErrors.Print("api "+route, codelog.Of(err)) }})
	if err != nil {
		return nil, err
	}
	read := s.ReadTimeout
	if read <= 0 {
		read = RequestReadTimeout
	}
	return HostGuard(inner, read), nil
}

// RequestReadTimeout matches the server's WriteTimeout; a global ReadTimeout would also end every event stream.
const RequestReadTimeout = 30 * time.Second

var apiErrors = &codelog.Limiter{Every: time.Minute}

// ErrorLog takes net/http's own messages, which can quote request text, and logs only a fixed code for them.
var ErrorLog = log.New(errorCodes{&codelog.Limiter{Every: time.Minute}}, "", 0)

type errorCodes struct{ limit *codelog.Limiter }

func (e errorCodes) Write(p []byte) (int, error) {
	c := "EHTTP"
	if bytes.Contains(p, []byte("panic")) {
		c = "EPANIC"
	}
	e.limit.Print("http", c)
	return len(p), nil
}

// MaxConns caps the open connections; past it a new one replaces an evictable one or is closed when accepted.
const MaxConns = 64

// CapListener closes connections past n instead of queueing them, so a flood cannot pile up sockets. With Track as
// the server's ConnState, a full listener closes the oldest connection that is not inside a request instead.
func CapListener(ln net.Listener, n int) *ConnCap {
	parked := max(n, 1) * parkedPerConn
	return &ConnCap{Listener: ln, n: n, conns: map[*capConn]struct{}{}, parkedMax: parked,
		pace: closeWait / time.Duration(parked), queue: make(chan accepted, parked)}
}

// closeWait is net/http's rstAvoidanceDelay: after half-closing a connection its goroutine sleeps this long,
// whether or not the socket is still open, so evicting the connection frees a slot but not that goroutine.
const closeWait = 500 * time.Millisecond

// parkedPerConn sizes the cap on those goroutines; 8 a slot measured about 8 MB at MaxConns.
const parkedPerConn = 8

type ConnCap struct {
	net.Listener
	n int

	mu    sync.Mutex
	conns map[*capConn]struct{}
	seq   uint64

	parked, parkedMax int
	pace              time.Duration
	paceAt            time.Time

	fill  sync.Once
	queue chan accepted
}

type accepted struct {
	conn net.Conn
	err  error
}

// take moves connections out of the kernel's queue, which resets clients once it is full (128 on macOS), into
// one that keeps arrival order for parkedMax more.
func (l *ConnCap) take() {
	defer close(l.queue)
	for {
		c, err := l.Listener.Accept()
		l.queue <- accepted{c, err}
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// Close also drops the connections still waiting for room, which no Accept will take any more.
func (l *ConnCap) Close() error {
	err := l.Listener.Close()
	l.fill.Do(func() { close(l.queue) })
	go func() {
		for a := range l.queue {
			if a.conn != nil {
				_ = a.conn.Close()
			}
		}
	}()
	return err
}

func (l *ConnCap) Accept() (net.Conn, error) {
	l.fill.Do(func() { go l.take() })
	for {
		l.awaitRoom()
		a, open := <-l.queue
		if !open {
			return nil, net.ErrClosed
		}
		if a.err != nil {
			return nil, a.err
		}
		c := a.conn
		l.mu.Lock()
		var victim *capConn
		if len(l.conns) >= l.n {
			victim = l.evictable()
			delete(l.conns, victim)
		}
		if len(l.conns) >= l.n {
			l.mu.Unlock()
			_ = c.Close()
			continue
		}
		l.seq++
		cc := &capConn{Conn: c, cap: l, seq: l.seq}
		l.conns[cc] = struct{}{}
		l.mu.Unlock()
		if victim != nil {
			_ = victim.Conn.Close()
		}
		return cc, nil
	}
}

// awaitRoom holds Accept back while refused connections park goroutines faster than closeWait ends them. The
// rest of a flood waits in arrival order, so a client's turn comes a few paces later.
func (l *ConnCap) awaitRoom() {
	for {
		l.mu.Lock()
		wait := time.Until(l.paceAt)
		if l.parked >= l.parkedMax {
			wait = max(wait, l.pace)
		}
		l.mu.Unlock()
		if wait <= 0 {
			return
		}
		time.Sleep(wait)
	}
}

// evictable is the oldest connection net/http half-closed after its last answer, else the oldest it reported as
// silent since it connected or idle between requests; one inside a request is never taken.
func (l *ConnCap) evictable() *capConn {
	var done, quiet *capConn
	for c := range l.conns {
		switch c.state {
		case stateDone:
			if done == nil || c.seq < done.seq {
				done = c
			}
		case stateNew, stateIdle:
			if quiet == nil || c.seq < quiet.seq {
				quiet = c
			}
		}
	}
	if done != nil {
		return done
	}
	return quiet
}

// Track is the http.Server ConnState hook; a connection it never heard of is never evicted.
func (l *ConnCap) Track(c net.Conn, st http.ConnState) {
	cc, ok := c.(*capConn)
	if !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch st {
	case http.StateNew:
		cc.state = stateNew
	case http.StateIdle:
		cc.state = stateIdle
	case http.StateClosed:
		delete(l.conns, cc)
	default:
		cc.state = stateBusy
	}
}

const (
	stateUntracked = iota
	stateNew
	stateIdle
	stateBusy
	stateDone
)

type capConn struct {
	net.Conn
	cap   *ConnCap
	seq   uint64
	state int
	park  int
}

const (
	parkNone = iota
	parkCounted
	parkOver
)

// net/http calls Close as the connection's goroutine ends, which for an evicted one is later than its socket.
func (c *capConn) Close() error {
	err := c.Conn.Close()
	c.cap.mu.Lock()
	delete(c.cap.conns, c)
	if c.park == parkCounted {
		c.cap.parked--
	}
	c.park = parkOver
	c.cap.mu.Unlock()
	return err
}

// net/http half-closes a connection it answered and will not reuse, then keeps it 500 ms for the client to read
// the answer. Without this method no FIN went out and that wait counted as a request, which eviction spares.
func (c *capConn) CloseWrite() error {
	l := c.cap
	l.mu.Lock()
	if c.state != stateUntracked {
		c.state = stateDone
	}
	if c.park == parkNone {
		c.park = parkCounted
		l.parked++
		// The first n in a row are free, so a refusal now and then delays nobody.
		if floor := time.Now().Add(-time.Duration(l.n) * l.pace); l.paceAt.Before(floor) {
			l.paceAt = floor
		}
		l.paceAt = l.paceAt.Add(l.pace)
	}
	l.mu.Unlock()
	if hc, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return hc.CloseWrite()
	}
	return nil
}

func HostGuard(next http.Handler, readTimeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		if !api.Admit(w, r, readTimeout) {
			return
		}
		if !allowedHost(r.Host) {
			api.WriteError(w, http.StatusMisdirectedRequest, api.ErrorResponseCodeBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedHost(host string) bool {
	host = strings.ToLower(host)
	for _, h := range AllowedHosts {
		if host == h {
			return true
		}
	}
	return false
}

// logUnavailable logs the code of a database failure answered 503, through the same limiter as OnError.
func logUnavailable(route string, err error) { apiErrors.Print("api "+route, unavailableCode(err)) }

// unavailable is true for errors that mean PostgreSQL cannot answer now, which the API reports as 503.
func unavailable(err error) bool { return unavailableCode(err) != "" }

// unavailableCode is the one list of errors answered 503, and names each by a fixed code, never its text; "" is no
// outage. The specific code of codelog.Of wins; the kind below is the fallback.
func unavailableCode(err error) string {
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		c := pgErr.Code
		if strings.HasPrefix(c, "08") || strings.HasPrefix(c, "57") || strings.HasPrefix(c, "53") ||
			c == "42P01" || c == "42883" {
			return specificCode(err, "EPG_OTHER")
		}
		return ""
	}
	var ne net.Error
	var ce *pgconn.ConnectError
	var kind string
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind = "ETIMEDOUT"
	case errors.As(err, &ne) && ne.Timeout():
		kind = "ETIMEDOUT"
	case errors.As(err, &ne):
		kind = "ENET"
	case errors.As(err, &ce):
		kind = "EPG_CONNECT"
	case errors.Is(err, errNoDB):
		kind = "ENODB"
	case errors.Is(err, io.EOF):
		kind = "EEOF"
	case errors.Is(err, io.ErrUnexpectedEOF):
		kind = "EUNEXPECTED_EOF"
	case errors.Is(err, pgconn.ErrConnClosed) || strings.Contains(err.Error(), "conn closed"):
		kind = "ECLOSED_CONN"
	case pgconn.SafeToRetry(err):
		kind = "ERETRY"
	case strings.Contains(err.Error(), "closed pool"):
		kind = "ECLOSED_POOL"
	default:
		return ""
	}
	return specificCode(err, kind)
}

func specificCode(err error, kind string) string {
	if c := codelog.Of(err); c != "" && c != "EOTHER" && c != "ok" {
		return c
	}
	return kind
}

var errNoDB = errors.New("no database")

func notFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func date(t time.Time) openapi_types.Date {
	return openapi_types.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func ptr[T any](v T) *T { return &v }

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func historyPaused(r *runner.Runner) bool {
	if r == nil {
		return true
	}
	st := r.Status.Get(collect.SourceHistory).State
	return st == collect.StatePaused
}

// Migrations are the versions the binary embeds; the database is `migrating` until every one is applied.
var Migrations = embeddedVersions()

func embeddedVersions() []int64 {
	names, _ := fs.Glob(db.Migrations, "migrations/*.sql")
	var out []int64
	for _, n := range names {
		digits, _, _ := strings.Cut(path.Base(n), "_")
		if v, err := strconv.ParseInt(digits, 10, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// database reports the PostgreSQL state, checked at most every 2 s.
func (s *Server) database(ctx context.Context) (api.DatabaseState, *int64) {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	if !s.dbAt.IsZero() && s.now().Sub(s.dbAt) < 2*time.Second {
		return s.dbState, s.dbSchema
	}
	s.dbAt = s.now()
	s.dbState, s.dbSchema = api.DatabaseStateUnavailable, nil
	if s.DB == nil {
		return s.dbState, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// goose records a down as a newer row with is_applied false, so the latest row of each version decides.
	var applied []int64
	err := s.DB.QueryRow(ctx, `SELECT coalesce(array_agg(version_id), '{}') FROM (SELECT DISTINCT ON (version_id)
		version_id, is_applied FROM public.goose_db_version ORDER BY version_id, id DESC) v WHERE is_applied`).Scan(&applied)
	switch {
	case err == nil && len(applied) > 0:
		s.dbState, s.dbSchema = api.DatabaseStateOk, ptr(slices.Max(applied))
		for _, v := range Migrations {
			if !slices.Contains(applied, v) {
				s.dbState = api.DatabaseStateMigrating
			}
		}
	case err == nil:
		s.dbState = api.DatabaseStateMigrating
	default:
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			s.dbState = api.DatabaseStateMigrating
		}
	}
	return s.dbState, s.dbSchema
}

func collectorStatus(st collect.SourceStatus, interval time.Duration) api.CollectorStatus {
	out := api.CollectorStatus{IntervalSeconds: int32(interval / time.Second)}
	switch st.State {
	case collect.StateOK, collect.StatePartial:
		out.State = api.CollectorStateOk
	case collect.StateUnavailable:
		out.State = api.CollectorStateUnavailable
	case "":
		out.State = api.CollectorStateStarting
	default:
		out.State = api.CollectorStateFailed
	}
	if st.LastError != "" && validCode(st.LastError) {
		out.ErrorCode = ptr(st.LastError)
	}
	if !st.LastOK.IsZero() {
		out.LastAt = ptr(st.LastOK.UTC())
	}
	return out
}

func validCode(c string) bool {
	if len(c) < 2 || len(c) > 32 || c[0] < 'A' || c[0] > 'Z' {
		return false
	}
	for i := 1; i < len(c); i++ {
		ch := c[i]
		if !(ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			return false
		}
	}
	return true
}

func codePtr(c string) *string {
	if c == "" || !validCode(c) {
		return nil
	}
	return &c
}

// dockerView is the Docker section as reported and whether it counts as partial for health; Docker that is not
// wanted or whose socket is absent reads disabled, and Docker installed but stopped reads unavailable; neither
// degrades health, whatever the last state was.
func dockerView(st collect.SourceStatus, live time.Duration, notWanted, absent, stopped bool) (api.CollectorStatus, bool) {
	if st.State == "" && notWanted || absent {
		return api.CollectorStatus{State: api.CollectorStateDisabled, IntervalSeconds: int32(live / time.Second)}, false
	}
	if stopped {
		return api.CollectorStatus{State: api.CollectorStateUnavailable, IntervalSeconds: int32(live / time.Second)}, false
	}
	return collectorStatus(st, live), st.State == collect.StatePartial
}

// StatusNow builds the status answer; the live stream sends the same object.
func (s *Server) StatusNow(ctx context.Context) api.StatusResponse {
	now := s.now()
	state, schema := s.database(ctx)
	out := api.StatusResponse{GeneratedAt: now.UTC(), Database: api.DatabaseStatus{State: state, SchemaVersion: schema,
		HistoryPaused: historyPaused(s.Runner)}}
	live := runner.DefaultLive
	partial, lossy, dockerStopped := false, false, false
	if s.Runner != nil {
		live = s.Runner.Options().Live
		machine, docker := s.Runner.Status.Get(collect.SourceMachine), s.Runner.Status.Get(collect.SourceDocker)
		out.Machine = collectorStatus(machine, live)
		var dockerPartial bool
		dockerStopped = s.Runner.DockerStopped()
		out.Docker, dockerPartial = dockerView(docker, live, !s.Runner.HasDocker(), s.Runner.DockerAbsent(), dockerStopped)
		partial = machine.State == collect.StatePartial || dockerPartial
		h := s.Runner.Status.Get(collect.SourceHistory)
		if h.State == collect.StatePaused || h.State == collect.StatePartial {
			out.Database.HistoryErrorCode = codePtr(h.LastError)
		}
		d := s.Runner.Drops()
		out.Database.DroppedRows.Rejected, out.Database.DroppedRows.Overflow = clamp(d.Rejected), clamp(d.Overflow)
		out.Database.DroppedRows.Expired = clamp(d.Expired)
		lossy = s.Runner.RecentLoss()
	} else {
		out.Machine = api.CollectorStatus{State: api.CollectorStateDisabled, IntervalSeconds: int32(live / time.Second)}
		out.Docker = out.Machine
	}
	if s.Upkeep != nil {
		err, lastOK, _ := s.Upkeep.State()
		out.Database.UpkeepErrorCode = codePtr(codelog.Of(err))
		if !lastOK.IsZero() {
			out.Database.UpkeepLastAt = ptr(lastOK.UTC())
		}
	}
	out.Guard = api.CollectorStatus{State: api.CollectorStateDisabled, IntervalSeconds: int32(transcripts.GuardEvery /
		time.Second)}
	if s.Guard != nil {
		g := s.Guard.Status()
		out.Guard.State = api.CollectorState(g.State)
		out.Guard.ErrorCode = codePtr(g.ErrorCode)
		if !g.LastAt.IsZero() {
			out.Guard.LastAt = ptr(g.LastAt.UTC())
		}
	}
	if s.guardAbsent(ctx) {
		out.Guard.State, out.Guard.ErrorCode = api.CollectorStateDisabled, nil
	}
	out.Scan = api.ScanStatus{State: api.ScanStateNeverRan, ByClient: []api.ClientScanCounts{}}
	if s.Scanner != nil {
		st := s.Scanner.Status()
		out.Scan = api.ScanStatus{State: api.ScanState(st.State), ErrorCode: codePtr(st.ErrorCode),
			WriteErrorCode: codePtr(st.DBErrorCode), WritePending: st.DBPending, KeptDays: st.KeptDays,
			DuplicateLinesDropped: st.Duplicates, ItemsDropped: st.ItemsDropped, Lines: st.Lines,
			SkippedLines: st.Skipped, ToolErrors: st.ToolErrorsToday, Transcripts: st.Transcripts,
			UnpricedModels: st.Unpriced, Unreadable: st.Unreadable, Unrebuilt: st.Unrebuilt,
			ByClient: scanByClient(st.ByClient)}
		if !st.LastRunAt.IsZero() {
			out.Scan.LastRunAt = ptr(st.LastRunAt.UTC())
		}
		if !st.LastChange.IsZero() {
			out.Scan.LastChangeAt = ptr(st.LastChange.UTC())
		}
		if !st.NextRun.IsZero() {
			out.Scan.NextRunInSeconds = ptr(int32(max(0, st.NextRun.Sub(now).Seconds())))
			out.Scan.IntervalSeconds = ptr(int32(math.Ceil(st.Wait.Seconds())))
		}
		for _, x := range s.Scanner.Sessions() {
			if x.State(now) == "live" {
				out.LiveSessions++
			}
		}
	}
	out.Health = api.HealthNominal
	if s.degraded(out, partial || lossy, dockerStopped) {
		out.Health = api.HealthDegraded
	}
	if out.Machine.State == api.CollectorStateFailed && out.Scan.State == api.ScanStateFailed {
		out.Health = api.HealthFailed
	}
	return out
}

// guardAbsent is true when this machine has no guard: no install and no guard log, now or in the stored figures.
func (s *Server) guardAbsent(ctx context.Context) bool {
	var in guard.Install
	if s.GuardInstall != nil {
		in = s.GuardInstall.Get()
	} else {
		in.Source = "none"
	}
	state := transcripts.GuardDisabled
	if s.Guard != nil {
		state = s.Guard.Status().State
	}
	return guardMissing(in, state, func() (bool, error) {
		if s.DB == nil {
			return true, errors.New("no database")
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return s.q().GuardFiguresStored(ctx)
	})
}

// guardMissing keeps the guard present when it is installed, when its log is being read or failed to read, and when a
// lookup of stored figures fails: only a clean "nothing anywhere" hides it. stored runs only for an unavailable log.
func guardMissing(in guard.Install, state string, stored func() (bool, error)) bool {
	if in.Found() {
		return false
	}
	switch state {
	case transcripts.GuardDisabled:
		return true
	case transcripts.GuardUnavailable:
		has, err := stored()
		return err == nil && !has
	}
	return false
}

// degraded is true when any part is not doing its whole job, including history it gives up and days it cannot store;
// partial also covers history that lost rows lately. Kept days do not count: deleted transcripts keep days by design.
func (s *Server) degraded(out api.StatusResponse, partial, dockerStopped bool) bool {
	db, scan := out.Database, out.Scan
	dockerFault := out.Docker.State != api.CollectorStateOk && out.Docker.State != api.CollectorStateDisabled &&
		!dockerStopped
	return db.State != api.DatabaseStateOk || db.HistoryPaused || db.HistoryErrorCode != nil ||
		db.UpkeepErrorCode != nil || partial || out.Machine.State != api.CollectorStateOk ||
		dockerFault || out.Guard.State == api.CollectorStateFailed || scan.State == api.ScanStateFailed ||
		scan.State == api.ScanStateLocked || scan.State == api.ScanStateNeverRan || scan.WriteErrorCode != nil ||
		scan.Unrebuilt > 0 || scan.ItemsDropped > 0
}

func clamp(v uint64) int64 { return int64(min(v, math.MaxInt64)) }

func (s *Server) GetStatus(ctx context.Context, _ api.GetStatusRequestObject) (api.GetStatusResponseObject, error) {
	return api.GetStatus200JSONResponse(s.StatusNow(ctx)), nil
}

func bandsOf(rows []store.TokenBand, kind store.TokenBandKind) []api.TokenBand {
	out := []api.TokenBand{}
	for _, b := range rows {
		if b.Kind == kind {
			out = append(out, api.TokenBand{MinTokens: b.MinTokens, MaxTokens: b.MaxTokens})
		}
	}
	return out
}

func (s *Server) GetMeta(ctx context.Context, _ api.GetMetaRequestObject) (api.GetMetaResponseObject, error) {
	out, err := s.meta(ctx)
	if unavailable(err) {
		logUnavailable("/api/meta", err)
		return api.GetMeta503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse{
			Code: api.ErrorResponseCodeUnavailable}}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetMeta200JSONResponse(out), nil
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

func (s *Server) meta(ctx context.Context) (api.MetaResponse, error) {
	if s.DB == nil {
		return api.MetaResponse{}, errNoDB
	}
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	q := s.q()
	out := api.MetaResponse{GeneratedAt: s.now().UTC()}
	stored, err := q.ListClients(ctx)
	if err != nil {
		return out, err
	}
	out.Clients = clientList(s.detectedClients(), stored)
	if out.Projects, err = q.ListProjects(ctx); err != nil {
		return out, err
	}
	if out.Models, err = q.ListModels(ctx); err != nil {
		return out, err
	}
	if out.AgentTypes, err = q.ListAgentTypes(ctx); err != nil {
		return out, err
	}
	if out.Tools, err = q.ListTools(ctx); err != nil {
		return out, err
	}
	if out.Stacks, err = listStacks(ctx, q, s.now().Add(-7*24*time.Hour)); err != nil {
		return out, err
	}
	out.Projects, out.Models, out.AgentTypes = nonNil(out.Projects), nonNil(out.Models), nonNil(out.AgentTypes)
	out.Tools, out.Stacks = nonNil(out.Tools), nonNil(out.Stacks)
	bands, err := q.ListTokenBands(ctx)
	if err != nil {
		return out, err
	}
	out.ContextBands = bandsOf(bands, store.TokenBandKindContext)
	out.ResultSizeBands = bandsOf(bands, store.TokenBandKindResultSize)
	if out.RereadTarget, err = q.RereadTarget(ctx); err != nil {
		return out, err
	}
	first, last, err := s.dayRange(ctx)
	if err != nil {
		return out, err
	}
	if first != nil {
		out.FirstDay, out.LastDay = ptr(date(*first)), ptr(date(*last))
	}
	out.Retention, err = retention(ctx, s.DB)
	if err != nil {
		return out, err
	}
	return out, nil
}

// stackParams starts each finer tier at the later of since and the newest bucket of the next coarser tier, if any.
func stackParams(since time.Time, c store.StackCutoffsRow) store.ListStacksParams {
	later := func(m pgtype.Timestamptz) pgtype.Timestamptz {
		if m.Valid && m.Time.After(since) {
			return m
		}
		return ts(since)
	}
	return store.ListStacksParams{HourSince: ts(since), MinuteSince: later(c.HourMax), RawSince: later(c.MinuteMax)}
}

// listStacks reads the cutoffs before the list, so a rollup in between only makes the list read more rows.
func listStacks(ctx context.Context, q *store.Queries, since time.Time) ([]string, error) {
	return listStacksWith(ctx, q, since, nil)
}

// listStacksWith runs between after the cutoffs are read, so a test can put a rollup there.
func listStacksWith(ctx context.Context, q *store.Queries, since time.Time, between func()) ([]string, error) {
	c, err := q.StackCutoffs(ctx)
	if err != nil {
		return nil, err
	}
	if between != nil {
		between()
	}
	return q.ListStacks(ctx, stackParams(since, c))
}

// dayRange is the first and last day with transcript rows; nil when there are none.
func (s *Server) dayRange(ctx context.Context) (*time.Time, *time.Time, error) {
	var first, last pgtype.Date
	err := s.DB.QueryRow(ctx, "SELECT min(day), max(day) FROM public.usage_daily").Scan(&first, &last)
	if err != nil || !first.Valid {
		return nil, nil, err
	}
	return &first.Time, &last.Time, nil
}

// retention reads the tiers itself because a tier kept forever has NULL months and days, which RetentionSettings
// cannot scan.
func retention(ctx context.Context, db store.DBTX) ([]api.RetentionTier, error) {
	rows, err := db.Query(ctx, `SELECT tier::text, (extract(year FROM keep) * 12 + extract(month FROM keep))::integer,
		extract(day FROM keep)::integer FROM retention_settings ORDER BY tier`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.RetentionTier{}
	for rows.Next() {
		var r api.RetentionTier
		var tier string
		if err := rows.Scan(&tier, &r.Months, &r.Days); err != nil {
			return nil, err
		}
		r.Tier = api.RetentionTierTier(tier)
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanByClient(in []transcripts.ClientCounts) []api.ClientScanCounts {
	out := make([]api.ClientScanCounts, 0, len(in))
	for _, c := range in {
		out = append(out, api.ClientScanCounts{Client: api.Client(c.Client), SkippedLines: c.Skipped,
			Unreadable: c.Unreadable})
	}
	return out
}
