package transcripts

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/codelog"
	"benchwatch/server/internal/pricing"
	"benchwatch/server/internal/scan"
	clientsource "benchwatch/server/internal/source"
	"benchwatch/server/internal/store"
)

const (
	LiveFor = 5 * time.Minute
	IdleFor = 60 * time.Minute
	// After a rebuild the next run waits this many times the CPU it used, so constant transcript writes cost a tenth
	// of a core at most however busy the Mac is. A scheduled full replay (start, hourly check) is exempt.
	throttle = 10
	// restAfter quiet runs in a row (30 s at Every) let Live drop the decoded shards it keeps, so an idle scanner
	// holds little heap; the first change after that reads them from the cache folder again.
	restAfter = 10
	// Every is the default pause between runs; an incremental rebuild costs far less than throttle allows at this rate.
	Every = 3 * time.Second
)

// Scan states, as the API names them.
const (
	StateOK       = "ok"
	StateRunning  = "running"
	StateLocked   = "locked"
	StateFailed   = "failed"
	StateNeverRan = "never_ran"
)

type Options struct {
	Projects, Cache string
	// Readers are the clients' readers; nil reads Claude Code's transcripts under Projects only.
	Readers []clientsource.Reader
	Every   time.Duration
	Now     func() time.Time
	// CPU is the process's CPU time so far; the default reads getrusage.
	CPU func() time.Duration
	// WriteTimeout bounds each database step of a run, so a lock wait cannot hold the scanner (2 minutes by default).
	WriteTimeout time.Duration
	// Release hands unused heap back to the system; the default is debug.FreeOSMemory.
	Release func()
}

type Status struct {
	State                 string
	ErrorCode             string
	LastRunAt, LastChange time.Time
	NextRun               time.Time
	Wait                  time.Duration
	Transcripts, Lines    int64
	Skipped, Duplicates   int64
	ItemsDropped          int64
	Unreadable, Unrebuilt int64
	// ByClient splits Skipped and Unreadable over the clients found on this machine.
	ByClient        []ClientCounts
	Unpriced        int32
	ToolErrorsToday int64
	// DBErrorCode is why the last rebuild is not fully in PostgreSQL, or EREPRICE[_code] when it is stored but repricing
	// kept days failed; DBPending means the next run retries the store or the reprice.
	DBErrorCode string
	DBPending   bool
	// KeptDays counts stored days left as they are because a transcript behind them is gone.
	KeptDays               int64
	FirstDay, LastDay      time.Time
	UniqueCalls            int64
	LastRebuildMs          int64
	ProjectsSeen, Sessions int
}

type Session struct {
	Client                  clients.ID
	Project, Session, Model string
	StartedAt, LastAt       time.Time
	ContextTokens, Tokens   int64
	SubagentRuns            int32
}

// State is live under LiveFor since the last call, idle under IdleFor, ended after.
func (s Session) State(now time.Time) string {
	switch age := now.Sub(s.LastAt); {
	case age < LiveFor:
		return "live"
	case age < IdleFor:
		return "idle"
	}
	return "ended"
}

type Activity struct {
	Kind                      string
	At                        time.Time
	Project, Session          string
	AgentType, ErrorCode      string
	Count                     *int64
	TokensBefore, TokensAfter *int64
}

type sessionKey struct {
	client           clients.ID
	project, session string
}

type runKey struct{ project, session, agent string }

// Scanner owns its shard cache, which must not be the Node job's.
type Scanner struct {
	opt Options
	db  DB
	// OnActivity and OnChange are called without the lock held, from the scanner goroutine.
	OnActivity func(Activity)
	OnChange   func()

	live   *aggregate.Live
	cache  *scan.Cache
	unread *unreadable
	// kind says how the last rebuild replayed the shards.
	kind aggregate.BuildKind
	// start gives Live its monotonic readings.
	start time.Time
	// settled says the heap the last rebuild grew has gone back to the system, at the latest on the run after it.
	settled bool
	// quiet counts the runs in a row that rebuilt nothing.
	quiet int

	mu         sync.RWMutex
	status     Status
	sessions   []Session
	digests    map[string]dayRecord
	loaded     bool
	pending    bool
	baseline   bool
	known      map[sessionKey]bool
	liveState  map[sessionKey]string
	runs       map[runKey]int64
	boundaries map[string]bool
	errorsDay  time.Time
	unpriced   map[time.Time][]aggregate.UnpricedPair
}

