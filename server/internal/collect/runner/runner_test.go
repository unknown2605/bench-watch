package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/clocktest"
	"benchwatch/server/internal/collect/docker"
	"benchwatch/server/internal/collect/docker/dockertest"
	"benchwatch/server/internal/collect/machine"
)

// tickSource is a machine.Source whose counters grow on every read; failMemory makes Memory fail.
type tickSource struct {
	mu         sync.Mutex
	n          float64
	failMemory bool
	noProcs    bool
}

func (s *tickSource) failProcs(on bool) { s.mu.Lock(); s.noProcs = on; s.mu.Unlock() }

func (s *tickSource) next() float64 { s.mu.Lock(); defer s.mu.Unlock(); s.n++; return s.n }
func (s *tickSource) CoreKinds(context.Context, int) ([]collect.CoreKind, string) {
	return []collect.CoreKind{collect.CoreE, collect.CoreP}, "fake"
}
func (s *tickSource) CPUTimes(context.Context) ([]machine.CPUTimes, error) {
	n := s.next()
	return []machine.CPUTimes{{Busy: n, Total: 2 * n}, {Busy: n, Total: 4 * n}}, nil
}
func (s *tickSource) Load(context.Context) (collect.Load, error) { return collect.Load{One: 1}, nil }
func (s *tickSource) Memory(context.Context) (collect.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failMemory {
		return collect.Memory{}, syscall.EPERM
	}
	return collect.Memory{Total: 16 << 30}, nil
}
func (s *tickSource) Volumes(context.Context) ([]collect.Volume, error) { return nil, nil }
func (s *tickSource) DiskIO(context.Context) ([]machine.IOCount, error) { return nil, nil }
func (s *tickSource) NetIO(context.Context) ([]machine.IOCount, error) {
	n := uint64(s.next())
	return []machine.IOCount{{Name: "en0", InBytes: n * 1000, OutBytes: n * 10}}, nil
}
func (s *tickSource) Procs(context.Context) ([]machine.Proc, error) {
	s.mu.Lock()
	fail := s.noProcs
	s.mu.Unlock()
	if fail {
		return nil, syscall.EPERM
	}
	return []machine.Proc{{PID: 1, Start: 1, Name: "launchd", CPUSec: s.next(), RSS: 1 << 20}}, nil
}

var t0 = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

// testHist and testLive are the steps most tests were written for; the defaults have checks of their own.
const (
	testHist = 10 * time.Second
	testLive = 2 * time.Second
)

func newRunner(
	t *testing.T, clk *clocktest.Clock, o Options, dc *docker.Collector, sink collect.Sink,
) (*Runner, *tickSource) {
	t.Helper()
	if o.Hist == 0 {
		o.Hist = testHist
	}
	if o.Live == 0 {
		o.Live = testLive
	}
	o.Clock = clk
	src := &tickSource{}
	return New(o, machine.New(src, machine.Options{Now: clk.Now}), dc, sink, collect.NewStatus(clk.Now)), src
}

func start(t *testing.T, r *Runner) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }
}

func drive(t *testing.T, clk *clocktest.Clock, what string, steps int, cond func() bool) {
	t.Helper()
	for range steps {
		if cond() {
			return
		}
		clk.Advance(2 * time.Second)
		for i := 0; i < 200 && !cond(); i++ {
			time.Sleep(time.Millisecond)
		}
	}
	if !cond() {
		t.Fatalf("not reached within %d steps: %s", steps, what)
	}
}

func dockerAt(t *testing.T, path string, clk *clocktest.Clock) *docker.Collector {
	t.Helper()
	dc, err := docker.New(docker.Options{Host: "unix://" + path, Now: clk.Now, CallTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dc.Close() })
	return dc
}

