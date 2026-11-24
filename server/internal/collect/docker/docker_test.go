package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/clocktest"
	"benchwatch/server/internal/collect/docker/dockertest"
)

func TestReadOnlyGuardRules(t *testing.T) {
	ok := []string{"GET /_ping", "HEAD /_ping", "GET /v1.56/containers/json", "GET /v1.56/containers/abc123/stats",
		"GET /v1.56/events", "GET /v1.56/system/df", "GET /v1.56/info"}
	refused := []string{"POST /v1.56/containers/abc/start", "POST /v1.56/containers/abc/exec",
		"DELETE /v1.56/containers/abc",
		"POST /v1.56/images/create", "POST /v1.56/containers/prune", "GET /v1.56/containers/abc/json",
		"GET /v1.56/containers/abc/logs", "GET /v1.56/containers/abc/archive", "GET /v1.56/containers/../info/stats",
		"PUT /v1.56/containers/abc/archive", "GET /v1.56/images/json",
		"POST /v1.56/containers/json", "DELETE /v1.56/events", "POST /_ping", "PUT /v1.56/info"}
	check := func(line string) error {
		m, p, _ := strings.Cut(line, " ")
		req, _ := http.NewRequest(m, "http://docker"+p, nil)
		return readOnly(req)
	}
	for _, l := range ok {
		if err := check(l); err != nil {
			t.Errorf("%s refused", l)
		}
	}
	for _, l := range refused {
		if err := check(l); !errors.Is(err, ErrGuard) {
			t.Errorf("%s allowed", l)
		}
	}
}