func New(opt Options, db DB) *Scanner {
	if opt.Every <= 0 {
		opt.Every = Every
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.CPU == nil {
		opt.CPU = processCPU
	}
	if opt.Release == nil {
		opt.Release = debug.FreeOSMemory
	}
	if opt.WriteTimeout <= 0 {
		opt.WriteTimeout = 2 * time.Minute
	}
	readers := opt.Readers
	if readers == nil {
		readers = []clientsource.Reader{scan.NewClaudeReader(opt.Projects)}
	}
	unread := &unreadable{}
	counted := make([]clientsource.Reader, 0, len(readers))
	for _, r := range readers {
		counted = append(counted, counting{Reader: r, counts: unread})
	}
	cache, err := scan.NewCacheFor(counted, opt.Cache)
	if err != nil {
		panic(err)
	}
	return &Scanner{opt: opt, db: db, live: aggregate.NewLive(), cache: cache, unread: unread,
		start: time.Now(), status: Status{State: StateNeverRan},
		known:     map[sessionKey]bool{},
		liveState: map[sessionKey]string{}, runs: map[runKey]int64{}, boundaries: map[string]bool{}}
}

// Clients are the clients whose reader detects records on this machine.
func (s *Scanner) Clients() []clients.ID { return s.cache.Clients() }

func (s *Scanner) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// Sessions returns the sessions active within IdleFor, latest first.
// Unpriced lists the (client, model) pairs without a price on the days from..to of the last rebuild.
func (s *Scanner) Unpriced(from, to time.Time, client *string) []aggregate.UnpricedPair {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Kept days add what their reprice left unpriced to the last rebuild's pairs.
	all := map[time.Time][]aggregate.UnpricedPair{}
	maps.Copy(all, s.unpriced)
	for day, rec := range s.digests {
		if t, err := time.Parse(time.DateOnly, day); err == nil && len(rec.Unpriced) > 0 {
			all[t] = slices.Concat(all[t], rec.Unpriced)
		}
	}
	return aggregate.UnpricedIn(all, from, to, client)
}

func (s *Scanner) Sessions() []Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.opt.Now()
	var out []Session
	for _, x := range s.sessions {
		if x.State(now) != "ended" {
			out = append(out, x)
		}
	}
	return out
}

func (s *Scanner) Run(ctx context.Context) {
	for {
		wait := s.Tick(ctx)
		s.mu.Lock()
		s.status.NextRun, s.status.Wait = s.opt.Now().Add(wait), wait
		s.mu.Unlock()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (s *Scanner) emit(items []Activity) {
	if s.OnActivity == nil {
		return
	}
	for _, a := range items {
		s.OnActivity(a)
	}
}

func (s *Scanner) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
}

func (s *Scanner) Tick(ctx context.Context) time.Duration {
	cpu := s.opt.CPU()
	prev := s.Status().State
	s.mu.Lock()
	s.status.State = StateRunning
	s.mu.Unlock()
	rebuilt, err := s.scan(ctx)
	now := s.opt.Now()
	var acts []Activity
	s.mu.Lock()
	s.status.LastRunAt = now
	switch {
	case errors.Is(err, errLocked):
		s.status.State, s.status.ErrorCode = StateLocked, "ELOCKED"
	case err != nil:
		s.status.State, s.status.ErrorCode = StateFailed, codeOf(err)
	default:
		s.status.State, s.status.ErrorCode = StateOK, ""
	}
	if st := s.status.State; st != prev && prev != StateRunning {
		kind := map[string]string{StateOK: "scan_ok", StateFailed: "scan_failed", StateLocked: "scan_locked"}[st]
		if kind != "" && (prev != StateNeverRan || st != StateOK) {
			acts = append(acts, Activity{Kind: kind, At: now, ErrorCode: s.status.ErrorCode})
		}
	}
	acts = append(acts, s.idleTransitions(now)...)
	runCode, writeCode := s.status.ErrorCode, s.status.DBErrorCode
	s.mu.Unlock()
	codelog.Note("scan", runCode)
	codelog.Note("scan write", writeCode)
	s.emit(acts)
	s.changed()
	switch {
	case rebuilt && s.kind != aggregate.Incremental, !rebuilt && !s.settled:
		s.opt.Release()
		s.settled = true
	case rebuilt:
		s.settled = false
	}
	s.quiet++
	if rebuilt {
		s.quiet = 0
	}
	if s.quiet == restAfter {
		s.live.Rest()
		s.opt.Release()
	}
	if rebuilt && s.kind != aggregate.Scheduled {
		return max(s.opt.Every, throttle*(s.opt.CPU()-cpu))
	}
	return s.opt.Every
}

var errLocked = errors.New("ELOCKED")

func processCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func (s *Scanner) scan(ctx context.Context) (rebuilt bool, err error) {
	if err := os.MkdirAll(s.opt.Cache, 0o700); err != nil {
		return false, err
	}
	lock := scan.NewLock(filepath.Join(s.opt.Cache, "lock"), scan.BeatMs)
	taken, err := lock.Take(nil)
	if err != nil {
		return false, err
	}
	if !taken {
		return false, errLocked
	}
	defer lock.Release()
	beat := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return lock.Beat()
	}
	s.unread.reset()
	st, err := s.cache.Update(beat)
	if err != nil {
		return false, err
	}
	codelog.Note("scan unread", codeOf(st.FileErr))
	s.mu.RLock()
	idle := !s.pending && s.baseline && s.live.Current(st)
	s.mu.RUnlock()
	if idle {
		return false, nil
	}
	started := s.opt.Now()
	contextBands, resultBands := aggregate.DefaultContextBands, aggregate.DefaultResultBands
	var bandsErr error
	if s.db != nil {
		bctx, cancel := context.WithTimeout(ctx, s.opt.WriteTimeout)
		contextBands, resultBands, bandsErr = Bands(bctx, s.db)
		cancel()
	}
	rows, kind, err := s.live.Build(st, contextBands, resultBands, time.Since(s.start))
	s.kind = kind
	if kind != aggregate.Incremental {
		codelog.Note("scan drift", driftCode(s.live.Drift))
	}
	if err != nil {
		return true, err
	}
	if err := st.Save(); err != nil {
		return true, err
	}
	now := s.opt.Now()
	digests := rows.Digests()
	s.observe(rows, st, now, started)
	writeErr := bandsErr
	kept := -1
	if writeErr == nil {
		kept, writeErr = s.write(ctx, rows, digests, len(st.Failed) > 0 || rows.ItemsDropped > 0)
	}
	s.mu.Lock()
	s.status.DBErrorCode = codeOf(writeErr)
	s.status.Unpriced = unpricedCount(rows.Unpriced, s.unpriced, s.digests)
	if kept >= 0 {
		s.status.KeptDays = int64(kept)
	}
	// A partial run has nothing to retry until its transcripts change.
	s.pending = writeErr != nil && !errors.Is(writeErr, errPartial)
	s.status.DBPending = s.pending
	s.mu.Unlock()
	if kept >= 0 {
		codelog.Note("scan kept days", keptCode(kept))
	}
	return true, nil
}

