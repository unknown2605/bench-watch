package transcripts

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/pricing"
)

func storeUsage(t *testing.T, conn *pgx.Conn, day, project, model, client string, calls, input, cr, out int64,
	cost float64) {
	t.Helper()
	q := `INSERT INTO usage_daily (day, project, scope, model, calls, input_tokens, cache_write_tokens,
		cache_read_tokens, output_tokens, context_tokens, cost_input_usd, cost_cache_write_usd, cost_cache_read_usd,
		cost_output_usd, client)
		VALUES ($1::date, $2, 'main', $3, $4, $5, 0, $6, $7, $5::bigint + $6::bigint, 0, 0, 0, $8, $9)`
	if _, err := conn.Exec(context.Background(), q, day, project, model, calls, input, cr, out, cost, client); err != nil {
		t.Fatal(err)
	}
	q = `INSERT INTO session_daily (day, project, session, scope, model, calls, input_tokens, cache_write_tokens,
		cache_read_tokens, output_tokens, context_tokens, peak_context_tokens, last_context_tokens, cost_usd, first_at,
		last_at, client) VALUES ($1::date, $2, '0a1b2c3d', 'main', $3, $4, $5, 0, $6, $7, $5::bigint + $6::bigint, 0, 0, $8,
		$1::date::timestamptz, $1::date::timestamptz, $9)`
	if _, err := conn.Exec(context.Background(), q, day, project, model, calls, input, cr, out, cost, client); err != nil {
		t.Fatal(err)
	}
}

func within(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

type storedCost struct {
	usage, session float64
	tokens         int64
}

func costOf(t *testing.T, conn *pgx.Conn, day, project, client string) storedCost {
	t.Helper()
	var c storedCost
	q := `SELECT (cost_input_usd + cost_cache_write_usd + cost_cache_read_usd + cost_output_usd)::float8,
		input_tokens + cache_read_tokens + output_tokens FROM usage_daily WHERE day = $1::date AND project = $2
		AND client = $3::client_id`
	if err := conn.QueryRow(context.Background(), q, day, project, client).Scan(&c.usage, &c.tokens); err != nil {
		t.Fatal(err)
	}
	q = `SELECT cost_usd::float8 FROM session_daily WHERE day = $1::date AND project = $2 AND client = $3::client_id`
	if err := conn.QueryRow(context.Background(), q, day, project, client).Scan(&c.session); err != nil {
		t.Fatal(err)
	}
	return c
}

// Days stored before Codex was priced, whose transcripts are gone, are kept; they get today's prices once, without a
// token changed, and a row its sums cannot price is listed as unpriced instead of reading as free.
func TestKeptDaysAreRepricedOnceAtTodaysPrices(t *testing.T) {
	conn := testConn(t)
	emptyTranscripts(t, conn)
	t.Cleanup(func() { emptyTranscripts(t, conn) })
	ctx := context.Background()
	projects, file := oneTranscript(t)
	opt := Options{Projects: projects, Cache: filepath.Join(t.TempDir(), "cache")}
	New(opt, conn).Tick(ctx)

	seeded := time.Unix(noon(3), 0).Format(time.DateOnly)
	legacy := time.Unix(noon(4), 0).Format(time.DateOnly)
	for _, day := range []string{seeded, legacy} {
		storeUsage(t, conn, day, "exact", "gpt-6.1-sol", "codex", 2, 100000, 50000, 20000, 0)
		storeUsage(t, conn, day, "tiered", "gpt-6.1-sol", "codex", 3, 400000, 0, 1000, 0)
		storeUsage(t, conn, day, "claude", "claude-opus-5-5", "claude-code", 1, 10, 0, 0, 1.25)
		storeUsage(t, conn, day, "recorded", "gpt-6.1-sol", "opencode", 1, 10, 0, 0, 0.5)
	}
	record := filepath.Join(opt.Cache, "db-days.json")
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var f digestFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f.Days[legacy] = dayRecord{Digest: "old", Keys: []string{"gone"}}
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, data, 0o600); err != nil {
		t.Fatal(err)
	}

	appendLines(t, file, callLine("m2", time.Now().Unix()))
	s := New(opt, conn)
	s.Tick(ctx)
	if st := s.Status(); st.DBErrorCode != "" || st.KeptDays != 2 {
		t.Fatalf("code %q, kept days %d, want 2", st.DBErrorCode, st.KeptDays)
	}
	want := pricing.CostOf(clients.Codex, "gpt-6.1-sol", pricing.Usage{Input: 100000, CacheRead: 50000, Output: 20000})
	exact := want.Input + want.CacheRead + want.Output
	from, to := time.Unix(noon(5), 0), time.Unix(noon(0), 0)
	codex := string(clients.Codex)
	for _, day := range []string{seeded, legacy} {
		c := costOf(t, conn, day, "exact", "codex")
		if !within(c.usage, exact) || !within(c.session, exact) || c.tokens != 170000 {
			t.Errorf("%s exact row: %+v, want cost %v and tokens unchanged", day, c, exact)
		}
		if c := costOf(t, conn, day, "tiered", "codex"); c.usage != 0 || c.session != 0 || c.tokens != 401000 {
			t.Errorf("%s tiered row: %+v", day, c)
		}
		if c := costOf(t, conn, day, "claude", "claude-code"); c.usage != 1.25 || c.session != 1.25 {
			t.Errorf("%s claude row changed: %+v", day, c)
		}
		if c := costOf(t, conn, day, "recorded", "opencode"); c.usage != 0.5 || c.session != 0.5 {
			t.Errorf("%s recorded row changed: %+v", day, c)
		}
	}
	pair := aggregate.UnpricedPair{Client: codex, Model: "gpt-6.1-sol"}
	if got := s.Unpriced(from, to, &codex); len(got) != 1 || got[0] != pair {
		t.Errorf("unpriced %+v, want the tiered rows' pair", got)
	}

	// Repriced once: a later scanner leaves the stored cost alone and still lists the pair.
	if _, err := conn.Exec(ctx, "UPDATE usage_daily SET cost_output_usd = 9 WHERE project = 'exact'"); err != nil {
		t.Fatal(err)
	}
	appendLines(t, file, callLine("m3", time.Now().Unix()))
	again := New(opt, conn)
	again.Tick(ctx)
	if c := costOf(t, conn, seeded, "exact", "codex"); !within(c.usage, 9+want.Input+want.CacheRead) {
		t.Errorf("repriced a second time: %+v", c)
	}
	if got := again.Unpriced(from, to, &codex); len(got) != 1 || got[0] != pair {
		t.Errorf("after a restart, unpriced %+v", got)
	}
}