func TestDockerDownThenBack(t *testing.T) {
	path := dockertest.ShortSocket(t)
	clk := clocktest.New(t0)
	r, _ := newRunner(t, clk, Options{}, dockerAt(t, path, clk), &collect.MemSink{})
	stop := start(t, r)
	defer stop()
	state := func(src string) collect.State { return r.Status.Get(src).State }
	drive(t, clk, "docker unavailable", 10, func() bool { return state(collect.SourceDocker) == collect.StateUnavailable })
	if code := r.Status.Get(collect.SourceDocker).LastError; code != "ENOENT" {
		t.Fatalf("down code %q, want ENOENT", code)
	}
	before := r.MachineLive.Len()
	drive(t, clk, "machine samples while Docker is down", 20, func() bool { return r.MachineLive.Len() > before+3 })
	if state(collect.SourceMachine) != collect.StateOK || r.DockerLive.Len() != 0 {
		t.Fatalf("machine %s, docker samples %d", state(collect.SourceMachine), r.DockerLive.Len())
	}
	d := dockertest.Start(t, path)
	drive(t, clk, "docker back", 20, func() bool {
		return state(collect.SourceDocker) == collect.StateOK && r.DockerLive.Len() > 0
	})
	waitReal(t, "events back", func() bool { return state(collect.SourceDockerEvents) == collect.StateOK })
	d.Stop()
	drive(t, clk, "docker down again", 20, func() bool { return state(collect.SourceDocker) == collect.StateUnavailable })
	d = dockertest.Start(t, path)
	defer d.Stop()
	n := r.DockerLive.Len()
	drive(t, clk, "docker back again", 20, func() bool {
		return state(collect.SourceDocker) == collect.StateOK && r.DockerLive.Len() > n
	})
	if _, bad := d.Requests(); len(bad) > 0 {
		t.Fatalf("non-read requests: %v", bad)
	}
}

// waitReal waits on work that runs on real time, such as the event stream's reconnect backoff.
func waitReal(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// states keeps each runner's tick state across ticks calls, as machineLoop does.
var states = map[*Runner]*machineState{}

func ticks(r *Runner, clk *clocktest.Clock, n int) {
	st := states[r]
	if st == nil {
		st = &machineState{}
		states[r] = st
	}
	for range n {
		r.machineTick(context.Background(), st)
		clk.Advance(r.opt.Live)
	}
}

func TestHistoryTimingTopAppsAndWallClock(t *testing.T) {
	for _, hist := range []time.Duration{10 * time.Second, 30 * time.Second} {
		t.Run(hist.String(), func(t *testing.T) { historyTiming(t, hist) })
	}
}

func historyTiming(t *testing.T, hist time.Duration) {
	clk := clocktest.New(t0)
	r, _ := newRunner(t, clk, Options{Hist: hist, Apps: time.Minute}, nil, nil)
	ticks(r, clk, int(132*time.Second/r.opt.Live))
	r.mu.Lock()
	stored := append([]collect.MachineSample(nil), r.machQ.items...)
	r.mu.Unlock()
	window := r.MachineHist.Window(clk.Now())
	want := int(132 * time.Second / hist)
	if len(stored) != want || len(window) != want {
		t.Fatalf("stored %d ring %d, want %d samples %v apart over 132 s", len(stored), len(window), want, hist)
	}
	secs := hist.Seconds()
	withApps := 0
	for i, s := range stored {
		if s.Interval != secs || len(window[i].V.TopCPU) != 1 || window[i].V.AppsWindow != secs {
			t.Fatalf("sample %d interval %v ring apps %+v", i, s.Interval, window[i].V.TopCPU)
		}
		if i > 0 && s.At.Sub(stored[i-1].At) != hist {
			t.Fatalf("samples %d and %d are %v apart, want %v", i-1, i, s.At.Sub(stored[i-1].At), hist)
		}
		if strings.Contains(s.At.String(), "m=") {
			t.Fatalf("stored time keeps a monotonic reading: %s", s.At)
		}
		if len(s.TopCPU) > 0 {
			withApps++
			if s.AppsWindow != 60 || s.At.Sub(t0) != time.Duration(60*withApps)*time.Second {
				t.Fatalf("top apps at %v over %v", s.At.Sub(t0), s.AppsWindow)
			}
		} else if s.AppsWindow != 0 || s.TopMem != nil {
			t.Fatalf("sample %d carries apps off the minute", i)
		}
	}
	if withApps != 2 {
		t.Fatalf("%d samples with top apps, want 2", withApps)
	}
}

func TestIntervalsAreSettings(t *testing.T) {
	for _, c := range []struct{ hist, apps, wantHist, wantApps time.Duration }{
		{0, 0, 30 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second},
		{30 * time.Second, 45 * time.Second, 30 * time.Second, time.Minute},
		{29 * time.Second, 0, 29 * time.Second, 29 * time.Second},
		{2 * time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second},
		{3 * time.Second, time.Minute, 3 * time.Second, time.Minute},
		{4 * time.Second, 10 * time.Second, 4 * time.Second, 12 * time.Second},
		{time.Second, 0, time.Second, time.Second},
		{1500 * time.Millisecond, 0, 2 * time.Second, 2 * time.Second},
		{0, time.Minute, 30 * time.Second, time.Minute},
	} {
		o := Options{Hist: c.hist, Apps: c.apps}
		o.defaults()
		if o.Hist != c.wantHist || o.Apps != c.wantApps {
			t.Errorf("hist %v apps %v became %v %v", c.hist, c.apps, o.Hist, o.Apps)
		}
	}
	var o Options
	o.defaults()
	if o.Keep != 6*time.Hour || o.MaxPending != 721 || o.KeepEvents != 24*time.Hour || o.MaxPendingEvents != 5000 {
		t.Fatalf("default caps %v %d %v %d", o.Keep, o.MaxPending, o.KeepEvents, o.MaxPendingEvents)
	}
	clk := clocktest.New(t0)
	r, _ := newRunner(t, clk, Options{Hist: 2 * time.Second, Apps: 2 * time.Second}, nil, nil)
	ticks(r, clk, 11)
	r.mu.Lock()
	n, apps := len(r.machQ.items), len(r.machQ.items[len(r.machQ.items)-1].TopCPU)
	r.mu.Unlock()
	if n != 10 || apps != 1 {
		t.Fatalf("2 s history: %d samples, top apps %d", n, apps)
	}
	d := New(Options{Clock: clk}, machine.New(&tickSource{}, machine.Options{Now: clk.Now}), nil, nil,
		collect.NewStatus(clk.Now))
	ticks(d, clk, 91)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.machQ.items) != 3 {
		t.Fatalf("default history: %d samples in 90 s", len(d.machQ.items))
	}
	for _, s := range d.machQ.items {
		if len(s.TopCPU) != 1 || s.AppsWindow != 30 {
			t.Fatalf("default history sample without 30 s top apps: %+v", s)
		}
	}
}