// unpricedCount adds to the rebuild's count the pairs that kept days' reprices left unpriced and it does not list.
func unpricedCount(rebuilt int, byDay map[time.Time][]aggregate.UnpricedPair, days map[string]dayRecord) int32 {
	seen := map[aggregate.UnpricedPair]bool{}
	for _, pairs := range byDay {
		for _, p := range pairs {
			seen[p] = true
		}
	}
	n := rebuilt
	for _, rec := range days {
		for _, p := range rec.Unpriced {
			if !seen[p] {
				seen[p] = true
				n++
			}
		}
	}
	return int32(min(n, 1<<30))
}

// driftCode names days where an incremental build differed from the full one that followed; it should never show.
func driftCode(n int) string {
	if n == 0 {
		return ""
	}
	return "EDRIFT_" + strconv.Itoa(n)
}

// keptCode names the kept-day count, so the log shows each change; days frozen by a lost transcript show nowhere else.
func keptCode(n int) string {
	if n == 0 {
		return ""
	}
	return "KEPT_" + strconv.Itoa(n)
}

// write returns how many stored days it kept: a partial run writes nothing, and a day is never replaced while a
// transcript that went into it is gone, so neither can shrink a stored day.
func (s *Scanner) write(ctx context.Context, rows *aggregate.Rows, digests map[time.Time]string,
	partial bool) (int, error) {
	if s.db == nil {
		return -1, errNoDB
	}
	if partial {
		return -1, errPartial
	}
	ctx, cancel := context.WithTimeout(ctx, s.opt.WriteTimeout)
	defer cancel()
	written, err := s.loadDigests(ctx)
	if err != nil {
		return -1, err
	}
	present := make(map[string]bool, len(rows.Keys))
	for _, k := range rows.Keys {
		present[k] = true
	}
	var kept []time.Time
	for day := range written {
		if t, err := time.Parse(time.DateOnly, day); err == nil && digests[t] == "" {
			kept = append(kept, t)
		}
	}
	prices := priceVersions()
	var days, unrecorded []time.Time
	for day, d := range digests {
		rec, ok := written[day.Format(time.DateOnly)]
		switch {
		case ok && rec.Digest == d:
			// The stored day equals this build, so today's contributors describe it: a later loss is then seen by
			// key, for days stored before keys were recorded and for a transcript renamed with its content unchanged.
			written[day.Format(time.DateOnly)] = dayRecord{Digest: d, Keys: sortedKeys(rows.Contributors[day]),
				Prices: prices}
		case ok && rec.Keys != nil && !allPresent(rec.Keys, present):
			kept = append(kept, day)
		default:
			if !ok || rec.Keys == nil {
				unrecorded = append(unrecorded, day)
			}
			days = append(days, day)
		}
	}
	if len(unrecorded) > 0 {
		var shrinking map[time.Time]bool
		if shrinking, err = shrinkingDays(ctx, s.db, rows, unrecorded); err != nil {
			return -1, err
		}
		days = slices.DeleteFunc(days, func(d time.Time) bool { return shrinking[d] })
		for d := range shrinking {
			kept = append(kept, d)
		}
	}
	slices.SortFunc(days, func(a, b time.Time) int { return a.Compare(b) })
	if err := WriteDays(ctx, s.db, rows, days); err != nil {
		return -1, err
	}
	for _, day := range days {
		written[day.Format(time.DateOnly)] = dayRecord{Digest: digests[day], Keys: sortedKeys(rows.Contributors[day]),
			Prices: prices}
	}
	// A failed reprice leaves its days' records as they were, so the next run retries it; the rebuilt days still save.
	repriceErr := s.repriceKept(ctx, written, kept, prices)
	if err := s.saveDigests(written); err != nil {
		return -1, err
	}
	if repriceErr != nil {
		return len(kept), repriceError{repriceErr}
	}
	return len(kept), nil
}