// TestGuardStopsWritesBeforeTheSocket calls write endpoints through the real client; none may reach the daemon.
func TestGuardStopsWritesBeforeTheSocket(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	cli, _, err := newClient("unix://" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx := context.Background()
	_, e1 := cli.ContainerStart(ctx, dockertest.ID(1), client.ContainerStartOptions{})
	_, e2 := cli.ContainerRemove(ctx, dockertest.ID(1), client.ContainerRemoveOptions{Force: true})
	_, e3 := cli.ContainerInspect(ctx, dockertest.ID(1), client.ContainerInspectOptions{})
	_, e4 := cli.ContainerPrune(ctx, client.ContainerPruneOptions{})
	for i, e := range []error{e1, e2, e3, e4} {
		if collect.ErrorCode(e) != "EGUARD" {
			t.Errorf("call %d: code %q, want EGUARD", i, collect.ErrorCode(e))
		}
	}
	all, bad := d.Requests()
	if len(bad) > 0 {
		t.Fatalf("requests outside the read list reached the daemon: %v", bad)
	}
	for _, r := range all {
		if !strings.HasPrefix(r, "GET /_ping") && !strings.HasPrefix(r, "HEAD /_ping") {
			t.Fatalf("unexpected request %s", r)
		}
	}
}

func TestCollectorHoldsReadMethodsOnly(t *testing.T) {
	reads := []string{"Close", "ContainerList", "ContainerStats", "DiskUsage", "Events", "Info", "Ping"}
	at := reflect.TypeOf((*api)(nil)).Elem()
	var got []string
	for i := range at.NumMethod() {
		got = append(got, at.Method(i).Name)
	}
	if !slices.Equal(got, reads) {
		t.Fatalf("api methods %v, want only %v", got, reads)
	}
	if f, _ := reflect.TypeOf(Collector{}).FieldByName("cli"); f.Type != at {
		t.Fatalf("Collector.cli is %v, want the read-only interface", f.Type)
	}
}

func newTestCollector(t *testing.T, path string, clk *clocktest.Clock, o Options) *Collector {
	t.Helper()
	o.Host = "unix://" + path
	if clk != nil {
		o.Now = clk.Now
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func byName(s collect.DockerSample) map[string]collect.Container {
	m := map[string]collect.Container{}
	for _, ct := range s.Containers {
		m[ct.Name] = ct
	}
	return m
}

func TestSnapshotDeriveAndEvents(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	clk := clocktest.New(time.Now())
	c := newTestCollector(t, path, clk, Options{CallTimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := collect.NewStatus(nil)
	var mu sync.Mutex
	var evs []collect.DockerEvent
	go c.RunEvents(ctx, status, func(e collect.DockerEvent) { mu.Lock(); evs = append(evs, e); mu.Unlock() })
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(evs) >= 7 })

	a, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.disk != nil })
	seen := map[string]uint64{}
	for _, n := range []int{1, 2, 3, 5, 6} {
		seen[dockertest.ID(n)] = d.Frames(dockertest.ID(n))
	}
	waitFor(t, func() bool {
		for id, n := range seen {
			if d.Frames(id) < n+2 {
				return false
			}
		}
		return true
	})
	clk.Advance(10 * time.Second)
	b, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Partial) != 0 {
		t.Fatalf("partial %v", b.Partial)
	}
	s := c.Derive(a, b)
	m := byName(s)
	web := m["web"]
	if len(s.Containers) != 5 || web.Stack != "shop" || web.State != "running" || web.Health != "healthy" ||
		web.StackFull != "only" {
		t.Fatalf("web %+v of %d", web, len(s.Containers))
	}
	// The fake stream reads one record per daemon second, so the rate is one counter step per second.
	if web.MemUsed != 200_000_000 || web.MemLimit != 1_000_000_000 || web.RxBps != 1000 || web.TxBps != 500 ||
		web.Restarts != 1 || web.UptimeApprox || web.Missing != nil {
		t.Fatalf("web numbers %+v", web)
	}
	for name, want := range dockertest.StatsCPU {
		if got := m[name].CPU; got != want {
			t.Errorf("%s cpu %v, want exactly %v", name, got, want)
		}
	}
	if w := m["worker"]; w.StackFull != "shared" || !w.UptimeApprox || w.UptimeSec != 3600 {
		t.Fatalf("worker %+v", w)
	}
	if sc := m["sidecar"]; sc.StackFull != "" {
		t.Fatalf("sidecar shares a namespace and must not count for stack_full: %+v", sc)
	}
	if o := m["old"]; o.State != "exited" || o.CPU != 0 || o.UptimeSec != 0 || o.Missing != nil ||
		o.ExitCode == nil || *o.ExitCode != 0 || web.ExitCode != nil {
		t.Fatalf("old %+v, web exit code %v", o, web.ExitCode)
	}
	o := m[collect.Other]
	if o.Count != 2 || o.Restarts != 1 || o.MemUsed != 400_000_000 || o.RxBps != 6000 || o.State != "running" ||
		o.Stack != "" {
		t.Fatalf("merged unsafe containers %+v", o)
	}
	if s.StackFull.Attributed != 1 || s.StackFull.Shared != 1 || s.StackFull.RxBps != web.RxBps {
		t.Fatalf("stack_full %+v", s.StackFull)
	}
	if s.VM.NCPU != 8 || s.VM.MemTotal != 8e9 || s.VM.CPU != 16+8+4+48 || s.Disk == nil || s.Disk.Images != 3000 ||
		s.Disk.VolumeCount != 2 || s.Missing != nil || s.Stale != nil {
		t.Fatalf("vm %+v disk %+v missing %v stale %v", s.VM, s.Disk, s.Missing, s.Stale)
	}
	if dk := s.Disk; dk.Reclaimable != 1200+700+300 || dk.ImagesReclaimable != 1200 || dk.ImagesUnused != 2 ||
		largestOf(dk) != 1700 || dk.VolumesReclaimable != 700 || dk.VolumesUnused != 1 || dk.BuildCacheReclaimable != 300 {
		t.Fatalf("disk figures %+v", dk)
	}
	mu.Lock()
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e.Name+":"+e.Kind)
	}
	mu.Unlock()
	want := "web:die,web:start,web:health_healthy,other:die,other:start,gone:die,gone:start"
	if strings.Join(kinds, ",") != want || c.SkippedEvents() != 1 {
		t.Fatalf("events %v skipped %d", kinds, c.SkippedEvents())
	}
	if status.Get(collect.SourceDockerEvents).State != collect.StateOK {
		t.Fatalf("events status %+v", status.Get(collect.SourceDockerEvents))
	}
	f := d.EventFilters()
	if len(f) == 0 || !strings.Contains(f[0], `"health_status":true`) || strings.Contains(f[0], "exec") {
		t.Fatalf("event filters %v", f)
	}

	out, _ := json.Marshal([]any{s, evs, status.Snapshot()})
	for _, leak := range append([]string{dockertest.Canary, dockertest.IDPrefix}, dockertest.UnsafeNames...) {
		if strings.Contains(strings.ToUpper(string(out)), strings.ToUpper(leak)) {
			t.Fatalf("%s leaked into output: %s", leak, out)
		}
	}
	if _, bad := d.Requests(); len(bad) > 0 {
		t.Fatalf("non-read requests: %v", bad)
	}
}