func TestSleepStartsNewSeries(t *testing.T) {
	clk := clocktest.New(t0)
	r, _ := newRunner(t, clk, Options{}, nil, nil)
	var st machineState
	for range 10 {
		r.machineTick(context.Background(), &st)
		clk.Advance(2 * time.Second)
	}
	clk.Sleep(30 * time.Minute)
	for range 10 {
		r.machineTick(context.Background(), &st)
		clk.Advance(2 * time.Second)
	}
	if r.Gaps() != 1 {
		t.Fatalf("gaps %d, want 1", r.Gaps())
	}
	for _, e := range append(r.MachineLive.Window(clk.Now()), r.MachineHist.Window(clk.Now())...) {
		if e.V.Interval > 10 {
			t.Fatalf("a sample spans the sleep: interval %v", e.V.Interval)
		}
	}
	clk.Sleep(2 * time.Hour)
	r.machineTick(context.Background(), &st)
	if w := r.MachineHist.Window(clk.Now()); len(w) != 0 {
		t.Fatalf("last hour after a 2 h sleep holds %d samples from before it", len(w))
	}
}

func TestPartialMachineIsMarked(t *testing.T) {
	clk := clocktest.New(t0)
	r, src := newRunner(t, clk, Options{}, nil, nil)
	src.failMemory = true
	ticks(r, clk, 6)
	st := r.Status.Get(collect.SourceMachine)
	if st.State != collect.StatePartial || st.LastError != "EPERM" || len(st.Missing) != 1 ||
		st.Missing[0] != collect.PartMemory {
		t.Fatalf("status %+v", st)
	}
	e, _ := r.MachineHist.Latest()
	if e.V.Memory != nil || !e.V.Lacks(collect.PartMemory) {
		t.Fatalf("memory %+v missing %v", e.V.Memory, e.V.Missing)
	}
	src.failMemory = false
	ticks(r, clk, 1)
	if st := r.Status.Get(collect.SourceMachine); st.State != collect.StateOK || st.Missing != nil {
		t.Fatalf("after recovery %+v", st)
	}
}

type recSink struct {
	collect.MemSink
	mu       sync.Mutex
	attempts []collect.Batch
	block    bool
}

func (s *recSink) Write(ctx context.Context, b collect.Batch) error {
	s.mu.Lock()
	s.attempts = append(s.attempts, b)
	block := s.block
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.MemSink.Write(ctx, b)
}

func (s *recSink) tries() []collect.Batch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]collect.Batch(nil), s.attempts...)
}

