package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/api"
	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/history"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/store"
	"benchwatch/server/internal/testdb"
	"benchwatch/server/internal/transcripts"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testdb.DSN(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// fixtureTables are emptied before and after the test; the test database holds nothing else.
var fixtureTables = []string{"machine_samples", "machine_core_samples", "machine_interface_samples",
	"machine_volume_samples", "machine_app_samples", "docker_vm_samples", "docker_container_samples", "docker_events",
	"machine_rollup_1m", "machine_rollup_1h", "machine_core_rollup_1m", "machine_core_rollup_1h",
	"machine_interface_rollup_1m", "machine_interface_rollup_1h", "docker_vm_rollup_1m", "docker_vm_rollup_1h",
	"docker_container_rollup_1m", "docker_container_rollup_1h", "guard_daily", "guard_mismatch_daily",
	"guard_latency_daily", "guard_window_checks", "guard_call_hourly", "guard_call_latency_daily", "guard_rule_daily",
	"guard_log_state"}

func emptyFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, table := range fixtureTables {
		if _, err := pool.Exec(ctx, "DELETE FROM "+pgx.Identifier{table}.Sanitize()); err != nil {
			t.Fatalf("empty %s: %v", table, err)
		}
	}
	if _, err := pool.Exec(ctx, "SELECT clear_transcript_days(ARRAY(SELECT DISTINCT day FROM usage_daily"+
		" UNION SELECT day FROM tool_result_daily UNION SELECT day FROM compaction_events))"); err != nil {
		t.Fatalf("empty transcripts: %v", err)
	}
}

func noonDaysAgo(n int) int64 {
	y, m, d := time.Now().AddDate(0, 0, -n).Date()
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local).Unix()
}

func shards() []*scan.Shard {
	agent := "Explore"
	d1, d0 := noonDaysAgo(1), noonDaysAgo(0)
	call := func(id string, ts int64, model string, in, read, out int64) extract.Event {
		return extract.Event{Kind: extract.Call, Ts: ts, ID: id, Text: model, N: [5]int64{in, 10, 0, read, out}}
	}
	result := func(id string, ts int64, tool string, tokens int64, class string) extract.Event {
		return extract.Event{Kind: extract.Result, Ts: ts, ID: id, Text: tool, N: [5]int64{tokens}, Error: class}
	}
	return []*scan.Shard{
		{Client: clients.ClaudeCode, Project: "benchwatch", Session: "abcd1234", Scope: "main", Lines: 10, Events: []extract.Event{
			call("c1", d1, "claude-opus-5-5", 100, 1000, 50),
			result("1111111111111111", d1+1, "Read: whole", 400, ""),
			call("c2", d1+2, "claude-opus-5-5", 10, 2000, 20),
			result("2222222222222222", d1+3, "Bash: git", 200, "non-zero exit"),
			call("c3", d1+4, "claude-opus-5-5", 10, 3000, 20),
			{Kind: extract.Boundary, Ts: d1 + 5, ID: "3333333333333333", Text: "auto", N: [5]int64{150000}},
			call("c4", d0, "claude-opus-5-5[1m]", 5, 500, 5),
		}},
		{Client: clients.ClaudeCode, Project: "benchwatch", Session: "abcd1234", Scope: "sub", AgentType: &agent, Events: []extract.Event{
			call("s1", d0+1, "claude-haiku-5-5", 50, 0, 10),
			call("s2", d0+2, "claude-haiku-5-5", 50, 100, 10),
		}},
	}
}

