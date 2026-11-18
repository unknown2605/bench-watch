// Package runner drives the collectors: live samples every 1 s, history samples every 30 s by default,
// and batched history writes to a Sink, which pause and retry while the sink fails.
package runner

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/docker"
	"benchwatch/server/internal/collect/machine"
	"benchwatch/server/internal/collect/ring"
)

// DefaultLive and DefaultHist apply when settings give none; DefaultHist matches collector_settings (00006).
const (
	DefaultLive = time.Second
	DefaultHist = 30 * time.Second
)

type Options struct {
	Live time.Duration
	Hist time.Duration
	// Apps is how often history stores top apps, measured over that span; the history ring keeps them every Hist.
	Apps  time.Duration
	Flush time.Duration
	// FlushTimeout bounds one sink write; StopFlushTimeout bounds the whole flush on shutdown.
	FlushTimeout     time.Duration
	StopFlushTimeout time.Duration
	MaxBackoff       time.Duration
	// Keep and MaxPending bound unwritten history per source, oldest first. A pending pair holds 11 KB (24 MB at the
	// 6 h cap), or 53 KB (114 MB) with every list at MaxRows and 64-character names; measured 2026-11-08.
	Keep             time.Duration
	KeepEvents       time.Duration
	MaxPending       int
	MaxPendingEvents int
	MaxBatch         int
	CallTimeout      time.Duration
	// MaxGap: two polls further apart in wall time, or a sleep longer than SleepSlack, start a new series.
	MaxGap     time.Duration
	SleepSlack time.Duration
	Clock      collect.Clock
}

func (o *Options) defaults() {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&o.Live, DefaultLive)
	def(&o.Hist, DefaultHist)
	def(&o.Apps, o.Hist)
	def(&o.Flush, 10*time.Second)
	def(&o.FlushTimeout, 15*time.Second)
	def(&o.StopFlushTimeout, 10*time.Second)
	def(&o.MaxBackoff, 2*time.Minute)
	def(&o.Keep, 6*time.Hour)
	def(&o.KeepEvents, 24*time.Hour)
	def(&o.CallTimeout, 5*time.Second)
	// A history interval that is not a multiple of the tick would land on alternating tick counts.
	o.Hist = multiple(o.Hist, o.Live)
	o.Apps = multiple(o.Apps, o.Hist)
	def(&o.MaxGap, 5*o.Live)
	def(&o.SleepSlack, 5*time.Second)
	if o.MaxPending <= 0 {
		o.MaxPending = int(o.Keep/o.Hist) + 1
	}
	if o.MaxPendingEvents <= 0 {
		o.MaxPendingEvents = 5000
	}
	if o.MaxBatch <= 0 {
		o.MaxBatch = 360
	}
	if o.Clock == nil {
		o.Clock = collect.RealClock()
	}
}

// Intervals are the history and top-apps intervals New uses for these settings, after defaults and rounding.
func Intervals(live, hist, apps time.Duration) (time.Duration, time.Duration) {
	o := Options{Live: live, Hist: hist, Apps: apps}
	o.defaults()
	return o.Hist, o.Apps
}

func multiple(d, step time.Duration) time.Duration {
	return max(1, (d+step/2)/step) * step
}

type Timing struct {
	Count int
	Total time.Duration
	Max   time.Duration
}

func (t *Timing) add(d time.Duration) {
	t.Count++
	t.Total += d
	t.Max = max(t.Max, d)
}

type Runner struct {
	opt     Options
	clock   collect.Clock
	machine *machine.Collector
	docker  *docker.Collector
	sink    collect.Sink
	Status  *collect.Status

	MachineLive *ring.Ring[collect.MachineSample]
	MachineHist *ring.Ring[collect.MachineSample]
	DockerLive  *ring.Ring[collect.DockerSample]
	DockerHist  *ring.Ring[collect.DockerSample]
	Events      *ring.Ring[collect.DockerEvent]

	flushMu sync.Mutex
	// writing holds the one sink call allowed at a time, also after write gave up on it.
	writing chan struct{}
	firstQ  int
	mu      sync.Mutex
	machQ   *queue[collect.MachineSample]
	dockQ   *queue[collect.DockerSample]
	evQ     *queue[collect.DockerEvent]
	drops   collect.Drops
	lostAt  time.Duration
	lost    bool
	gaps    int
	timings map[string]*Timing
}

type noSinkError struct{}

func (noSinkError) Error() string { return "no sink" }
func (noSinkError) Code() string  { return "ENOSINK" }

type sinkBusyError struct{}