func TestRetrySendsTheSameBatch(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &recSink{}
	r, _ := newRunner(t, clk, Options{MaxBatch: 4}, nil, sink)
	ticks(r, clk, 30)
	sink.FailNext(2, syscall.ECONNREFUSED)
	for range 2 {
		if err := r.Flush(context.Background()); err == nil {
			t.Fatal("transient failure not returned")
		}
		if st := r.Status.Get(collect.SourceHistory); st.State != collect.StatePaused || st.LastError != "ECONNREFUSED" {
			t.Fatalf("status %+v", st)
		}
	}
	ticks(r, clk, 5)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := sink.tries()
	if a[0].Key != a[1].Key || a[1].Key != a[2].Key || a[0].Len() != 4 || a[2].Len() != 4 ||
		a[0].Key.Source != collect.SourceMachine {
		t.Fatalf("retries changed the batch: %+v %+v %+v", a[0].Key, a[1].Key, a[2].Key)
	}
	if !a[0].Key.At.Equal(a[0].Machine[0].At) {
		t.Fatal("key time is not the first row's time")
	}
	m, _, _ := sink.Totals()
	if m != r.MachineHist.Len() || sink.Duplicates() != 0 {
		t.Fatalf("stored %d of %d, duplicates %d", m, r.MachineHist.Len(), sink.Duplicates())
	}
	if st := r.Status.Get(collect.SourceHistory); st.State != collect.StateOK {
		t.Fatalf("status %+v", st)
	}
}

func TestPermanentErrorDropsTheBatch(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &collect.MemSink{}
	r, _ := newRunner(t, clk, Options{MaxBatch: 2}, nil, sink)
	ticks(r, clk, 26)
	sink.FailNext(1, fmt.Errorf("check constraint: %w", collect.ErrPermanent))
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("a permanent error must not stop the flush: %v", err)
	}
	m, _, _ := sink.Totals()
	pm, _, _ := r.Pending()
	if d := r.Drops(); d.Rejected != 2 || m != 3 || pm != 0 {
		t.Fatalf("drops %+v stored %d pending %d", d, m, pm)
	}
	st := r.Status.Get(collect.SourceHistory)
	if st.Drops.Rejected != 2 || st.LastError != "EPERMANENT" || st.State != collect.StatePartial ||
		len(st.Missing) != 1 || st.Missing[0] != collect.SourceMachine {
		t.Fatalf("a dropped batch must leave history partial: %+v", st)
	}
	r.Flush(context.Background())
	if st := r.Status.Get(collect.SourceHistory); st.State != collect.StatePartial {
		t.Fatalf("a flush with nothing to write must keep the loss visible: %+v", st)
	}
	ticks(r, clk, 5)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := r.Status.Get(collect.SourceHistory); st.State != collect.StateOK || st.Missing != nil {
		t.Fatalf("a later write must clear it: %+v", st)
	}
}

func TestSkippedProcessListKeepsItsFailure(t *testing.T) {
	clk := clocktest.New(t0)
	r, src := newRunner(t, clk, Options{}, nil, nil)
	ticks(r, clk, 6)
	src.failProcs(true)
	for i := range 10 {
		ticks(r, clk, 1)
		st := r.Status.Get(collect.SourceMachine)
		if i > 4 && (st.State != collect.StatePartial || st.Missing[0] != collect.PartApps) {
			t.Fatalf("tick %d: status %+v", i, st)
		}
	}
	src.failProcs(false)
	ticks(r, clk, 5)
	if st := r.Status.Get(collect.SourceMachine); st.State != collect.StateOK {
		t.Fatalf("after a good read: %+v", st)
	}
}