func writeFixtures(t *testing.T, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	ctx := context.Background()
	cb, rb, err := transcripts.Bands(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := aggregate.BuildRows(func(visit func(*scan.Shard) error) error {
		for _, s := range shards() {
			if err := visit(s); err != nil {
				return err
			}
		}
		return nil
	}, aggregate.MaxRunItems, cb, rb)
	if err != nil {
		t.Fatal(err)
	}
	if err := transcripts.WriteDays(ctx, pool, rows, rows.Days()); err != nil {
		t.Fatalf("write transcripts: %v", err)
	}
	var b collect.Batch
	for i := 30; i >= 1; i-- {
		at := now.Add(-time.Duration(i) * 10 * time.Second)
		b.Machine = append(b.Machine, machineSample(at))
		b.Docker = append(b.Docker, dockerSample(at))
	}
	b.Events = []collect.DockerEvent{{At: now.Add(-time.Minute), Name: "nginx", Kind: "restart"}}
	if err := (history.Sink{DB: pool}).Write(ctx, b); err != nil {
		t.Fatalf("write samples: %v", err)
	}
	up := &history.Upkeep{DB: pool, Now: func() time.Time { return now }}
	if err := up.Tick(ctx); err != nil {
		t.Fatalf("rollups: %v", err)
	}
	q := store.New(pool)
	today := dayOf(now)
	if err := q.AddGuardCallHour(ctx, store.AddGuardCallHourParams{Day: today, Hour: int16(now.Hour()),
		Tool: store.GuardToolBash, Decision: store.GuardDecisionAllow, Calls: 40}); err != nil {
		t.Fatalf("guard calls: %v", err)
	}
	for _, l := range []store.AddGuardCallLatencyParams{
		{Day: today, Tool: store.GuardToolBash, LoMs: 0, HiMs: ptr(50.0), Calls: 30},
		{Day: today, Tool: store.GuardToolBash, LoMs: 50, HiMs: nil, Calls: 10}} {
		if err := q.AddGuardCallLatency(ctx, l); err != nil {
			t.Fatalf("guard latency: %v", err)
		}
	}
}

func decode[T any](t *testing.T, h http.Handler, target string) T {
	t.Helper()
	rec := get(h, AllowedHosts[0], target)
	var out T
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d body %s", target, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return out
}

// A period with no rows makes every aggregate NULL; each page must still answer 200 (CM-00018).
func TestEveryEndpointAnswersOnAnEmptyDatabase(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	now := time.Now()
	_, h := newServer(t, pool, memoryRunner(now, 5, 2*time.Second))
	for _, rng := range []string{"today", "7d", "30d", "all"} {
		ov := decode[api.OverviewResponse](t, h, "/api/overview?range="+rng)
		cx := decode[api.ContextResponse](t, h, "/api/context?range="+rng)
		for name, c := range map[string]api.CompactionSummary{"overview": ov.Compactions, "context": cx.Compactions} {
			if c.Count != 0 || c.AvgFreedTokens != nil || len(c.ByDay) != 0 {
				t.Errorf("%s %s compactions: %+v", name, rng, c)
			}
		}
		for _, p := range []string{"tokens", "sessions", "subagents", "fills", "guard"} {
			decode[json.RawMessage](t, h, "/api/"+p+"?range="+rng)
		}
	}
	for _, p := range []string{"meta", "machine?range=1h", "docker?range=1h", "status"} {
		decode[json.RawMessage](t, h, "/api/"+p)
	}
}

// Every endpoint answers 200 on fixture data; NewHandler turns any answer outside the spec into a 500.
func TestEveryEndpointAnswersWithinTheSpec(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	now := time.Now()
	writeFixtures(t, pool, now)
	s, h := newServer(t, pool, memoryRunner(now, 5, 2*time.Second))
	s.Runner.Status.OK(collect.SourceMachine)
	s.Runner.Status.OK(collect.SourceDocker)

	meta := decode[api.MetaResponse](t, h, "/api/meta")
	if meta.FirstDay == nil || len(meta.Projects) != 1 || len(meta.Retention) == 0 || meta.RereadTarget <= 0 {
		t.Errorf("meta: first day %v projects %v retention %d", meta.FirstDay, meta.Projects, len(meta.Retention))
	}
	ov := decode[api.OverviewResponse](t, h, "/api/overview?range=7d")
	if ov.Calls.Value != 6 || ov.SessionMap.Sessions != 1 || ov.SessionMap.WithSubagents != 1 || len(ov.Daily) != 2 ||
		ov.PeakDay == nil || ov.Calls.Previous != nil {
		t.Errorf("overview: calls %v sessions %+v daily %d", ov.Calls, ov.SessionMap, len(ov.Daily))
	}
	for _, d := range ov.Daily {
		if d.RereadShare <= 0 || d.RereadShare >= 1 {
			t.Errorf("overview day %s: re-read share %v", d.Day, d.RereadShare)
		}
	}
	if ov.Compactions.Count != 1 || len(ov.Compactions.Recent) != 1 || ov.Compactions.Recent[0].At.IsZero() ||
		len(ov.Compactions.ByDay) != 1 || ov.Compactions.ByDay[0].Count != 1 {
		t.Errorf("overview compactions: %+v", ov.Compactions)
	}
	from := now.AddDate(0, 0, -1).Format(time.DateOnly)
	custom := decode[api.TokensResponse](t, h, "/api/tokens?from="+from+"&to="+now.Format(time.DateOnly))
	if custom.Period.Days != 2 || len(custom.ByKind) != 4 || len(custom.Daily) != 2 || custom.PeakCostDay == nil {
		t.Errorf("tokens: days %d kinds %d daily %d", custom.Period.Days, len(custom.ByKind), len(custom.Daily))
	}
	cx := decode[api.ContextResponse](t, h, "/api/context?range=30d&project=benchwatch")
	if cx.Peak == nil || cx.Compactions.Count != 1 || len(cx.ByProject) != 1 {
		t.Errorf("context: peak %v compactions %d", cx.Peak, cx.Compactions.Count)
	}
	if len(cx.Daily) != len(ov.Daily) {
		t.Fatalf("context daily %d days, overview %d", len(cx.Daily), len(ov.Daily))
	}
	for i, d := range cx.Daily {
		if ov.Daily[i].Calls > 0 && d.AvgContextTokens <= 0 {
			t.Errorf("context daily %v: calls %d but average context %v", d.Day, ov.Daily[i].Calls, d.AvgContextTokens)
		}
	}
	for i, d := range cx.Daily {
		if d.RereadShare <= 0 || d.RereadShare >= 1 || d.RereadShare != ov.Daily[i].RereadShare {
			t.Errorf("context day %s: re-read share %v, overview %v", d.Day, d.RereadShare, ov.Daily[i].RereadShare)
		}
	}
	ss := decode[api.SessionsResponse](t, h, "/api/sessions?range=all")
	if len(ss.Sessions) != 1 || ss.Totals.Sessions != 1 || len(ss.Projects) != 1 || ss.Projects[0].Share != 1 {
		t.Errorf("sessions: %d sessions, totals %+v", len(ss.Sessions), ss.Totals)
	}
	one := decode[api.SessionDetailResponse](t, h, "/api/sessions/abcd1234?project=benchwatch")
	if one.Summary.Calls == 0 || len(one.Subagents) != 1 || len(one.Compactions) != 1 {
		t.Errorf("session: %+v", one.Summary)
	}
	if rec := get(h, AllowedHosts[0], "/api/sessions/ffff0000?project=benchwatch"); rec.Code != 404 ||
		code(t, rec) != "not_found" {
		t.Errorf("unknown session: status %d", rec.Code)
	}
	sub := decode[api.SubagentsResponse](t, h, "/api/subagents?range=today")
	if len(sub.Types) != 1 || sub.Runs.Value != 1 || sub.ScopeShare.Sub <= 0 {
		t.Errorf("subagents: types %d runs %v", len(sub.Types), sub.Runs)
	}
	if len(sub.ScopeDaily) != 1 || sub.ScopeDaily[0].SubShare <= 0 || sub.ScopeDaily[0].SubShare >= 1 ||
		sub.ScopeDaily[0].SubCostUsd != sub.CostUsd {
		t.Errorf("subagents scope daily: %+v, sub cost %v", sub.ScopeDaily, sub.CostUsd)
	}
	for _, g := range []string{"tool", "project", "sizeBand"} {
		f := decode[api.FillsResponse](t, h, "/api/fills?group="+g)
		if len(f.Groups) == 0 || len(f.Tools) != 2 || f.Largest == nil || f.ContextShare <= 0 {
			t.Errorf("fills %s: groups %d tools %d", g, len(f.Groups), len(f.Tools))
		}
	}
	gd := decode[api.GuardResponse](t, h, "/api/guard?range=7d")
	if gd.Calls.Value != 40 || gd.Decisions == nil || gd.Decisions.Allow != 40 || len(gd.ByTool) != 1 ||
		gd.ByTool[0].P50Ms == nil || len(gd.ToolErrors) != 1 || gd.Hours == nil || gd.LastBlock != nil {
		t.Errorf("guard: calls %v decisions %+v by tool %+v", gd.Calls, gd.Decisions, gd.ByTool)
	}
	for _, rng := range []string{"5m", "15m", "1h", "24h", "7d"} {
		m := decode[api.MachineResponse](t, h, "/api/machine?range="+rng)
		if len(m.Series) == 0 || len(m.CoreSeries) != 2 || m.CoverageFrom == nil || m.HistoryPaused {
			t.Errorf("machine %s: %d points, %d cores", rng, len(m.Series), len(m.CoreSeries))
		}
		d := decode[api.DockerResponse](t, h, "/api/docker?range="+rng)
		if len(d.VmSeries) == 0 || len(d.ContainerSeries) != 2 || len(d.Events) != 1 {
			t.Errorf("docker %s: %d vm points, %d containers, %d events", rng, len(d.VmSeries),
				len(d.ContainerSeries), len(d.Events))
		}
	}
	st := decode[api.StatusResponse](t, h, "/api/status")
	if st.Database.State != api.DatabaseStateOk || st.Database.SchemaVersion == nil {
		t.Errorf("status database %s", st.Database.State)
	}
}

// A day stored before repeats were counted is not measured: its results stay out of the measured count, so the share
// is taken over measured days only, and a period with no measured day sends no repeat count.
func TestFillsCountRepeatsOfMeasuredDaysOnly(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	writeFixtures(t, pool, time.Now())
	_, h := newServer(t, pool, memoryRunner(time.Now(), 5, 2*time.Second))
	tools := func(path string) map[string]api.ToolStats {
		out := map[string]api.ToolStats{}
		for _, tool := range decode[api.FillsResponse](t, h, path).Tools {
			out[tool.Tool] = tool
		}
		return out
	}
	before := tools("/api/fills?range=30d")
	for _, name := range []string{"Read: whole", "Bash: git"} {
		if tool, ok := before[name]; !ok || len(before) != 2 || tool.RepeatResults == nil || tool.Results == 0 ||
			tool.RepeatMeasuredResults != tool.Results {
			t.Fatalf("rows the scanner wrote: %s %+v of %d tools, want every result measured", name, tool, len(before))
		}
	}
	// The stray 4 repeats of the unmeasured row must not be counted either.
	if _, err := pool.Exec(context.Background(), "INSERT INTO tool_result_daily VALUES"+
		" ($1::date, 'benchwatch', 'main', 'Read: whole', 5, 50, 50, 0, 10, 4)",
		time.Now().AddDate(0, 0, -3).Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	got := tools("/api/fills?range=30d")
	read, was := got["Read: whole"], before["Read: whole"]
	if read.Results != was.Results+5 || read.RepeatMeasuredResults != was.Results || read.RepeatResults == nil ||
		*read.RepeatResults != *was.RepeatResults {
		t.Fatalf("with an unmeasured day for Read: %+v, want %d results, %d of them measured, %d repeats", read,
			was.Results+5, was.Results, *was.RepeatResults)
	}
	if bash := got["Bash: git"]; bash.RepeatMeasuredResults != bash.Results || bash.RepeatResults == nil {
		t.Fatalf("Bash has no unmeasured day: %+v", bash)
	}
	old := time.Now().AddDate(0, 0, -3).Format(time.DateOnly)
	only := tools("/api/fills?from=" + old + "&to=" + old)["Read: whole"]
	if only.Results != 5 || only.RepeatMeasuredResults != 0 || only.RepeatResults != nil {
		t.Fatalf("a period of unmeasured days only: %+v, want 5 results, none measured and no repeat count", only)
	}
}