func (sinkBusyError) Error() string { return "an abandoned sink write is still running" }
func (sinkBusyError) Code() string  { return "ETIMEDOUT" }

// New takes a nil docker collector when Docker is not wanted, and a nil sink, which keeps history paused.
func New(opt Options, m *machine.Collector, d *docker.Collector, sink collect.Sink, status *collect.Status) *Runner {
	opt.defaults()
	if status == nil {
		status = collect.NewStatus(opt.Clock.Now)
	}
	liveCap := int(5*time.Minute/opt.Live) + 1
	histCap := int(time.Hour/opt.Hist) + 1
	return &Runner{
		opt: opt, clock: opt.Clock, machine: m, docker: d, sink: sink, Status: status,
		MachineLive: ring.New[collect.MachineSample](liveCap, 5*time.Minute),
		MachineHist: ring.New[collect.MachineSample](histCap, time.Hour),
		DockerLive:  ring.New[collect.DockerSample](liveCap, 5*time.Minute),
		DockerHist:  ring.New[collect.DockerSample](histCap, time.Hour),
		Events:      ring.New[collect.DockerEvent](opt.MaxPendingEvents, time.Hour),
		machQ: newQueue(collect.SourceMachine, opt.MaxPending, func(s collect.MachineSample) time.Time { return s.At },
			func(b *collect.Batch, xs []collect.MachineSample) { b.Machine = xs }),
		dockQ: newQueue(collect.SourceDocker, opt.MaxPending, func(s collect.DockerSample) time.Time { return s.At },
			func(b *collect.Batch, xs []collect.DockerSample) { b.Docker = xs }),
		evQ: newQueue(collect.SourceDockerEvents, opt.MaxPendingEvents, func(e collect.DockerEvent) time.Time { return e.At },
			func(b *collect.Batch, xs []collect.DockerEvent) { b.Events = xs }),
		timings: map[string]*Timing{},
		writing: make(chan struct{}, 1),
	}
}

func (r *Runner) Options() Options { return r.opt }

func (r *Runner) HasDocker() bool { return r.docker != nil }

// DockerAbsent is true while a resolving collector finds no socket.
func (r *Runner) DockerAbsent() bool { return r.docker != nil && r.docker.Absent() }

// DockerStopped is true while Docker is installed but its socket does not exist; it is not a fault.
func (r *Runner) DockerStopped() bool { return r.docker != nil && r.docker.Stopped() }

func (r *Runner) Timings() map[string]Timing {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]Timing, len(r.timings))
	for k, v := range r.timings {
		out[k] = *v
	}
	return out
}

func (r *Runner) Drops() collect.Drops {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drops
}

// LossWindow is how long health stays degraded after history last lost rows: the counts only ever grow, and a later
// clean write would otherwise hide new losses between two status reads.
const LossWindow = 10 * time.Minute

// RecentLoss is true while history rows were rejected, overflowed or expired within LossWindow.
func (r *Runner) RecentLoss() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lost && r.clock.Elapsed()-r.lostAt < LossWindow
}

// Gaps counts series restarted after a sleep, a clock jump or a stall.
func (r *Runner) Gaps() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gaps
}

func (r *Runner) Pending() (machine, docker, events int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.machQ.len(), r.dockQ.len(), r.evQ.len()
}

func (r *Runner) time(name string, d time.Duration) {
	r.mu.Lock()
	t := r.timings[name]
	if t == nil {
		t = &Timing{}
		r.timings[name] = t
	}
	t.add(d)
	r.mu.Unlock()
}

func (r *Runner) drop(d collect.Drops) {
	if d.Total() == 0 {
		return
	}
	r.mu.Lock()
	r.drops.Rejected += d.Rejected
	r.drops.Overflow += d.Overflow
	r.drops.Expired += d.Expired
	r.lostAt, r.lost = r.clock.Elapsed(), true
	r.mu.Unlock()
	r.Status.AddDrops(collect.SourceHistory, d)
}

// Run blocks until ctx ends, then flushes pending history within StopFlushTimeout. Each source has
// its own loop, so a slow or absent Docker never delays the machine.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.machineLoop(ctx) }()
	go func() { defer wg.Done(); r.flushLoop(ctx) }()
	if r.docker != nil {
		wg.Add(2)
		go func() { defer wg.Done(); r.dockerLoop(ctx) }()
		go func() { defer wg.Done(); r.docker.RunEvents(ctx, r.Status, r.addEvent) }()
	}
	wg.Wait()
	fctx, cancel := context.WithTimeout(context.Background(), r.opt.StopFlushTimeout)
	defer cancel()
	r.Flush(fctx)
}