func TestQueueRetryKeepsItsBatch(t *testing.T) {
	q := newQueue("machine", 10, func(v time.Time) time.Time { return v }, func(b *collect.Batch, xs []time.Time) {
		for _, x := range xs {
			b.Events = append(b.Events, collect.DockerEvent{At: x})
		}
	})
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Second) }
	q.push(at(0))
	q.push(at(1))
	if b := q.batch(4); b.Len() != 2 {
		t.Fatalf("first batch %d rows", b.Len())
	}
	for i := 2; i < 10; i++ {
		q.push(at(i))
	}
	if b := q.batch(4); b.Len() != 2 || !b.Key.At.Equal(at(0)) {
		t.Fatalf("retry has %d rows at %v", b.Len(), b.Key.At)
	}
	q.expire(at(1))
	if b := q.batch(4); b.Len() != 1 || !b.Key.At.Equal(at(0)) || !b.Events[0].At.Equal(at(1)) {
		t.Fatalf("after expiry the retry keeps its key: %d rows, key %v", b.Len(), b.Key.At)
	}
	if over := q.push(at(10)) + q.push(at(11)); over != 1 || q.len() != 10 || !q.at(q.items[0]).Equal(at(1)) ||
		!q.at(q.items[1]).Equal(at(3)) {
		t.Fatalf("cap must count the batch in flight and drop the oldest row outside it: dropped %d, len %d", over, q.len())
	}
	q.finish()
	full := newQueue("machine", 3, q.at, q.set)
	for i := range 3 {
		full.push(at(i))
	}
	if b := full.batch(10); b.Len() != 2 {
		t.Fatalf("a batch must leave room for the newest row: %d", b.Len())
	}
	full.push(at(3))
	if !full.at(full.items[2]).Equal(at(3)) {
		t.Fatal("the newest row was dropped")
	}
	one := newQueue("machine", 1, q.at, q.set)
	one.push(at(0))
	if b := one.batch(10); b.Len() != 1 {
		t.Fatalf("a queue of 1 batches %d rows", b.Len())
	}
	if dropped := one.push(at(1)) + one.push(at(2)); dropped != 1 || !one.at(one.items[one.len()-1]).Equal(at(2)) {
		t.Fatalf("a queue of 1 dropped %d and kept %v last; the newest row must stay", dropped, one.items)
	}
}

// slowMachineSink times out on machine rows and stores the rest.
type slowMachineSink struct{ collect.MemSink }

func (s *slowMachineSink) Write(ctx context.Context, b collect.Batch) error {
	if b.Key.Source == collect.SourceMachine {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.MemSink.Write(ctx, b)
}

func TestAQueueThatTimesOutDoesNotStarveTheOthers(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &slowMachineSink{}
	r, _ := newRunner(t, clk, Options{FlushTimeout: 30 * time.Millisecond}, nil, sink)
	ticks(r, clk, 12)
	r.addEvent(collect.DockerEvent{At: clk.Now(), Name: "web", Kind: "start"})
	for range 3 {
		if err := r.Flush(context.Background()); collect.ErrorCode(err) != "ETIMEDOUT" {
			t.Fatalf("flush: %v", err)
		}
		waitIdle(t, r)
	}
	if _, _, ev := sink.Totals(); ev != 1 {
		t.Fatalf("events behind a machine queue that times out: %d stored after 3 flushes", ev)
	}
}

// waitIdle waits until an abandoned write has returned and freed the sink.
func waitIdle(t *testing.T, r *Runner) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); len(r.writing) > 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the abandoned sink write never returned")
		}
	}
}

// gatedMachineSink records the source of each write; machine writes wait for release, whatever their context.
type gatedMachineSink struct {
	collect.MemSink
	release chan struct{}
	mu      sync.Mutex
	order   []string
}

func (s *gatedMachineSink) Write(ctx context.Context, b collect.Batch) error {
	s.mu.Lock()
	s.order = append(s.order, b.Key.Source)
	s.mu.Unlock()
	if b.Key.Source == collect.SourceMachine {
		<-s.release
	}
	return s.MemSink.Write(ctx, b)
}

func TestAFlushRefusedAsBusyKeepsTheNextQueueFirst(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &gatedMachineSink{release: make(chan struct{})}
	r, _ := newRunner(t, clk, Options{FlushTimeout: 20 * time.Millisecond}, nil, sink)
	ticks(r, clk, 12)
	r.addEvent(collect.DockerEvent{At: clk.Now(), Name: "web", Kind: "start"})
	for range 2 {
		if err := r.Flush(context.Background()); collect.ErrorCode(err) != "ETIMEDOUT" {
			t.Fatalf("flush: %v", err)
		}
	}
	close(sink.release)
	waitIdle(t, r)
	sink.mu.Lock()
	seen := len(sink.order)
	sink.mu.Unlock()
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("flush after the sink recovered: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if seen != 1 || len(sink.order) <= seen || sink.order[seen] != collect.SourceDockerEvents {
		t.Fatalf("writes before %d, first write after the busy flushes %v; want dockerEvents", seen, sink.order[seen:])
	}
}

func TestOnlyOneAbandonedSinkWriteRuns(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &failSink{ignore: true}
	r, _ := newRunner(t, clk, Options{FlushTimeout: 20 * time.Millisecond}, nil, sink)
	ticks(r, clk, 12)
	base := runtime.NumGoroutine()
	for range 10 {
		if err := r.Flush(context.Background()); collect.ErrorCode(err) != "ETIMEDOUT" {
			t.Fatalf("flush: %v", err)
		}
	}
	if grew := runtime.NumGoroutine() - base; grew > 1 {
		t.Fatalf("10 flushes into a sink that never returns left %d goroutines", grew)
	}
	if pm, _, _ := r.Pending(); pm == 0 {
		t.Fatal("the rows of an unfinished write must stay pending")
	}
}