// TestDeriveExactRatesByID builds snapshots by hand: two containers whose names both fail the gate
// must keep their own counters, so the merged row is the exact sum.
func TestDeriveExactRatesByID(t *testing.T) {
	c := newCollector(nil, Options{})
	t0 := time.Unix(1000, 0)
	run := func(id, name string, total, system uint64, rx uint64) raw {
		return raw{id: id, name: name, state: "running", statsOK: true, cpuTotal: total, cpuSystem: system, online: 2,
			rx: rx, tx: rx}
	}
	a := Snapshot{At: t0, infoKnown: true, containers: []raw{
		run("id-a", dockertest.UnsafeNames[0], 1e9, 1e11, 0), run("id-b", dockertest.UnsafeNames[1], 7e15, 1e11, 5e9),
		run("id-c", "db", 0, 1e11, 0),
	}}
	b := Snapshot{At: t0.Add(4 * time.Second), infoKnown: true, containers: []raw{
		run("id-b", dockertest.UnsafeNames[1], 7e15+3e9, 2e11, 5e9+4000),
		run("id-a", dockertest.UnsafeNames[0], 1e9+5e8, 2e11, 2000),
		run("id-c", "db", 25e7, 2e11, 0),
	}}
	s := c.Derive(a, b)
	m := byName(s)
	// id-a: 2 CPUs * 5e8/1e11 = 1%; id-b: 2 * 3e9/1e11 = 6%; db: 2 * 2.5e8/1e11 = 0.5%.
	if o := m[collect.Other]; o.CPU != 7 || o.RxBps != 1500 || o.Count != 2 || m["db"].CPU != 0.5 || s.VM.CPU != 7.5 {
		t.Fatalf("other %+v db %+v vm %+v", o, m["db"], s.VM)
	}
	a.containers[2].stack, b.containers[2].stack = dockertest.UnsafeNames[1], dockertest.UnsafeNames[1]
	if db := byName(c.Derive(a, b))["db"]; db.Stack != collect.Other {
		t.Fatalf("an unsafe compose project reached the sample: %q", db.Stack)
	}
	a.containers[2].stack, b.containers[2].stack = "shop", "shop"
	if !s.Lacks(collect.PartDisk) || s.Lacks(collect.PartStats) {
		t.Fatalf("missing %v", s.Missing)
	}

	b.containers[2].statsOK, b.containers[2].cpuTotal = false, 0
	b.containers[0].cpuTotal = 1
	s = c.Derive(a, b)
	m = byName(s)
	if db := m["db"]; !slices.Equal(db.Missing, []string{collect.PartStats, collect.PartRates}) || db.CPU != 0 {
		t.Fatalf("db without stats %+v", db)
	}
	if o := m[collect.Other]; !slices.Contains(o.Missing, collect.PartRates) || o.CPU != 1 {
		t.Fatalf("a counter that went backwards gives no rate: %+v", o)
	}
	if !s.Lacks(collect.PartStats) {
		t.Fatalf("sample missing %v", s.Missing)
	}
}