// repriceError is a failed reprice of kept days after the rebuilt days were stored.
type repriceError struct{ err error }

func (e repriceError) Error() string { return "reprice kept days: " + e.err.Error() }
func (e repriceError) Unwrap() error { return e.err }

func priceVersions() map[clients.ID]int {
	out := make(map[clients.ID]int, len(clients.All()))
	for _, c := range clients.All() {
		out[c] = pricing.Version(c)
	}
	return out
}

// staleClients lists the clients whose stored costs on a day were priced at an older version than today's; a
// downgrade is left alone.
func staleClients(rec dayRecord, prices map[clients.ID]int) []clients.ID {
	var out []clients.ID
	for _, c := range clients.All() {
		if recordedVersion(rec, c) < prices[c] {
			out = append(out, c)
		}
	}
	return out
}

func recordedVersion(rec dayRecord, c clients.ID) int {
	if was, ok := rec.Prices[c]; ok {
		return was
	}
	return 1
}

// repriceKept brings the costs of kept days to today's prices, since no rebuild replaces them; it changes cost columns
// only and is safe to repeat (decideReprice), so a lost day record cannot lower a stored cost.
func (s *Scanner) repriceKept(ctx context.Context, written map[string]dayRecord, kept []time.Time,
	prices map[clients.ID]int) error {
	stale := map[time.Time][]clients.ID{}
	for _, day := range kept {
		if c := staleClients(written[day.Format(time.DateOnly)], prices); len(c) > 0 {
			stale[day] = c
		}
	}
	if len(stale) == 0 {
		return nil
	}
	unpriced, err := repriceDays(ctx, s.db, stale)
	if err != nil {
		return err
	}
	for day, repriced := range stale {
		key := day.Format(time.DateOnly)
		rec := written[key]
		// No build may match the old digest now: once the day is rebuilt it must be written again.
		rec.Digest = ""
		raised := make(map[clients.ID]int, len(prices))
		for c, v := range prices {
			raised[c] = max(v, recordedVersion(rec, c))
		}
		rec.Prices = raised
		rec.Unpriced = replacePairs(rec.Unpriced, repriced, unpriced[day])
		written[key] = rec
	}
	return nil
}

// replacePairs drops the pairs of the repriced clients from old and adds what the reprice left unpriced.
func replacePairs(old []aggregate.UnpricedPair, repriced []clients.ID,
	fresh []aggregate.UnpricedPair) []aggregate.UnpricedPair {
	out := slices.DeleteFunc(slices.Clone(old), func(p aggregate.UnpricedPair) bool {
		return slices.Contains(repriced, clients.ID(p.Client))
	})
	out = append(out, fresh...)
	slices.SortFunc(out, func(x, y aggregate.UnpricedPair) int {
		return cmp.Or(strings.Compare(x.Client, y.Client), strings.Compare(x.Model, y.Model))
	})
	return slices.Compact(out)
}

// repriceGroup is one (day, project, scope, model, client): its usage_daily row and session_daily rows.
type repriceGroup struct {
	day                   time.Time
	project, scope, model string
	client                clients.ID
}

