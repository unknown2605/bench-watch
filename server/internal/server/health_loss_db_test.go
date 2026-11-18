package server

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/machine"
	"benchwatch/server/internal/collect/runner"
	"benchwatch/server/internal/transcripts"
)

// lossSource is a machine.Source whose counters grow on every read.
type lossSource struct{ n atomic.Int64 }

func (s *lossSource) next() float64 { return float64(s.n.Add(1)) }
func (s *lossSource) CoreKinds(context.Context, int) ([]collect.CoreKind, string) {
	return []collect.CoreKind{collect.CoreP}, "fake"
}
func (s *lossSource) CPUTimes(context.Context) ([]machine.CPUTimes, error) {
	n := s.next()
	return []machine.CPUTimes{{Busy: n, Total: 2 * n}}, nil
}
func (s *lossSource) Load(context.Context) (collect.Load, error) { return collect.Load{One: 1}, nil }
func (s *lossSource) Memory(context.Context) (collect.Memory, error) {
	return collect.Memory{Total: 16 << 30}, nil
}
func (s *lossSource) Volumes(context.Context) ([]collect.Volume, error) { return nil, nil }
func (s *lossSource) DiskIO(context.Context) ([]machine.IOCount, error) { return nil, nil }
func (s *lossSource) NetIO(context.Context) ([]machine.IOCount, error)  { return nil, nil }
func (s *lossSource) Procs(context.Context) ([]machine.Proc, error)     { return nil, nil }

// runUntil runs a fast runner on the sink until done reports true, then stops it and returns it.
func runUntil(t *testing.T, sink collect.Sink, done func(*runner.Runner) bool) *runner.Runner {
	t.Helper()
	ms := time.Millisecond
	r := runner.New(runner.Options{Live: 5 * ms, Hist: 5 * ms, Apps: 5 * ms, Flush: 5 * ms, MaxBatch: 2},
		machine.New(&lossSource{}, machine.Options{}), nil, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { r.Run(ctx); close(stopped) }()
	deadline := time.Now().Add(10 * time.Second)
	for !done(r) && time.Now().Before(deadline) {
		time.Sleep(5 * ms)
	}
	cancel()
	<-stopped
	if !done(r) {
		t.Fatal("the runner never reached the state the test waits for")
	}
	return r
}

// Rows the history sink loses reach health: with every state ok, only the loss separates degraded from nominal.
func TestLostRowsReachHealth(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	dir := t.TempDir()
	sc := transcripts.New(transcripts.Options{Projects: filepath.Join(dir, "projects"),
		Cache: filepath.Join(dir, "cache")}, pool)
	sc.Tick(context.Background())

	health := func(r *runner.Runner) api.StatusResponse {
		r.Status.OK(collect.SourceMachine)
		r.Status.OK(collect.SourceDocker)
		r.Status.OK(collect.SourceHistory)
		return status(t, &Server{DB: pool, Runner: r, Scanner: sc})
	}

	kept := &collect.MemSink{}
	clean := runUntil(t, kept, func(r *runner.Runner) bool { return kept.Writes() > 0 })
	st := health(clean)
	if clean.RecentLoss() || st.Health != api.HealthNominal {
		t.Fatalf("control, no row lost: recent loss %v, health %s, scan %s %v, database %s; want nominal",
			clean.RecentLoss(), st.Health, st.Scan.State, st.Scan.WriteErrorCode, st.Database.State)
	}

	refusing := &collect.MemSink{}
	refusing.SetFail(fmt.Errorf("check constraint: %w", collect.ErrPermanent))
	lossy := runUntil(t, refusing, func(r *runner.Runner) bool { return r.RecentLoss() })
	st = health(lossy)
	if st.Database.DroppedRows.Rejected == 0 || st.Health != api.HealthDegraded {
		t.Errorf("rows lost: rejected %d, health %s; want some rejected and degraded",
			st.Database.DroppedRows.Rejected, st.Health)
	}
}