func (r *Runner) addEvent(e collect.DockerEvent) {
	r.Events.Push(e.At, e)
	r.mu.Lock()
	over := r.evQ.push(e)
	r.mu.Unlock()
	r.drop(collect.Drops{Overflow: over})
}

func (r *Runner) every(ctx context.Context, d time.Duration, f func()) {
	t := r.clock.NewTicker(d)
	defer t.Stop()
	for {
		f()
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		}
	}
}

// pace is when a poll happened by both clocks; Elapsed pauses in a macOS sleep while wall time runs on.
type pace struct {
	wall time.Time
	mono time.Duration
}

func (r *Runner) gap(prev *pace, wall time.Time, mono time.Duration) bool {
	if prev == nil {
		return false
	}
	dw, dm := wall.Sub(prev.wall), mono-prev.mono
	if dw < 0 || dw > r.opt.MaxGap || dw-dm > r.opt.SleepSlack {
		r.mu.Lock()
		r.gaps++
		r.mu.Unlock()
		return true
	}
	return false
}

func (r *Runner) histDue(prev time.Time, now time.Time, step time.Duration) bool {
	return now.Sub(prev) >= step-r.opt.Live/2
}

type machineState struct {
	last             *pace
	live, hist, apps *machine.Snapshot
	// appsFail is the last process-list failure; ticks that skip the list report it until a read succeeds.
	appsFail *collect.PartError
}

func (st *machineState) reset() { st.last, st.live, st.hist, st.apps = nil, nil, nil, nil }

func (r *Runner) machineLoop(ctx context.Context) {
	var st machineState
	r.every(ctx, r.opt.Live, func() { r.machineTick(ctx, &st) })
}

func (st *machineState) parts(s machine.Snapshot, readProcs bool) []collect.PartError {
	if readProcs {
		st.appsFail = nil
		for _, p := range s.Partial {
			if p.Part == collect.PartApps {
				st.appsFail = &p
			}
		}
		return s.Partial
	}
	if st.appsFail == nil {
		return s.Partial
	}
	return append(slices.Clone(s.Partial), *st.appsFail)
}

func (r *Runner) machineTick(ctx context.Context, st *machineState) {
	mono := r.clock.Elapsed()
	hist := st.hist == nil || r.histDue(st.hist.At, r.clock.Now(), r.opt.Hist)
	cctx, cancel := context.WithTimeout(ctx, r.opt.CallTimeout)
	s, err := r.machine.Snapshot(cctx, hist)
	cancel()
	r.time("machine", r.clock.Elapsed()-mono)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.Status.Fail(collect.SourceMachine, collect.StateUnavailable, err)
		st.reset()
		return
	}
	r.Status.Report(collect.SourceMachine, st.parts(s, hist))
	if r.gap(st.last, s.At, mono) {
		st.reset()
	}
	st.last = &pace{s.At, mono}
	if st.live != nil {
		live := r.machine.Derive(*st.live, s)
		r.MachineLive.Push(live.At, live)
	}
	st.live = &s
	if !hist {
		return
	}
	if st.hist != nil {
		h := r.machine.Derive(*st.hist, s)
		r.MachineHist.Push(h.At, h)
		stored := h
		stored.TopCPU, stored.TopMem, stored.AppsWindow = nil, nil, 0
		if st.apps != nil && r.histDue(st.apps.At, s.At, r.opt.Apps) {
			if cpu, mem, w, ok := r.machine.Apps(*st.apps, s); ok {
				stored.TopCPU, stored.TopMem, stored.AppsWindow = cpu, mem, w
				st.apps = &s
			}
		}
		r.mu.Lock()
		over := r.machQ.push(stored)
		r.mu.Unlock()
		r.drop(collect.Drops{Overflow: over})
	}
	st.hist = &s
	if st.apps == nil && s.Procs != nil {
		st.apps = &s
	}
}

type dockerState struct {
	last       *pace
	live, hist *docker.Snapshot
}

func (st *dockerState) reset() { st.last, st.live, st.hist = nil, nil, nil }

func (r *Runner) dockerLoop(ctx context.Context) {
	var st dockerState
	r.every(ctx, r.opt.Live, func() { r.dockerTick(ctx, &st) })
}