type repriceRow struct {
	group   repriceGroup
	session string
	isUse   bool
	sums    pricing.Stored
	// was is the stored cost and cost the new one; a session row holds its total in Output.
	was, cost pricing.Cost
}

func total(c pricing.Cost) float64 { return c.Input + c.CacheWrite + c.CacheRead + c.Output }

// costEpsilon is the cost columns' numeric(18, 6) step: a recomputed cost closer than it is the stored one.
const costEpsilon = 1e-6

func costDiffers(a, b pricing.Cost) bool {
	return math.Abs(a.Input-b.Input) >= costEpsilon || math.Abs(a.CacheWrite-b.CacheWrite) >= costEpsilon ||
		math.Abs(a.CacheRead-b.CacheRead) >= costEpsilon || math.Abs(a.Output-b.Output) >= costEpsilon
}

// decideReprice decides once per group for both tables; a group not priced exactly keeps its stored costs, as a kept
// day cannot be rebuilt, and is listed unpriced only when a row of it holds no cost. It returns the rows to update.
func decideReprice(rows []repriceRow) ([]repriceRow, map[time.Time][]aggregate.UnpricedPair) {
	byGroup := map[repriceGroup][]int{}
	var order []repriceGroup
	for i := range rows {
		g := rows[i].group
		if _, ok := byGroup[g]; !ok {
			order = append(order, g)
		}
		byGroup[g] = append(byGroup[g], i)
	}
	var changed []repriceRow
	unpriced := map[time.Time][]aggregate.UnpricedPair{}
	for _, g := range order {
		priced, fallback, free := true, false, false
		for _, i := range byGroup[g] {
			r := &rows[i]
			cost, exact := pricing.Reprice(g.client, g.model, r.sums)
			priced = priced && exact && (cost.Basis == pricing.Listed || cost.Basis == pricing.Fallback)
			fallback = fallback || cost.Basis == pricing.Fallback
			free = free || total(r.was) == 0
			if !r.isUse {
				cost = pricing.Cost{Output: total(cost), Basis: cost.Basis}
			}
			r.cost = cost
		}
		if priced && fallback || !priced && free {
			unpriced[g.day] = append(unpriced[g.day], aggregate.UnpricedPair{Client: string(g.client), Model: g.model})
		}
		if !priced {
			continue
		}
		for _, i := range byGroup[g] {
			if costDiffers(rows[i].cost, rows[i].was) {
				changed = append(changed, rows[i])
			}
		}
	}
	for d, p := range unpriced {
		unpriced[d] = replacePairs(nil, nil, p)
	}
	return changed, unpriced
}

