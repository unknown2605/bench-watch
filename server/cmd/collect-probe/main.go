// Command collect-probe runs the machine and Docker collectors for a while and prints a numeric summary:
// counts, rates, timings, its own CPU and memory, and an estimate of history bytes per day.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/docker"
	"benchwatch/server/internal/collect/machine"
	"benchwatch/server/internal/collect/ring"
	"benchwatch/server/internal/collect/runner"
)

func main() {
	secs := flag.Int("seconds", 60, "how long to run")
	host := flag.String("docker", "auto", "Docker socket URL; auto finds it; empty to skip Docker")
	sinkFail := flag.Bool("sink-down", false, "make the test sink fail, as when PostgreSQL is stopped")
	cores := flag.Bool("cores", false, "print per-core busy for every live sample")
	hist := flag.Duration("hist", 0, "history detail interval, a multiple of the 1 s live tick (default 30s)")
	apps := flag.Duration("apps", 0, "top apps history interval, a multiple of -hist (default: the -hist interval)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(*secs)*time.Second)
	defer cancel()

	status := collect.NewStatus(nil)
	mc := machine.New(machine.NewDarwin(), machine.Options{})
	var dc *docker.Collector
	if *host == "auto" {
		*host, _ = docker.Resolve(docker.OSEnv())
	}
	if *host != "" {
		var err error
		if dc, err = docker.New(docker.Options{Host: *host}); err != nil {
			fmt.Println("docker client:", collect.ErrorCode(err))
			os.Exit(1)
		}
		defer dc.Close()
	}
	sink := &collect.MemSink{}
	if *sinkFail {
		sink.SetFail(fmt.Errorf("sink down: %w", syscall.ECONNREFUSED))
	}
	r := runner.New(runner.Options{Hist: *hist, Apps: *apps}, mc, dc, sink, status)

	var u0 syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &u0)
	start := time.Now()
	if *cores {
		go printCores(ctx, r)
	}
	r.Run(ctx)
	elapsed := time.Since(start)
	var u1 syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &u1)

	report(r, mc, sink, status, elapsed, u0, u1)
	if dc != nil {
		fmt.Printf("exec events seen and dropped: %d\n", dc.SkippedEvents())
	}
}

func printCores(ctx context.Context, r *runner.Runner) {
	t := time.NewTicker(r.Options().Live)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if e, ok := r.MachineLive.Latest(); ok {
				var b strings.Builder
				for _, c := range e.V.Cores {
					fmt.Fprintf(&b, " %d%s:%3.0f", c.Index, c.Kind, c.Busy)
				}
				fmt.Println("cores" + b.String())
			}
		}
	}
}

func cpuSec(u syscall.Rusage) float64 {
	return float64(u.Utime.Sec+u.Stime.Sec) + float64(u.Utime.Usec+u.Stime.Usec)/1e6
}

func mb(b uint64) float64 { return float64(b) / (1 << 20) }