func writeRecords(t *testing.T, cache string, set map[string]dayRecord) {
	t.Helper()
	path := filepath.Join(cache, "db-days.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f digestFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	maps.Copy(f.Days, set)
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// allCosts maps every usage_daily and session_daily row to its stored cost.
func allCosts(t *testing.T, conn *pgx.Conn) map[string]float64 {
	t.Helper()
	res, err := conn.Query(context.Background(), `SELECT 'u ' || day || project || scope || model || client,
		(cost_input_usd + cost_cache_write_usd + cost_cache_read_usd + cost_output_usd)::float8 FROM usage_daily
		UNION ALL SELECT 's ' || day || project || session || scope || model || client, cost_usd::float8
		FROM session_daily`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(res, func(r pgx.CollectableRow) (struct {
		k string
		v float64
	}, error) {
		var x struct {
			k string
			v float64
		}
		return x, r.Scan(&x.k, &x.v)
	})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]float64{}
	for _, x := range out {
		m[x.k] = x.v
	}
	return m
}

// A kept row its sums cannot price exactly keeps a non-zero cost; an older version's pairs are replaced, a newer
// version is left alone, and a lost day record makes a repeat that lowers nothing.
func TestKeptDayRepriceNeverLowersAStoredCost(t *testing.T) {
	conn := testConn(t)
	emptyTranscripts(t, conn)
	t.Cleanup(func() { emptyTranscripts(t, conn) })
	ctx := context.Background()
	projects, file := oneTranscript(t)
	opt := Options{Projects: projects, Cache: filepath.Join(t.TempDir(), "cache")}
	New(opt, conn).Tick(ctx)

	older := time.Unix(noon(3), 0).Format(time.DateOnly)
	newer := time.Unix(noon(4), 0).Format(time.DateOnly)
	storeUsage(t, conn, older, "tiered", soloModel, "codex", 3, 400000, 0, 1000, 0.63)
	storeUsage(t, conn, older, "zero", soloModel, "codex", 3, 400000, 0, 1000, 0)
	storeUsage(t, conn, older, "exact", "gpt-6.1-terra", "codex", 2, 100000, 50000, 20000, 0)
	storeUsage(t, conn, newer, "exact", "gpt-6.1-terra", "codex", 2, 100000, 50000, 20000, 0)
	// Group "split": the usage row sums two calls over the tier, each session row holds one call within it.
	storeUsage(t, conn, older, "split", soloModel, "codex", 2, 400000, 0, 20, 0)
	if _, err := conn.Exec(ctx, `UPDATE session_daily SET calls = 1, input_tokens = 200000, output_tokens = 10,
		context_tokens = 200000 WHERE project = 'split'`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO session_daily (day, project, session, scope, model, calls, input_tokens,
		cache_write_tokens, cache_read_tokens, output_tokens, context_tokens, peak_context_tokens, last_context_tokens,
		cost_usd, first_at, last_at, client) SELECT day, project, '1a1b2c3d', scope, model, calls, input_tokens,
		cache_write_tokens, cache_read_tokens, output_tokens, context_tokens, peak_context_tokens, last_context_tokens,
		cost_usd, first_at, last_at, client FROM session_daily WHERE project = 'split'`); err != nil {
		t.Fatal(err)
	}
	codex := string(clients.Codex)
	stalePair := aggregate.UnpricedPair{Client: codex, Model: "gpt-6.1-terra"}
	newerPrices := map[clients.ID]int{}
	for _, c := range clients.All() {
		newerPrices[c] = pricing.Version(c) + 1
	}
	writeRecords(t, opt.Cache, map[string]dayRecord{
		older: {Digest: "old", Keys: []string{"gone"}, Unpriced: []aggregate.UnpricedPair{stalePair}},
		newer: {Digest: "old", Keys: []string{"gone"}, Prices: newerPrices},
	})

	appendLines(t, file, callLine("m2", time.Now().Unix()))
	s := New(opt, conn)
	s.Tick(ctx)
	if st := s.Status(); st.DBErrorCode != "" || st.KeptDays != 2 {
		t.Fatalf("code %q, kept days %d, want 2", st.DBErrorCode, st.KeptDays)
	}
	if c := costOf(t, conn, older, "tiered", codex); !within(c.usage, 0.63) || !within(c.session, 0.63) {
		t.Errorf("non-zero cost of a row not priced exactly changed: %+v", c)
	}
	if c := costOf(t, conn, older, "exact", codex); c.usage == 0 || !within(c.usage, c.session) {
		t.Errorf("exact row not repriced: %+v", c)
	}
	if c := costOf(t, conn, newer, "exact", codex); c.usage != 0 || c.session != 0 {
		t.Errorf("a day priced at a newer version was repriced: %+v", c)
	}
	var split []float64
	if err := conn.QueryRow(ctx, `SELECT array_agg(cost_usd::float8) FROM session_daily WHERE project = 'split'`).
		Scan(&split); err != nil {
		t.Fatal(err)
	}
	if c := costOf2(t, conn, older, "split"); c != 0 || len(split) != 2 || split[0] != 0 || split[1] != 0 {
		t.Errorf("tables disagree on group split: usage %v sessions %v", c, split)
	}
	want := aggregate.UnpricedPair{Client: codex, Model: soloModel}
	from, to := time.Unix(noon(5), 0), time.Unix(noon(0), 0)
	if got := s.Unpriced(from, to, &codex); len(got) != 1 || got[0] != want {
		t.Errorf("unpriced %+v, want only %+v", got, want)
	}
	if st := s.Status(); st.Unpriced != 1 {
		t.Errorf("status counts %d unpriced pairs, want the kept day's 1", st.Unpriced)
	}

	before := allCosts(t, conn)
	if err := os.Remove(filepath.Join(opt.Cache, "db-days.json")); err != nil {
		t.Fatal(err)
	}
	appendLines(t, file, callLine("m3", time.Now().Unix()))
	lost := New(opt, conn)
	lost.Tick(ctx)
	if st := lost.Status(); st.DBErrorCode != "" {
		t.Fatalf("code %q", st.DBErrorCode)
	}
	after := allCosts(t, conn)
	for k, v := range before {
		if after[k] < v-1e-9 {
			t.Errorf("%s lowered from %v to %v after the day records were lost", k, v, after[k])
		}
	}
}

// A kept Claude day with cache writes cannot be priced from its sums, so a stale reprice and a repeat after the day
// record is lost both leave its stored cost as it was.
func TestKeptClaudeDayWithCacheWritesKeepsItsCost(t *testing.T) {
	conn := testConn(t)
	emptyTranscripts(t, conn)
	t.Cleanup(func() { emptyTranscripts(t, conn) })
	ctx := context.Background()
	projects, file := oneTranscript(t)
	opt := Options{Projects: projects, Cache: filepath.Join(t.TempDir(), "cache")}
	New(opt, conn).Tick(ctx)

	day := time.Unix(noon(3), 0).Format(time.DateOnly)
	claude := string(clients.ClaudeCode)
	storeUsage(t, conn, day, "cachew", "claude-opus-5", claude, 2, 1000, 5000, 200, 0.42)
	for _, table := range []string{"usage_daily", "session_daily"} {
		q := `UPDATE ` + table + ` SET cache_write_tokens = 3000 WHERE project = 'cachew'`
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// Version 0 is older than Claude's today's, so the day is stale and goes through the reprice.
	writeRecords(t, opt.Cache, map[string]dayRecord{
		day: {Digest: "old", Keys: []string{"gone"}, Prices: map[clients.ID]int{clients.ClaudeCode: 0}},
	})

	appendLines(t, file, callLine("m2", time.Now().Unix()))
	s := New(opt, conn)
	s.Tick(ctx)
	if st := s.Status(); st.DBErrorCode != "" || st.KeptDays != 1 {
		t.Fatalf("code %q, kept days %d, want 1", st.DBErrorCode, st.KeptDays)
	}
	if c := costOf(t, conn, day, "cachew", claude); !within(c.usage, 0.42) || !within(c.session, 0.42) {
		t.Errorf("stored cost of a Claude day with cache writes changed: %+v", c)
	}

	before := allCosts(t, conn)
	if err := os.Remove(filepath.Join(opt.Cache, "db-days.json")); err != nil {
		t.Fatal(err)
	}
	appendLines(t, file, callLine("m3", time.Now().Unix()))
	lost := New(opt, conn)
	lost.Tick(ctx)
	if st := lost.Status(); st.DBErrorCode != "" {
		t.Fatalf("code %q", st.DBErrorCode)
	}
	after := allCosts(t, conn)
	for k, v := range before {
		if after[k] < v-1e-9 {
			t.Errorf("%s lowered from %v to %v after the day records were lost", k, v, after[k])
		}
	}
	if c := costOf(t, conn, day, "cachew", claude); !within(c.usage, 0.42) || !within(c.session, 0.42) {
		t.Errorf("stored cost changed after the day records were lost: %+v", c)
	}
}

func costOf2(t *testing.T, conn *pgx.Conn, day, project string) float64 {
	t.Helper()
	var v float64
	if err := conn.QueryRow(context.Background(), `SELECT (cost_input_usd + cost_cache_write_usd + cost_cache_read_usd
		+ cost_output_usd)::float8 FROM usage_daily WHERE day = $1::date AND project = $2`, day, project).
		Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// failingReprice fails the reprice's read of session_daily while fail is set.
type failingReprice struct {
	*pgx.Conn
	fail bool
}

func (f *failingReprice) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := f.Conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return repriceFailTx{Tx: tx, f: f}, nil
}

type repriceFailTx struct {
	pgx.Tx
	f *failingReprice
}

func (t repriceFailTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if t.f.fail && strings.Contains(sql, "cost_usd::float8, session FROM") {
		return nil, errRefused
	}
	return t.Tx.Query(ctx, sql, args...)
}

// A failing reprice still saves the rebuilt days, shows in the status, and is retried on the next run.
func TestFailedRepriceSavesTheRebuildAndRetries(t *testing.T) {
	conn := testConn(t)
	emptyTranscripts(t, conn)
	t.Cleanup(func() { emptyTranscripts(t, conn) })
	ctx := context.Background()
	projects, file := oneTranscript(t)
	opt := Options{Projects: projects, Cache: filepath.Join(t.TempDir(), "cache")}
	New(opt, conn).Tick(ctx)
	day := time.Unix(noon(3), 0).Format(time.DateOnly)
	storeUsage(t, conn, day, "exact", "gpt-6.1-terra", "codex", 2, 100000, 50000, 20000, 0)
	writeRecords(t, opt.Cache, map[string]dayRecord{day: {Digest: "old", Keys: []string{"gone"}}})

	db := &failingReprice{Conn: conn, fail: true}
	appendLines(t, file, callLine("m2", time.Now().Unix()))
	s := New(opt, db)
	s.Tick(ctx)
	st := s.Status()
	if st.DBErrorCode != "EREPRICE_EPG_57P03" || !st.DBPending || st.KeptDays != 1 {
		t.Fatalf("code %q pending %v kept %d; want EREPRICE_EPG_57P03, pending, 1", st.DBErrorCode, st.DBPending,
			st.KeptDays)
	}
	data, err := os.ReadFile(filepath.Join(opt.Cache, "db-days.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f digestFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	today := time.Now().Format(time.DateOnly)
	if f.Days[today].Digest == "" || f.Days[day].Digest != "old" || f.Days[day].Prices != nil {
		t.Fatalf("today %+v kept %+v: rebuilt day not saved or kept day marked repriced", f.Days[today], f.Days[day])
	}
	if c := costOf(t, conn, day, "exact", "codex"); c.usage != 0 {
		t.Fatalf("failed reprice wrote %+v", c)
	}

	db.fail = false
	s.Tick(ctx)
	if st := s.Status(); st.DBErrorCode != "" || st.DBPending {
		t.Fatalf("retry: code %q pending %v", st.DBErrorCode, st.DBPending)
	}
	if c := costOf(t, conn, day, "exact", "codex"); c.usage == 0 || !within(c.usage, c.session) {
		t.Errorf("retry did not reprice: %+v", c)
	}
}