// repriceDays updates usage_daily and session_daily rows of the stale clients in one transaction and returns, per
// day, the (client, model) pairs whose rows are left unpriced.
func repriceDays(ctx context.Context, db DB, stale map[time.Time][]clients.ID) (
	map[time.Time][]aggregate.UnpricedPair, error) {
	days := make([]time.Time, 0, len(stale))
	for d := range stale {
		days = append(days, d)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var rows []repriceRow
	read := func(isUse bool, q string) error {
		res, err := tx.Query(ctx, q, days)
		if err != nil {
			return err
		}
		defer res.Close()
		for res.Next() {
			var r repriceRow
			var client string
			g := &r.group
			dest := []any{&g.day, &g.project, &g.scope, &g.model, &client, &r.sums.Calls, &r.sums.Input,
				&r.sums.CacheWrite, &r.sums.CacheRead, &r.sums.Output}
			if isUse {
				dest = append(dest, &r.was.Input, &r.was.CacheWrite, &r.was.CacheRead, &r.was.Output)
			} else {
				dest = append(dest, &r.was.Output, &r.session)
			}
			if err := res.Scan(dest...); err != nil {
				return err
			}
			g.day = time.Date(g.day.Year(), g.day.Month(), g.day.Day(), 0, 0, 0, 0, time.UTC)
			g.client, r.isUse = clients.ID(client), isUse
			if !slices.Contains(stale[g.day], g.client) || pricing.RecordsCost(g.client) {
				continue
			}
			rows = append(rows, r)
		}
		return res.Err()
	}
	const cols = "day, project, scope::text, model, client::text, calls, input_tokens, cache_write_tokens, " +
		"cache_read_tokens, output_tokens"
	if err := read(true, "SELECT "+cols+", cost_input_usd::float8, cost_cache_write_usd::float8, "+
		"cost_cache_read_usd::float8, cost_output_usd::float8 FROM public.usage_daily "+
		"WHERE day = ANY($1::date[])"); err != nil {
		return nil, err
	}
	if err := read(false, "SELECT "+cols+", cost_usd::float8, session FROM public.session_daily "+
		"WHERE day = ANY($1::date[])"); err != nil {
		return nil, err
	}
	changed, unpriced := decideReprice(rows)
	if err := updateCosts(ctx, tx, changed); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return unpriced, nil
}

type costColumns struct {
	day                                  []time.Time
	project, scope, model, client, sess  []string
	input, cacheWrite, cacheRead, output []float64
}

func (c *costColumns) add(r repriceRow) {
	g := r.group
	c.day, c.project, c.scope = append(c.day, g.day), append(c.project, g.project), append(c.scope, g.scope)
	c.model, c.client, c.sess = append(c.model, g.model), append(c.client, string(g.client)), append(c.sess, r.session)
	c.input, c.cacheWrite = append(c.input, r.cost.Input), append(c.cacheWrite, r.cost.CacheWrite)
	c.cacheRead, c.output = append(c.cacheRead, r.cost.CacheRead), append(c.output, r.cost.Output)
}

// updateCosts writes the new costs with one statement per table.
func updateCosts(ctx context.Context, tx pgx.Tx, rows []repriceRow) error {
	var use, sess costColumns
	for _, r := range rows {
		if r.isUse {
			use.add(r)
		} else {
			sess.add(r)
		}
	}
	const match = " t.day = v.day AND t.project = v.project AND t.scope = v.scope::token_scope AND t.model = v.model" +
		" AND t.client = v.client::client_id"
	if len(use.day) > 0 {
		if _, err := tx.Exec(ctx, "UPDATE public.usage_daily t SET cost_input_usd = v.ci, cost_cache_write_usd = v.cw,"+
			" cost_cache_read_usd = v.cr, cost_output_usd = v.co FROM unnest($1::date[], $2::text[], $3::text[],"+
			" $4::text[], $5::text[], $6::float8[], $7::float8[], $8::float8[], $9::float8[])"+
			" AS v(day, project, scope, model, client, ci, cw, cr, co) WHERE"+match,
			use.day, use.project, use.scope, use.model, use.client, use.input, use.cacheWrite, use.cacheRead,
			use.output); err != nil {
			return err
		}
	}
	if len(sess.day) > 0 {
		if _, err := tx.Exec(ctx, "UPDATE public.session_daily t SET cost_usd = v.cost FROM unnest($1::date[],"+
			" $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::float8[])"+
			" AS v(day, project, scope, model, client, session, cost) WHERE t.session = v.session AND"+match,
			sess.day, sess.project, sess.scope, sess.model, sess.client, sess.sess, sess.output); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(contributors []string) []string {
	keys := append([]string{}, contributors...)
	slices.Sort(keys)
	return keys
}

func allPresent(keys []string, present map[string]bool) bool {
	for _, k := range keys {
		if !present[k] {
			return false
		}
	}
	return true
}

// shrinkingDays finds, among days whose contributing transcripts were never recorded, those this build would store
// with fewer calls than PostgreSQL holds: a transcript behind them is gone, and the stored figures stay.
func shrinkingDays(ctx context.Context, db DB, rows *aggregate.Rows, days []time.Time) (map[time.Time]bool, error) {
	calls := map[time.Time]int64{}
	for _, u := range rows.Usage {
		calls[u.Day] += u.Calls
	}
	res, err := db.Query(ctx, "SELECT day, sum(calls)::bigint FROM public.usage_daily WHERE day = ANY($1::date[])"+
		" GROUP BY day", days)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	out := map[time.Time]bool{}
	for res.Next() {
		var day time.Time
		var stored int64
		if err := res.Scan(&day, &stored); err != nil {
			return nil, err
		}
		day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		if stored > calls[day] {
			out[day] = true
		}
	}
	return out, res.Err()
}

var (
	errNoDB    = errors.New("ENOSINK")
	errPartial = errors.New("EPARTIAL")
)

type dayRecord struct {
	Digest string `json:"digest"`
	// Keys are the shards that had an event on the day when it was written; nil when not recorded.
	Keys []string `json:"keys"`
	// Prices is the price version per client of the stored costs; a client missing from it is at version 1.
	Prices map[clients.ID]int `json:"prices,omitempty"`
	// Unpriced lists the pairs a reprice of the kept day could not price; a rebuild of the day drops them.
	Unpriced []aggregate.UnpricedPair `json:"unpriced,omitempty"`
}

type digestFile struct {
	Version int                  `json:"version"`
	Days    map[string]dayRecord `json:"days"`
}

type digestFileV1 struct {
	Version int               `json:"version"`
	Days    map[string]string `json:"days"`
}

func (s *Scanner) digestPath() string { return filepath.Join(s.opt.Cache, "db-days.json") }

// loadDigests trusts the file only while the database holds transcript rows, so a recreated database is refilled; a
// failed check is an error, never an empty database.
func (s *Scanner) loadDigests(ctx context.Context) (map[string]dayRecord, error) {
	s.mu.RLock()
	if s.loaded {
		out := maps.Clone(s.digests)
		s.mu.RUnlock()
		return out, nil
	}
	s.mu.RUnlock()
	out := map[string]dayRecord{}
	var any bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM public.usage_daily)").Scan(&any); err != nil {
		return nil, err
	}
	if any {
		if data, err := scan.ReadBytes(s.digestPath(), 8<<20); err == nil {
			out = decodeDigests(data)
		}
		// A stored day missing from the file (lost or older file) gets an empty record, so it is seen when kept.
		days, err := storedDays(ctx, s.db)
		if err != nil {
			return nil, err
		}
		for _, d := range days {
			if _, ok := out[d]; !ok {
				out[d] = dayRecord{}
			}
		}
	}
	s.mu.Lock()
	s.digests, s.loaded = maps.Clone(out), true
	s.mu.Unlock()
	return out, nil
}

func storedDays(ctx context.Context, db DB) ([]string, error) {
	res, err := db.Query(ctx, "SELECT DISTINCT day FROM public.usage_daily")
	if err != nil {
		return nil, err
	}
	defer res.Close()
	var out []string
	for res.Next() {
		var day time.Time
		if err := res.Scan(&day); err != nil {
			return nil, err
		}
		out = append(out, day.Format(time.DateOnly))
	}
	return out, res.Err()
}

func decodeDigests(data []byte) map[string]dayRecord {
	var f digestFile
	if json.Unmarshal(data, &f) == nil && f.Version == 2 && f.Days != nil {
		return f.Days
	}
	var v1 digestFileV1
	out := map[string]dayRecord{}
	if json.Unmarshal(data, &v1) == nil && v1.Version == 1 {
		for day, d := range v1.Days {
			out[day] = dayRecord{Digest: d}
		}
	}
	return out
}

func (s *Scanner) saveDigests(days map[string]dayRecord) error {
	s.mu.Lock()
	s.digests = maps.Clone(days)
	s.mu.Unlock()
	data, err := json.Marshal(digestFile{Version: 2, Days: days})
	if err != nil {
		return err
	}
	return scan.WriteAtomic(s.digestPath(), data, 0o600)
}

// observe updates the scan figures, the session list and the activity feed from a fresh rebuild.
func (s *Scanner) observe(rows *aggregate.Rows, st *scan.Store, now, started time.Time) {
	found := byClient(s.cache.Clients(), rows.SkippedByClient, s.unread)
	byKey := map[sessionKey]*Session{}
	latestMain := map[sessionKey]time.Time{}
	for _, r := range rows.Sessions {
		k := sessionKey{clients.ID(r.Client), r.Project, r.Session}
		x := byKey[k]
		if x == nil {
			x = &Session{Client: clients.ID(r.Client), Project: r.Project, Session: r.Session, Model: r.Model,
				StartedAt: r.FirstAt.Time, LastAt: r.LastAt.Time}
			byKey[k] = x
		}
		x.Tokens += r.InputTokens + r.CacheWriteTokens + r.CacheReadTokens + r.OutputTokens
		x.StartedAt = minTime(x.StartedAt, r.FirstAt.Time)
		x.LastAt = maxTime(x.LastAt, r.LastAt.Time)
		if r.Scope == store.TokenScopeMain && !r.LastAt.Time.Before(latestMain[k]) {
			latestMain[k] = r.LastAt.Time
			x.Model, x.ContextTokens = r.Model, r.LastContextTokens
		}
	}
	runs := map[runKey]int64{}
	for _, r := range rows.SubagentRuns {
		runs[runKey{r.Project, r.Session, r.AgentType}] += r.Runs
		if x := byKey[sessionKey{clients.ClaudeCode, r.Project, r.Session}]; x != nil {
			x.SubagentRuns += int32(min(r.Runs, 1<<30))
		}
	}
	today := dayOfTime(now)
	var todayErrors int64
	for _, e := range rows.Errors {
		if e.Day.Equal(today) {
			todayErrors += e.Errors
		}
	}
	sessions := make([]Session, 0, len(byKey))
	for _, x := range byKey {
		if now.Sub(x.LastAt) < IdleFor {
			sessions = append(sessions, *x)
		}
	}
	slices.SortFunc(sessions, func(a, b Session) int { return b.LastAt.Compare(a.LastAt) })
	days := rows.Days()

	s.mu.Lock()
	var acts []Activity
	if s.baseline {
		recent := func(t time.Time) bool { return now.Sub(t) < IdleFor }
		for k, x := range byKey {
			if !s.known[k] && recent(x.StartedAt) {
				acts = append(acts, Activity{Kind: "session_start", At: x.StartedAt, Project: k.project,
					Session: k.session})
			}
		}
		for k, n := range runs {
			if d := n - s.runs[k]; d > 0 {
				acts = append(acts, Activity{Kind: "subagent_start", At: now, Project: k.project, Session: k.session,
					AgentType: k.agent, Count: ptr(d)})
			}
		}
		for _, b := range rows.Boundaries {
			if !s.boundaries[b.Boundary] && recent(b.At.Time) {
				acts = append(acts, Activity{Kind: "compaction", At: b.At.Time, Project: b.Project, Session: b.Session,
					TokensBefore: ptr(b.TokensBefore), TokensAfter: b.TokensAfter})
			}
		}
		if s.errorsDay.Equal(today) {
			if d := todayErrors - s.status.ToolErrorsToday; d > 0 {
				acts = append(acts, Activity{Kind: "tool_errors", At: now, Count: ptr(d)})
			}
		}
	}
	s.known = map[sessionKey]bool{}
	for k := range byKey {
		s.known[k] = true
	}
	s.runs = runs
	s.boundaries = map[string]bool{}
	for _, b := range rows.Boundaries {
		s.boundaries[b.Boundary] = true
	}
	s.baseline = true
	s.errorsDay = today
	s.unpriced = rows.UnpricedByDay()
	s.sessions = sessions
	for _, x := range sessions {
		k := sessionKey{x.Client, x.Project, x.Session}
		if _, ok := s.liveState[k]; !ok {
			s.liveState[k] = x.State(now)
		}
	}
	st2 := &s.status
	st2.ToolErrorsToday = todayErrors
	st2.LastChange = now
	st2.Transcripts, st2.Lines, st2.Skipped = int64(rows.Transcripts), rows.Lines, rows.Skipped
	st2.Duplicates, st2.ItemsDropped = int64(rows.DuplicateLines), int64(rows.ItemsDropped)
	st2.Unreadable, st2.Unrebuilt = int64(st.Unreadable), int64(len(st.Failed))
	st2.ByClient = found
	st2.Unpriced, st2.UniqueCalls = int32(min(rows.Unpriced, 1<<30)), int64(rows.UniqueCalls)
	st2.LastRebuildMs = now.Sub(started).Milliseconds()
	st2.Sessions = len(byKey)
	if len(days) > 0 {
		st2.FirstDay, st2.LastDay = days[0], days[len(days)-1]
	}
	s.mu.Unlock()
	slices.SortFunc(acts, func(a, b Activity) int { return a.At.Compare(b.At) })
	s.emit(acts)
}

// idleTransitions reports sessions that went from live to idle since the last check; the caller holds the lock.
func (s *Scanner) idleTransitions(now time.Time) []Activity {
	var acts []Activity
	seen := map[sessionKey]bool{}
	for _, x := range s.sessions {
		k := sessionKey{x.Client, x.Project, x.Session}
		seen[k] = true
		state := x.State(now)
		if s.liveState[k] == "live" && state == "idle" {
			acts = append(acts, Activity{Kind: "session_idle", At: x.LastAt.Add(LiveFor), Project: x.Project,
				Session: x.Session})
		}
		s.liveState[k] = state
	}
	for k := range s.liveState {
		if !seen[k] {
			delete(s.liveState, k)
		}
	}
	return acts
}

func ptr[T any](v T) *T { return &v }

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func dayOfTime(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// codeOf names an error by a fixed code, never its text, which can hold a path.
func codeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errLocked):
		return "ELOCKED"
	case errors.Is(err, scan.ErrLockLost):
		return "ELOCKLOST"
	case errors.Is(err, scan.ErrSeenFull):
		return "ESEENFULL"
	case errors.Is(err, aggregate.ErrTooManyRows):
		return "ETOOMANYROWS"
	case errors.Is(err, errNoDB):
		return "ENOSINK"
	case errors.Is(err, errPartial):
		return "EPARTIAL"
	}
	if re := (repriceError{}); errors.As(err, &re) {
		// Within the API's 32-character error code.
		if code := "EREPRICE_" + codeOf(re.err); len(code) <= 32 {
			return code
		}
		return "EREPRICE"
	}
	return codelog.Of(err)
}