type failSink struct {
	collect.MemSink
	err    map[string]error
	ignore bool
}

func (s *failSink) Write(ctx context.Context, b collect.Batch) error {
	if s.ignore {
		select {}
	}
	if err := s.err[b.Key.Source]; err != nil {
		return err
	}
	return s.MemSink.Write(ctx, b)
}

func TestUnclassifiedErrorDoesNotBlockOtherQueues(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &failSink{err: map[string]error{collect.SourceMachine: fmt.Errorf("bad row")}}
	r, _ := newRunner(t, clk, Options{}, nil, sink)
	ticks(r, clk, 12)
	r.addEvent(collect.DockerEvent{At: clk.Now(), Name: "web", Kind: "start"})
	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("the machine error must be returned")
	}
	if _, _, ev := sink.Totals(); ev != 1 {
		t.Fatalf("events behind a failing machine queue: %d stored", ev)
	}
	if st := r.Status.Get(collect.SourceHistory); st.State != collect.StatePaused || st.LastError != "EOTHER" {
		t.Fatalf("status %+v", st)
	}
}

func TestSinkIgnoringContextCannotHangShutdown(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &failSink{ignore: true}
	o := Options{FlushTimeout: 50 * time.Millisecond, StopFlushTimeout: 100 * time.Millisecond}
	r, _ := newRunner(t, clk, o, nil, sink)
	ticks(r, clk, 12)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	began := time.Now()
	r.Run(ctx)
	if took := time.Since(began); took > time.Second {
		t.Fatalf("shutdown took %v", took)
	}
	if pm, _, _ := r.Pending(); pm == 0 {
		t.Fatal("an unfinished write must keep its rows")
	}
}

func TestPendingCapsByCountAndAge(t *testing.T) {
	clk := clocktest.New(t0)
	r, _ := newRunner(t, clk, Options{Keep: time.Hour, MaxPending: 3, MaxPendingEvents: 2}, nil, nil)
	ticks(r, clk, 31)
	for i := range 5 {
		r.addEvent(collect.DockerEvent{At: clk.Now().Add(time.Duration(i) * time.Millisecond), Name: "web", Kind: "start"})
	}
	pm, _, pe := r.Pending()
	if pm != 3 || pe != 2 || r.Drops().Overflow != 3+3 {
		t.Fatalf("pending %d events %d drops %+v", pm, pe, r.Drops())
	}
	r.mu.Lock()
	newest := r.machQ.items[2].At
	r.mu.Unlock()
	if !newest.Equal(r.MachineHist.Window(clk.Now())[5].At) {
		t.Fatal("count cap must drop the oldest rows")
	}
	clk.Advance(2 * time.Hour)
	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("no sink must read as paused")
	}
	pm, _, pe = r.Pending()
	st := r.Status.Get(collect.SourceHistory)
	if pm != 0 || pe != 2 || st.Drops.Expired != 3 || st.Drops.Overflow != 6 || st.LastError != "ENOSINK" {
		t.Fatalf("after 2 h: pending %d %d status %+v", pm, pe, st)
	}
	clk.Advance(23 * time.Hour)
	r.Flush(context.Background())
	if _, _, pe = r.Pending(); pe != 0 || r.Status.Get(collect.SourceHistory).Drops.Expired != 5 {
		t.Fatalf("after 25 h: pending events %d drops %+v", pe, r.Status.Get(collect.SourceHistory).Drops)
	}
}

func TestDockerAndEventQueuesAreCapped(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	clk := clocktest.New(time.Now())
	sink := &collect.MemSink{}
	sink.SetFail(syscall.ECONNREFUSED)
	r, _ := newRunner(t, clk, Options{MaxPending: 2, MaxPendingEvents: 3}, dockerAt(t, path, clk), sink)
	stop := start(t, r)
	defer stop()
	drive(t, clk, "docker and event caps", 60, func() bool {
		_, pd, pe := r.Pending()
		return pd == 2 && pe == 3 && r.Drops().Overflow >= 4
	})
	_, pd, pe := r.Pending()
	if pd != 2 || pe != 3 {
		t.Fatalf("pending docker %d events %d", pd, pe)
	}
}