func (r *Runner) dockerTick(ctx context.Context, st *dockerState) {
	if r.docker.Absent() {
		r.docker.Release()
		st.reset()
		return
	}
	mono := r.clock.Elapsed()
	s, err := r.docker.Snapshot(ctx)
	r.time("docker", r.clock.Elapsed()-mono)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.Status.Fail(collect.SourceDocker, collect.StateUnavailable, err)
		st.reset()
		return
	}
	r.Status.Report(collect.SourceDocker, s.Partial)
	if r.gap(st.last, s.At, mono) {
		st.reset()
	}
	st.last = &pace{s.At, mono}
	if st.live != nil {
		live := r.docker.Derive(*st.live, s)
		r.DockerLive.Push(live.At, live)
	}
	st.live = &s
	if st.hist == nil {
		st.hist = &s
		return
	}
	if r.histDue(st.hist.At, s.At, r.opt.Hist) {
		h := r.docker.Derive(*st.hist, s)
		r.DockerHist.Push(h.At, h)
		r.mu.Lock()
		over := r.dockQ.push(h)
		r.mu.Unlock()
		r.drop(collect.Drops{Overflow: over})
		st.hist = &s
	}
}

func (r *Runner) flushLoop(ctx context.Context) {
	wait := r.opt.Flush
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.clock.After(wait):
		}
		if err := r.Flush(ctx); err != nil {
			wait = min(2*wait, r.opt.MaxBackoff)
		} else {
			wait = r.opt.Flush
		}
	}
}

// Flush: a permanent error drops the batch and leaves history partial until a later write succeeds; a transient
// one keeps the batch, moves on to the next queue unless the sink timed out, and is returned.
func (r *Runner) Flush(ctx context.Context) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	now := r.clock.Now()
	cut := now.Add(-r.opt.Keep)
	r.mu.Lock()
	expired := r.machQ.expire(cut) + r.dockQ.expire(cut) + r.evQ.expire(now.Add(-r.opt.KeepEvents))
	r.mu.Unlock()
	r.drop(collect.Drops{Expired: expired})
	if r.sink == nil {
		r.Status.Fail(collect.SourceHistory, collect.StatePaused, noSinkError{})
		return noSinkError{}
	}
	var (
		transient error
		rejected  []collect.PartError
		wrote     bool
	)
	order := []func() (collect.Batch, func(bool)){r.next(r.machQ), r.next(r.dockQ), r.next(r.evQ)}
	first := r.firstQ
	order = append(order[first:], order[:first]...)
queues:
	for i, f := range order {
		for ctx.Err() == nil {
			b, done := f()
			if b.Len() == 0 {
				break
			}
			start := r.clock.Elapsed()
			err := r.write(ctx, b)
			r.time("flush", r.clock.Elapsed()-start)
			switch {
			case err == nil:
				done(true)
				wrote = true
			case collect.IsPermanent(err):
				done(true)
				r.drop(collect.Drops{Rejected: uint64(b.Len())})
				rejected = append(rejected, collect.NewPartError(b.Key.Source, err))
			default:
				done(false)
				transient = cmp.Or(transient, err)
				if collect.ErrorCode(err) == "ETIMEDOUT" {
					if _, busy := err.(sinkBusyError); !busy {
						r.firstQ = (first + i + 1) % len(order)
					}
					break queues
				}
				continue queues
			}
		}
	}
	switch {
	case transient != nil:
		r.Status.Fail(collect.SourceHistory, collect.StatePaused, transient)
		return transient
	case ctx.Err() != nil:
		return ctx.Err()
	case len(rejected) > 0:
		r.Status.Report(collect.SourceHistory, rejected)
	case wrote || r.Status.Get(collect.SourceHistory).State == "":
		r.Status.OK(collect.SourceHistory)
	}
	return nil
}

// write gives up at FlushTimeout even when the sink ignores ctx; the abandoned call may still store the
// batch, which its retry then stores once more under the same key.
func (r *Runner) write(ctx context.Context, b collect.Batch) error {
	select {
	case r.writing <- struct{}{}:
	default:
		return sinkBusyError{}
	}
	wctx, cancel := context.WithTimeout(ctx, r.opt.FlushTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		err := r.sink.Write(wctx, b)
		<-r.writing
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-wctx.Done():
		select {
		case err := <-done:
			return err
		default:
			return wctx.Err()
		}
	}
}

func (r *Runner) next(q batcher) func() (collect.Batch, func(bool)) {
	return func() (collect.Batch, func(bool)) {
		r.mu.Lock()
		defer r.mu.Unlock()
		b := q.batch(r.opt.MaxBatch)
		return b, func(finished bool) {
			if finished {
				r.mu.Lock()
				q.finish()
				r.mu.Unlock()
			}
		}
	}
}