func TestPartialFailuresAreMarked(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	clk := clocktest.New(time.Now())
	c := newTestCollector(t, path, clk, Options{StreamBackoff: 20 * time.Millisecond})
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.disk != nil })
	d.Fail("/containers/"+dockertest.ID(2)+"/stats", 500)
	d.Cut(dockertest.ID(2))
	waitFor(t, func() bool {
		_, err := c.streamFor(dockertest.ID(2)).state()
		return collect.ErrorCode(err) == "EOTHER"
	})
	d.Fail("/info", 500)
	d.Fail("/system/df", 500)
	clk.Advance(6 * time.Minute)
	b, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.diskErr != nil })
	clk.Advance(2 * time.Second)
	b2, _ := c.Snapshot(ctx)
	parts := map[string]string{}
	for _, p := range append(b.Partial, b2.Partial...) {
		parts[p.Part] = p.Code
	}
	if parts[collect.PartStats] != "EOTHER" || parts[collect.PartInfo] != "EOTHER" || parts[collect.PartDisk] != "EOTHER" {
		t.Fatalf("partial %v", parts)
	}
	s := c.Derive(b, b2)
	if w := byName(s)["worker"]; !slices.Equal(w.Missing, []string{collect.PartStats, collect.PartRates}) ||
		w.MemUsed != 0 {
		t.Fatalf("worker %+v", w)
	}
	if !s.Lacks(collect.PartStats) || !slices.Equal(s.Stale, []string{collect.PartInfo, collect.PartDisk}) ||
		s.VM.NCPU != 8 {
		t.Fatalf("missing %v stale %v vm %+v", s.Missing, s.Stale, s.VM)
	}
	d.Fail("/containers/"+dockertest.ID(2)+"/stats", 0)
	d.Fail("/info", 0)
	waitFor(t, func() bool { f, err := c.streamFor(dockertest.ID(2)).state(); return err == nil && f != nil })
	clk.Advance(2 * time.Second)
	b3, _ := c.Snapshot(ctx)
	// The first record after the gap carries its own rates, so the worker has numbers again at once.
	if s := c.Derive(b2, b3); !slices.Equal(s.Stale, []string{collect.PartDisk}) || byName(s)["worker"].Missing != nil ||
		byName(s)["worker"].CPU != dockertest.StatsCPU["worker"] {
		t.Fatalf("after recovery stale %v worker %+v", s.Stale, byName(s)["worker"])
	}
}

// TestSilentSocketIsBounded: a daemon that accepts and never answers must cost each call at most CallTimeout,
// even while the event stream is stuck opening on the same client.
func TestSilentSocketIsBounded(t *testing.T) {
	path := dockertest.ShortSocket(t)
	stop := dockertest.StartSilent(t, path)
	defer stop()
	limit := 300 * time.Millisecond
	c := newTestCollector(t, path, nil, Options{CallTimeout: limit})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := collect.NewStatus(nil)
	go c.RunEvents(ctx, status, nil)
	for range 4 {
		start := time.Now()
		_, err := c.Snapshot(ctx)
		if took := time.Since(start); err == nil || took > limit+200*time.Millisecond {
			t.Fatalf("snapshot err %v after %v", err, took)
		}
		if collect.ErrorCode(err) != "ETIMEDOUT" {
			t.Fatalf("code %q", collect.ErrorCode(err))
		}
	}
	waitFor(t, func() bool { return status.Get(collect.SourceDockerEvents).LastError == "ETIMEDOUT" })
}

func TestHungStatsDoesNotStallSnapshot(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	limit := 400 * time.Millisecond
	c := newTestCollector(t, path, nil, Options{CallTimeout: limit})
	d.Hang("/containers/"+dockertest.ID(2)+"/stats", true)
	start := time.Now()
	s, err := c.Snapshot(context.Background())
	if took := time.Since(start); err != nil || took > limit+200*time.Millisecond {
		t.Fatalf("snapshot err %v after %v", err, took)
	}
	if len(s.Partial) == 0 || s.Partial[0].Code != "ETIMEDOUT" {
		t.Fatalf("partial %v", s.Partial)
	}
	for _, r := range s.containers {
		if r.id == dockertest.ID(1) && !r.statsOK {
			t.Fatal("one hung stats call cost the others theirs")
		}
	}
}