func TestStopFlushesWithinItsOwnTimeout(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &recSink{}
	r, _ := newRunner(t, clk, Options{Flush: time.Hour, StopFlushTimeout: 200 * time.Millisecond}, nil, sink)
	stop := start(t, r)
	drive(t, clk, "history samples", 40, func() bool { return r.MachineHist.Len() >= 3 })
	stop()
	m, _, _ := sink.Totals()
	if pm, _, _ := r.Pending(); m != r.MachineHist.Len() || pm != 0 {
		t.Fatalf("shutdown flushed %d of %d, pending %d", m, r.MachineHist.Len(), pm)
	}

	sink2 := &recSink{block: true}
	r2, _ := newRunner(t, clk, Options{Flush: time.Hour, StopFlushTimeout: 200 * time.Millisecond}, nil, sink2)
	ticks(r2, clk, 12)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	began := time.Now()
	r2.Run(ctx)
	if took := time.Since(began); took > time.Second || len(sink2.tries()) == 0 {
		t.Fatalf("stop flush took %v with %d tries", took, len(sink2.tries()))
	}
}

func TestFlushBacksOff(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &collect.MemSink{}
	sink.SetFail(syscall.ECONNREFUSED)
	r, _ := newRunner(t, clk, Options{MaxBackoff: 40 * time.Second}, nil, sink)
	ticks(r, clk, 6)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.flushLoop(ctx); close(done) }()
	var at []int
	for sec := 1; sec <= 120; sec++ {
		waitReal(t, "flush loop waiting", func() bool { return clk.Afters() == 1 })
		before := sink.Writes()
		clk.Advance(time.Second)
		if sec == 10 || sec == 30 || sec == 70 || sec == 110 {
			waitReal(t, "flush attempt", func() bool { return sink.Writes() > before })
		}
		if sink.Writes() > before {
			at = append(at, sec)
		}
	}
	cancel()
	<-done
	if fmt.Sprint(at) != "[10 30 70 110]" {
		t.Fatalf("attempts at %v s, want 10, 30, 70, 110", at)
	}
}