func report(r *runner.Runner, mc *machine.Collector, sink *collect.MemSink, status *collect.Status,
	elapsed time.Duration, u0, u1 syscall.Rusage) {
	now := time.Now()
	used := cpuSec(u1) - cpuSec(u0)
	fmt.Printf("ran %.1f s; own CPU %.2f s = %.2f%% of one core; peak RSS %.1f MB\n",
		elapsed.Seconds(), used, 100*used/elapsed.Seconds(), mb(uint64(u1.Maxrss)))
	o := r.Options()
	fmt.Printf("intervals: live %s, history %s, top apps %s\n", o.Live, o.Hist, o.Apps)
	kinds, kindSource := mc.Kinds()
	fmt.Printf("core kinds %v from %s\n", kinds, kindSource)

	keys := []string{}
	st := status.Snapshot()
	for k := range st {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("status %-13s %-11s lastError=%q missing=%v drops=%+v\n", k, st[k].State, st[k].LastError,
			st[k].Missing, st[k].Drops)
	}
	tk := r.Timings()
	for _, k := range []string{"machine", "docker", "flush"} {
		if t, ok := tk[k]; ok && t.Count > 0 {
			fmt.Printf("timing %-8s n=%d mean %.1f ms max %.1f ms\n", k, t.Count,
				float64(t.Total.Microseconds())/float64(t.Count)/1000, float64(t.Max.Microseconds())/1000)
		}
	}
	m, d, e := sink.Totals()
	pm, pd, pe := r.Pending()
	fmt.Printf("rings: machine live %d hist %d; docker live %d hist %d; events %d; sink machine %d docker %d events %d\n",
		r.MachineLive.Len(), r.MachineHist.Len(), r.DockerLive.Len(), r.DockerHist.Len(), r.Events.Len(), m, d, e)
	fmt.Printf("pending machine %d docker %d events %d; drops %+v; gaps %d; sink writes %d duplicates %d\n",
		pm, pd, pe, r.Drops(), r.Gaps(), sink.Writes(), sink.Duplicates())

	if h, ok := r.MachineHist.Latest(); ok {
		s := h.V
		fmt.Printf("machine missing %v\n", s.Missing)
		if l := s.Load; l != nil {
			fmt.Printf("load %.2f %.2f %.2f\n", l.One, l.Five, l.Fifteen)
		}
		if mm := s.Memory; mm != nil {
			fmt.Printf("memory MB: total %.0f used %.0f app %.0f wired %.0f compressed %.0f cached %.0f free %.0f swap %.0f/%.0f\n",
				mb(mm.Total), mb(mm.Used), mb(mm.App), mb(mm.Wired), mb(mm.Compressed), mb(mm.Cached), mb(mm.Free),
				mb(mm.SwapUsed), mb(mm.SwapTotal))
		}
		var b strings.Builder
		for _, c := range s.Cores {
			fmt.Fprintf(&b, " %d%s:%.0f", c.Index, c.Kind, c.Busy)
		}
		fmt.Println("cores busy %" + b.String())
		for _, v := range s.Volumes {
			fmt.Printf("volume %-12s free %.1f of %.1f GB\n", v.Name, float64(v.Free)/1e9, float64(v.Total)/1e9)
		}
		for _, io := range s.DiskIO {
			fmt.Printf("disk %-8s read %.0f B/s write %.0f B/s\n", io.Name, io.ReadBps, io.WriteBps)
		}
		active := 0
		for _, n := range s.Net {
			if n.RxBps > 0 || n.TxBps > 0 {
				active++
				fmt.Printf("net %-10s rx %.0f B/s tx %.0f B/s\n", n.Name, n.RxBps, n.TxBps)
			}
		}
		fmt.Printf("interfaces listed %d, active %d\n", len(s.Net), active)
		for i, a := range s.TopCPU {
			fmt.Printf("top cpu %2d %-32s %6.1f%% %4d procs\n", i+1, a.Name, a.CPU, a.Procs)
		}
		for i, a := range s.TopMem {
			fmt.Printf("top mem %2d %-32s %8.0f MB %4d procs\n", i+1, a.Name, mb(a.RSS), a.Procs)
		}
		if s.DockerVM != nil {
			fmt.Printf("docker vm on the Mac: cpu %.1f%% rss %.0f MB\n", s.DockerVM.CPU, mb(s.DockerVM.RSS))
		}
	}
	if h, ok := r.DockerHist.Latest(); ok {
		s := h.V
		fmt.Printf("docker missing %v stale %v\n", s.Missing, s.Stale)
		fmt.Printf("docker vm: ncpu %d mem total %.0f MB, containers use %.0f MB, cpu %.1f%%\n",
			s.VM.NCPU, mb(s.VM.MemTotal), mb(s.VM.MemUsed), s.VM.CPU)
		if s.Disk != nil {
			fmt.Printf("docker disk GB: images %.2f (%d) volumes %.2f (%d) build cache %.2f containers %.2f\n",
				float64(s.Disk.Images)/1e9, s.Disk.ImageCount, float64(s.Disk.Volumes)/1e9, s.Disk.VolumeCount,
				float64(s.Disk.BuildCache)/1e9, float64(s.Disk.Containers)/1e9)
		}
		fmt.Printf("stack_full rx %.0f B/s tx %.0f B/s from %d containers (%d shared, not split)\n",
			s.StackFull.RxBps, s.StackFull.TxBps, s.StackFull.Attributed, s.StackFull.Shared)
		for _, c := range s.Containers {
			approx := ""
			if c.UptimeApprox {
				approx = "~"
			}
			fmt.Printf("container %-28s stack %-22s %-8s %-9s cpu %5.1f%% mem %6.0f MB rx %7.0f tx %7.0f restarts %d up %s%ds count %d missing %v\n",
				c.Name, c.Stack, c.State, c.Health, c.CPU, mb(c.MemUsed), c.RxBps, c.TxBps, c.Restarts, approx, c.UptimeSec,
				c.Count, c.Missing)
		}
	}
	evKinds := map[string]int{}
	for _, ev := range r.Events.Window(now) {
		evKinds[ev.V.Kind]++
	}
	fmt.Printf("event kinds in the last hour: %v\n", evKinds)
	estimate(r, now)
}