func TestQuietStreamReopensWithoutFailing(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	d.Quiet(true)
	c := newTestCollector(t, path, nil, Options{IdleTimeout: 400 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := collect.NewStatus(nil)
	go c.RunEvents(ctx, status, nil)
	waitFor(t, func() bool { return len(d.EventFilters()) >= 3 })
	if st := status.Get(collect.SourceDockerEvents); st.State != collect.StateOK || st.LastError != "" {
		t.Fatalf("an idle reopen must not read as a failure: %+v", st)
	}
}

// msg is an event of the container named by label, under an ID of the daemon's shape.
func msg(label, action string, at time.Time) events.Message {
	return events.Message{Type: events.ContainerEventType, Action: events.Action(action), TimeNano: at.UnixNano(),
		Actor: events.Actor{ID: hexID(label), Attributes: map[string]string{"name": "n" + label}}}
}

func hexID(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

func TestRestartsClearedWhenContainerIsGone(t *testing.T) {
	c := newCollector(nil, Options{})
	t0 := time.Unix(5000, 0)
	for i, a := range []string{"die", "start", "die", "start"} {
		c.apply(msg("a", a, t0.Add(time.Duration(i)*time.Second)))
		c.apply(msg("b", a, t0.Add(time.Duration(i)*time.Second+time.Millisecond)))
	}
	if r, _ := c.events.counts(); r[hexID("a")] != 2 || r[hexID("b")] != 2 {
		t.Fatalf("restarts %v", r)
	}
	c.apply(msg("b", "destroy", t0.Add(10*time.Second)))
	c.events.prune(map[string]bool{"x": true}, t0.Add(2*time.Second))
	if r, s := c.events.counts(); r[hexID("a")] != 2 || r[hexID("b")] != 0 || len(s) != 1 {
		t.Fatalf("after destroy restarts %v started %v", r, s)
	}
	c.events.prune(map[string]bool{"x": true}, t0.Add(time.Minute))
	if r, s := c.events.counts(); len(r) != 0 || len(s) != 0 || len(c.events.seen) != 0 {
		t.Fatalf("after prune restarts %v started %v", r, s)
	}
}

func TestRestartsCountCrashesAndDockerRestartOnly(t *testing.T) {
	cases := map[string]struct {
		events []string
		want   int
	}{
		"one-shot run again":                 {[]string{"start", "die:0", "start", "die:0", "start"}, 0},
		"crash and start":                    {[]string{"start", "die:1", "start"}, 1},
		"crash, then two starts":             {[]string{"start", "die:1", "start", "start"}, 1},
		"kill signal and start":              {[]string{"start", "die:137", "start"}, 1},
		"unknown code and start":             {[]string{"start", "die", "start"}, 1},
		"crash, then a clean run":            {[]string{"die:1", "start", "die:0", "start"}, 1},
		"docker restart, clean exit":         {[]string{"kill", "die:0", "stop", "start", "restart"}, 1},
		"docker restart, killed":             {[]string{"kill", "die:137", "stop", "start", "restart"}, 1},
		"docker restart twice":               {[]string{"die:0", "start", "restart", "die:0", "start", "restart"}, 2},
		"crash, then docker restart":         {[]string{"die:1", "start", "die:0", "start", "restart"}, 2},
		"docker restart of a stopped one":    {[]string{"die:0", "start", "die:0", "start", "restart"}, 1},
		"restart event without its start":    {[]string{"restart"}, 1},
		"crash not started again":            {[]string{"start", "die:1"}, 0},
		"removed container starts from zero": {[]string{"die:1", "start", "die:1", "destroy", "start"}, 0},
	}
	for name, tc := range cases {
		c := newCollector(nil, Options{})
		t0 := time.Unix(5000, 0)
		for i, step := range tc.events {
			action, code, known := strings.Cut(step, ":")
			m := msg("a", action, t0.Add(time.Duration(i)*time.Second))
			if known {
				m.Actor.Attributes["exitCode"] = code
			}
			c.apply(m)
		}
		if r, _ := c.events.counts(); r[hexID("a")] != tc.want {
			t.Errorf("%s: %d restarts, want %d", name, r[hexID("a")], tc.want)
		}
	}
}

func TestExitCodeOfAnExitedContainer(t *testing.T) {
	code := func(n int32) *int32 { return &n }
	same := func(a, b *int32) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	for _, tc := range []struct {
		state, status string
		want          *int32
	}{
		{"exited", "Exited (0) 2 hours ago", code(0)}, {"exited", "Exited (137) About a minute ago", code(137)},
		{"exited", "Exited (-1) 5 seconds ago", code(-1)}, {"exited", "Exited (99999999999) 5 seconds ago", nil},
		{"exited", "", nil}, {"exited", "Exited () 5 seconds ago", nil}, {"exited", "Was Exited (0)", nil},
		{"restarting", "Restarting (0) 5 seconds ago", nil}, {"running", "Up 2 hours", nil}, {"dead", "Dead", nil},
		{"running", "Exited (0) 2 hours ago", nil},
	} {
		r := fromSummary(&container.Summary{State: container.ContainerState(tc.state), Status: tc.status})
		if !same(r.exitCode, tc.want) {
			t.Errorf("%s %q: exit code %v, want %v", tc.state, tc.status, r.exitCode, tc.want)
		}
	}
	c := newCollector(nil, Options{})
	exited := func(id, name string, n *int32) raw { return raw{id: id, name: name, state: "exited", exitCode: n} }
	for name, tc := range map[string]struct {
		codes []*int32
		want  *int32
	}{
		"all equal": {[]*int32{code(0), code(0)}, code(0)}, "different": {[]*int32{code(0), code(1), code(0)}, nil},
		"unknown last": {[]*int32{code(0), nil}, nil}, "unknown first": {[]*int32{nil, code(0), code(0)}, nil},
	} {
		s := Snapshot{At: time.Unix(1000, 0), infoKnown: true, containers: []raw{exited("id-job", "job", code(0))}}
		for i, n := range tc.codes {
			s.containers = append(s.containers, exited(fmt.Sprintf("id-%d", i), dockertest.UnsafeNames[i%2], n))
		}
		m := byName(c.Derive(s, s))
		if job := m["job"]; !same(job.ExitCode, code(0)) {
			t.Errorf("%s: job %+v", name, job)
		}
		if o := m[collect.Other]; o.Count != len(tc.codes) || !same(o.ExitCode, tc.want) {
			t.Errorf("%s: merged %+v, want exit code %v", name, o, tc.want)
		}
	}
}

func TestParseUp(t *testing.T) {
	cases := map[string]time.Duration{
		"Up 3 hours (healthy)": 3 * time.Hour, "Up About a minute": time.Minute, "Up 45 seconds": 45 * time.Second,
		"Up 2 days": 48 * time.Hour, "Up Less than a second": 0, "Up 2 hours (Paused)": 2 * time.Hour,
	}
	for in, want := range cases {
		if got, ok := parseUp(in); !ok || got != want {
			t.Errorf("parseUp(%q) = %v %v", in, got, ok)
		}
	}
	for _, in := range []string{"Exited (0) 2 hours ago", "Restarting (1) 5 seconds ago", "Created", ""} {
		if _, ok := parseUp(in); ok {
			t.Errorf("parseUp(%q) accepted", in)
		}
	}
}

func TestKindOf(t *testing.T) {
	cases := map[string]string{
		"start": "start", "die": "die", "health_status: unhealthy": "health_unhealthy",
		"health_status: CANARY": "health", "exec_create: sh -c secret": kindExec, "exec_die": kindExec,
		"attach": "other", "copy: /etc/secret": "other",
	}
	for in, want := range cases {
		if got := kindOf(in); got != want {
			t.Errorf("kindOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLargestImageFailureKeepsOtherDiskFigures(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	clk := clocktest.New(time.Now())
	c := newTestCollector(t, path, clk, Options{})
	ctx := context.Background()
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.disk != nil && !c.diskBusy })
	if largestOf(c.disk) != 1700 {
		t.Fatalf("first poll %+v", c.disk)
	}
	main := `{"ImageUsage":{"TotalSize":4000,"TotalCount":3},"VolumeUsage":{"TotalSize":2000},` +
		`"BuildCacheUsage":{"TotalSize":1000},"ContainerUsage":{"TotalSize":500}}`
	d.Body("/system/df", main)
	for _, tc := range []struct {
		name, code string
		fault      func()
	}{
		{"fails", "EOTHER", func() { d.Fail("/system/df?verbose", 500) }},
		{"over the cap", "ETOOLARGE", func() {
			d.Fail("/system/df?verbose", 0)
			d.Body("/system/df?verbose", `{"ImageUsage":{"Items":`+emptyObjects(16<<20-40)+`}}`)
		}},
	} {
		tc.fault()
		clk.Advance(6 * time.Minute)
		if _, err := c.Snapshot(ctx); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			return !c.diskBusy && c.diskAt.Equal(clk.Now()) && collect.ErrorCode(c.diskErr) == tc.code
		})
		s, _ := c.Snapshot(ctx)
		var code string
		for _, p := range s.Partial {
			if p.Part == collect.PartDisk {
				code = p.Code
			}
		}
		if code != tc.code || largestOf(c.disk) != 1700 || c.disk.Images != 4000 || c.disk.Containers != 500 {
			t.Fatalf("%s: code %q disk %+v", tc.name, code, c.disk)
		}
	}
}

// largestOf is the largest image of d, or -1 while it is unknown.
func largestOf(d *collect.DockerDisk) int64 {
	if d.LargestImage == nil {
		return -1
	}
	return *d.LargestImage
}

// The image list failing on the very first poll leaves the largest image unknown, not 0, and the disk part stale;
// it is known from the first list that is read and kept through a later failure.
func TestLargestImageIsUnknownUntilItWasRead(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	d.Fail("/system/df?verbose", 500)
	clk := clocktest.New(time.Now())
	c := newTestCollector(t, path, clk, Options{})
	ctx := context.Background()
	poll := func() collect.DockerSample {
		prev, err := c.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return !c.diskBusy && c.diskAt.Equal(clk.Now()) })
		cur, err := c.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return c.Derive(prev, cur)
	}
	s := poll()
	if s.Disk == nil || s.Disk.LargestImage != nil || s.Disk.Images != 3000 ||
		!slices.Contains(s.Stale, collect.PartDisk) {
		t.Fatalf("first poll without the image list: disk %+v, stale %v; want no largest image and a stale disk", s.Disk,
			s.Stale)
	}
	d.Fail("/system/df?verbose", 0)
	clk.Advance(6 * time.Minute)
	if s = poll(); largestOf(s.Disk) != 1700 || slices.Contains(s.Stale, collect.PartDisk) {
		t.Fatalf("after the list was read: largest %d, stale %v; want 1700 and not stale", largestOf(s.Disk), s.Stale)
	}
	d.Fail("/system/df?verbose", 500)
	clk.Advance(6 * time.Minute)
	if s = poll(); largestOf(s.Disk) != 1700 || !slices.Contains(s.Stale, collect.PartDisk) {
		t.Fatalf("a later failure: largest %d, stale %v; want 1700 kept and a stale disk", largestOf(s.Disk), s.Stale)
	}
}

func TestDiskClampsInTheCollector(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	d.Body("/system/df", `{"ImageUsage":{"TotalSize":10,"TotalCount":1,"ActiveCount":4,"Reclaimable":-5},`+
		`"VolumeUsage":{"TotalSize":10,"TotalCount":2,"ActiveCount":3,"Reclaimable":-6},`+
		`"BuildCacheUsage":{"TotalSize":10,"Reclaimable":-7},"ContainerUsage":{"TotalSize":10,"Reclaimable":-8}}`)
	c := newTestCollector(t, path, clocktest.New(time.Now()), Options{})
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.disk != nil })
	if dk := c.disk; dk.Reclaimable != 0 || dk.ImagesReclaimable != 0 || dk.ImagesUnused != 0 ||
		dk.VolumesReclaimable != 0 || dk.VolumesUnused != 0 || dk.BuildCacheReclaimable != 0 {
		t.Fatalf("negative figures not clamped: %+v", dk)
	}
}