// TestPrivacyCanaries runs the whole pipeline against a daemon whose every field outside the allowed set
// holds a canary, then checks no output (rings, sink, status) contains one.
func TestPrivacyCanaries(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	clk := clocktest.New(time.Now())
	sink := &collect.MemSink{}
	r, _ := newRunner(t, clk, Options{}, dockerAt(t, path, clk), sink)
	stop := start(t, r)
	drive(t, clk, "docker history and events", 60, func() bool {
		_, dn, en := sink.Totals()
		return dn >= 2 && en >= 6
	})
	stop()
	now := clk.Now()
	out, err := json.Marshal(map[string]any{
		"machineLive": r.MachineLive.Window(now), "machineHist": r.MachineHist.Window(now),
		"dockerLive": r.DockerLive.Window(now), "dockerHist": r.DockerHist.Window(now),
		"events": r.Events.Window(now), "sink": sink.Batches(), "status": r.Status.Snapshot(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToUpper(string(out))
	for _, leak := range append([]string{dockertest.Canary, "/USERS/"}, dockertest.UnsafeNames...) {
		if strings.Contains(s, strings.ToUpper(leak)) {
			t.Fatalf("%s leaked: %s", leak, out)
		}
	}
	for _, want := range []string{`"name":"web"`, `"stack":"shop"`, `"kind":"health_healthy"`, `"name":"other"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("expected %s in output", want)
		}
	}
	if _, bad := d.Requests(); len(bad) > 0 {
		t.Fatalf("non-read requests: %v", bad)
	}
}

func TestDockerPartialIsReported(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	d.Fail("/info", 500)
	clk := clocktest.New(time.Now())
	r, _ := newRunner(t, clk, Options{}, dockerAt(t, path, clk), nil)
	var st dockerState
	r.dockerTick(context.Background(), &st)
	if s := r.Status.Get(collect.SourceDocker); s.State != collect.StatePartial || len(s.Missing) != 1 ||
		s.Missing[0] != collect.PartInfo {
		t.Fatalf("status %+v", s)
	}
	d.Fail("/info", 0)
	r.dockerTick(context.Background(), &st)
	if s := r.Status.Get(collect.SourceDocker); s.State != collect.StateOK {
		t.Fatalf("after recovery %+v", s)
	}
}

func TestLostRowsStayVisibleForTheLossWindow(t *testing.T) {
	clk := clocktest.New(t0)
	sink := &collect.MemSink{}
	r, _ := newRunner(t, clk, Options{MaxBatch: 2}, nil, sink)
	ticks(r, clk, 26)
	if r.RecentLoss() {
		t.Fatal("no row lost yet")
	}
	sink.FailNext(1, fmt.Errorf("check constraint: %w", collect.ErrPermanent))
	_ = r.Flush(context.Background())
	ticks(r, clk, 5)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := r.Status.Get(collect.SourceHistory); st.State != collect.StateOK || !r.RecentLoss() {
		t.Fatalf("after a clean write: history %s, recent loss %v; want ok and still a recent loss", st.State,
			r.RecentLoss())
	}
	clk.Advance(LossWindow)
	if r.RecentLoss() {
		t.Error("a loss older than the window still counts")
	}
}

// CM-00015: by default a live sample lands every second while history keeps one sample per 30 s.
func TestDefaultCadence(t *testing.T) {
	clk := clocktest.New(t0)
	r := New(Options{Clock: clk}, machine.New(&tickSource{}, machine.Options{Now: clk.Now}), nil, nil,
		collect.NewStatus(clk.Now))
	if r.opt.Live != time.Second || r.opt.Hist != 30*time.Second {
		t.Fatalf("live %v hist %v", r.opt.Live, r.opt.Hist)
	}
	ticks(r, clk, 91)
	live := r.MachineLive.Window(clk.Now())
	if len(live) != 90 {
		t.Fatalf("%d live samples over 90 s", len(live))
	}
	for i := 1; i < len(live); i++ {
		if d := live[i].V.At.Sub(live[i-1].V.At); d != time.Second || live[i].V.Interval != 1 {
			t.Fatalf("live samples %d and %d are %v apart, interval %v", i-1, i, d, live[i].V.Interval)
		}
	}
	r.mu.Lock()
	stored := append([]collect.MachineSample(nil), r.machQ.items...)
	r.mu.Unlock()
	if len(stored) != 3 || stored[0].Interval != 30 || stored[2].At.Sub(stored[0].At) != time.Minute {
		t.Fatalf("history kept %d samples, first over %v s", len(stored), stored[0].Interval)
	}
}

func TestDockerSocketThatAppearsAndVanishesIsFollowedWithoutRestart(t *testing.T) {
	path := dockertest.ShortSocket(t)
	resolve := func() (string, docker.Presence) {
		if _, err := os.Lstat(path); err != nil {
			return "", docker.Absent
		}
		return "unix://" + path, docker.Found
	}
	clk := clocktest.New(time.Now())
	dc, err := docker.New(docker.Options{Resolve: resolve, Now: clk.Now, CallTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dc.Close() })
	r, _ := newRunner(t, clk, Options{}, dc, nil)
	var st dockerState
	ctx := context.Background()

	r.dockerTick(ctx, &st)
	if !r.DockerAbsent() || r.Status.Get(collect.SourceDocker).State != "" {
		t.Fatalf("no socket: absent %v, status %+v", r.DockerAbsent(), r.Status.Get(collect.SourceDocker))
	}

	d := dockertest.Start(t, path)
	r.dockerTick(ctx, &st)
	if r.DockerAbsent() || r.Status.Get(collect.SourceDocker).State != collect.StateOK {
		t.Fatalf("socket appeared: absent %v, status %+v", r.DockerAbsent(), r.Status.Get(collect.SourceDocker))
	}

	d.Stop()
	r.dockerTick(ctx, &st)
	if !r.DockerAbsent() {
		t.Fatal("socket vanished but Docker still counts as present")
	}
	if all, bad := d.Requests(); len(bad) > 0 {
		t.Fatalf("non-read requests: %v (of %d)", bad, len(all))
	}
}

func TestAbsentDockerDialsNothing(t *testing.T) {
	dialed := 0
	resolve := func() (string, docker.Presence) { dialed++; return "", docker.Absent }
	dc, err := docker.New(docker.Options{Resolve: resolve})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dc.Close() })
	r, _ := newRunner(t, clocktest.New(time.Now()), Options{}, dc, nil)
	var st dockerState
	r.dockerTick(context.Background(), &st)
	if dialed != 1 || r.Status.Get(collect.SourceDocker).State != "" {
		t.Fatalf("resolved %d times, status %+v; want one look at the file system and no report", dialed,
			r.Status.Get(collect.SourceDocker))
	}
	if _, err := dc.Snapshot(context.Background()); !errors.Is(err, docker.ErrAbsent) {
		t.Fatalf("a dial while absent returned %v, want ErrAbsent", err)
	}
}