// estimate sizes history as PostgreSQL rows in a narrow layout for the retention of 00006: 7 days of detail and 30 of
// 1-minute and hourly averages and events; an average row set has the shape of one detail sample.
func estimate(r *runner.Runner, now time.Time) {
	mh, dh := r.MachineHist.Window(now), r.DockerHist.Window(now)
	if len(mh) == 0 {
		return
	}
	var jsonM, base, apps float64
	for _, e := range mh {
		b, _ := json.Marshal(e.V)
		jsonM += float64(len(b))
		base += machineRows(e.V)
		apps += appRows(e.V)
	}
	n := float64(len(mh))
	jsonM, base, apps = jsonM/n, base/n, apps/n
	jsonD, rowsD := 0.0, 0.0
	for _, e := range dh {
		b, _ := json.Marshal(e.V)
		jsonD += float64(len(b))
		rowsD += dockerRows(e.V)
	}
	if len(dh) > 0 {
		jsonD, rowsD = jsonD/float64(len(dh)), rowsD/float64(len(dh))
	}
	evs := r.Events.Window(now)
	evBytes := 0.0
	for _, e := range evs {
		evBytes += row(0, e.V.Name+e.V.Kind)
	}
	set := base + apps + rowsD
	hist := r.Options().Hist.Seconds()
	fmt.Printf("per %.0f s sample: machine JSON %.0f B, docker JSON %.0f B; PostgreSQL estimate machine %.0f B "+
		"(of which top apps %.0f B), docker %.0f B\n", hist, jsonM, jsonD, base+apps, apps, rowsD)
	day := set * 86400 / hist / 1e6
	fmt.Printf("detail every %.0f s with top apps every %.0f s: %.1f MB per day, %.0f MB for 7 days\n",
		hist, r.Options().Apps.Seconds(), day, 7*day)
	minute, hour := set*1440/1e6, set*24/1e6
	fmt.Printf("1-minute averages: %.0f MB for 30 days; hourly averages: %.1f MB for 30 days\n", 30*minute, 30*hour)
	evDay := evBytes * 24 / 1e6
	fmt.Printf("events: %d in the last hour, %.1f MB for 30 days at that rate\n", len(evs), 30*evDay)
	fmt.Printf("steady state: %.0f MB (heap + one index, before compression)\n",
		7*day+30*(minute+hour+evDay))
	pendingCost(mh[len(mh)-1].V, dh, hist)
}

func pendingCost(m collect.MachineSample, dh []ring.Entry[collect.DockerSample], hist float64) {
	const copies = 2000
	mj, _ := json.Marshal(m)
	var dj []byte
	if len(dh) > 0 {
		dj, _ = json.Marshal(dh[len(dh)-1].V)
	}
	heap := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}
	before := heap()
	ms, ds := make([]collect.MachineSample, copies), make([]collect.DockerSample, copies)
	for i := range copies {
		json.Unmarshal(mj, &ms[i])
		if dj != nil {
			json.Unmarshal(dj, &ds[i])
		}
	}
	per := float64(int64(heap())-int64(before)) / copies
	runtime.KeepAlive(ms)
	runtime.KeepAlive(ds)
	fmt.Printf("pending heap: %.0f B per machine + docker history sample, %.1f MB per hour of outage\n",
		per, per*3600/hist/1e6)
}

// Row model: 24 B tuple header + 4 B line pointer, 8 B timestamp, 8 B per number, name bytes + 1,
// and one b-tree entry of about 24 B on (time, name). Real figures come from pg_total_relation_size later.
func row(numbers int, name string) float64 {
	return float64(24+4+8+8*numbers+len(name)+1) + 24
}

func machineRows(s collect.MachineSample) float64 {
	b := row(14, "")
	for range s.Cores {
		b += row(2, "")
	}
	for _, v := range s.Volumes {
		b += row(2, v.Name)
	}
	for _, d := range s.DiskIO {
		b += row(4, d.Name)
	}
	for _, n := range s.Net {
		b += row(2, n.Name)
	}
	return b
}

func appRows(s collect.MachineSample) float64 {
	b := 0.0
	for _, a := range append(append([]collect.App{}, s.TopCPU...), s.TopMem...) {
		b += row(3, a.Name)
	}
	return b
}

func dockerRows(s collect.DockerSample) float64 {
	b := row(9, "")
	for _, c := range s.Containers {
		b += row(9, c.Name+c.Stack+c.State+c.Health)
	}
	return b
}
