package server

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/docker"
	"benchwatch/server/internal/collect/ring"
	"benchwatch/server/internal/collect/runner"
	"benchwatch/server/internal/history"
	"benchwatch/server/internal/store"
)

const (
	maxApps   = 10
	maxEvents = 200
)

func machineRows(s collect.MachineSample) store.SampleBatch {
	return history.Convert(collect.Batch{Machine: []collect.MachineSample{s}})
}

func dockerRows(s collect.DockerSample) store.SampleBatch {
	return history.Convert(collect.Batch{Docker: []collect.DockerSample{s}})
}

// MachineNow is the snapshot of the latest live sample; nil before the first one. Live samples read no processes, so
// top apps come from the latest history sample while it is at most two history steps older.
func (s *Server) MachineNow() *api.MachineSnapshot {
	if s.Runner == nil {
		return nil
	}
	e, ok := s.Runner.MachineLive.Latest()
	if !ok {
		return nil
	}
	v := e.V
	if len(v.TopCPU) == 0 && len(v.TopMem) == 0 {
		o := s.Runner.Options()
		if h, ok := s.Runner.MachineHist.Latest(); ok && e.At.Sub(h.At) <= 2*o.Hist+o.Live {
			v.TopCPU, v.TopMem, v.AppsWindow = h.V.TopCPU, h.V.TopMem, h.V.AppsWindow
		}
	}
	return machineSnapshot(machineRows(v))
}

func machineSnapshot(b store.SampleBatch) *api.MachineSnapshot {
	if len(b.Machine) == 0 {
		return nil
	}
	m := b.Machine[0]
	out := &api.MachineSnapshot{At: m.At.Time.UTC(), CpuBusyPct: m.CpuBusyPct, Cores: []api.CoreLoad{}}
	for _, c := range b.Cores {
		out.Cores = append(out.Cores, api.CoreLoad{Core: int32(c.Core), Kind: api.CoreKind(c.Kind), BusyPct: c.BusyPct})
		if c.Kind == store.CoreKindEfficiency {
			out.EfficiencyCores++
		} else {
			out.PerformanceCores++
		}
	}
	if m.DiskReadBps != nil && m.DiskWriteBps != nil {
		out.DiskIo = &struct {
			ReadBps  int64 `json:"readBps"`
			WriteBps int64 `json:"writeBps"`
		}{*m.DiskReadBps, *m.DiskWriteBps}
	}
	if m.Load1 != nil && m.Load5 != nil && m.Load15 != nil {
		out.Load = &struct {
			Fifteen float32 `json:"fifteen"`
			Five    float32 `json:"five"`
			One     float32 `json:"one"`
		}{*m.Load15, *m.Load5, *m.Load1}
	}
	if all(m.MemTotalBytes, m.MemUsedBytes, m.MemAppBytes, m.MemCachedBytes, m.MemCompressedBytes, m.SwapUsedBytes,
		m.SwapTotalBytes) {
		mem := &struct {
			AppBytes        int64               `json:"appBytes"`
			CachedBytes     int64               `json:"cachedBytes"`
			CompressedBytes int64               `json:"compressedBytes"`
			Pressure        *api.MemoryPressure `json:"pressure"`
			SwapTotalBytes  int64               `json:"swapTotalBytes"`
			SwapUsedBytes   int64               `json:"swapUsedBytes"`
			TotalBytes      int64               `json:"totalBytes"`
			UsedBytes       int64               `json:"usedBytes"`
		}{AppBytes: *m.MemAppBytes, CachedBytes: *m.MemCachedBytes, CompressedBytes: *m.MemCompressedBytes,
			SwapTotalBytes: *m.SwapTotalBytes, SwapUsedBytes: *m.SwapUsedBytes, TotalBytes: *m.MemTotalBytes,
			UsedBytes: *m.MemUsedBytes}
		if m.MemPressure != nil {
			mem.Pressure = ptr(api.MemoryPressure(*m.MemPressure))
		}
		out.Memory = mem
	}
	out.Interfaces = make([]struct {
		Interface   api.InterfaceLabel `json:"interface"`
		MergedNames int32              `json:"mergedNames"`
		RxBps       int64              `json:"rxBps"`
		TxBps       int64              `json:"txBps"`
	}, len(b.Interfaces))
	for i, n := range b.Interfaces {
		out.Interfaces[i].Interface, out.Interfaces[i].MergedNames = n.Interface, n.MergedNames
		out.Interfaces[i].RxBps, out.Interfaces[i].TxBps = n.RxBps, n.TxBps
	}
	out.Volumes = make([]struct {
		Filesystem  *api.FilesystemLabel `json:"filesystem"`
		FreeBytes   int64                `json:"freeBytes"`
		Kind        *api.VolumeKind      `json:"kind"`
		MergedNames int32                `json:"mergedNames"`
		TotalBytes  int64                `json:"totalBytes"`
		Volume      api.VolumeLabel      `json:"volume"`
	}, len(b.Volumes))
	for i, v := range b.Volumes {
		o := &out.Volumes[i]
		o.Filesystem, o.FreeBytes, o.MergedNames, o.TotalBytes, o.Volume = v.Filesystem, v.FreeBytes, v.MergedNames,
			v.TotalBytes, v.Volume
		if v.Kind != nil {
			o.Kind = ptr(api.VolumeKind(*v.Kind))
		}
	}
	apps := append([]store.MachineAppSample(nil), b.Apps...)
	sort.SliceStable(apps, func(i, j int) bool { return apps[i].CpuPct > apps[j].CpuPct })
	apps = apps[:min(len(apps), maxApps)]
	out.Apps = make([]struct {
		App      api.AppLabel `json:"app"`
		CpuPct   float32      `json:"cpuPct"`
		MemBytes int64        `json:"memBytes"`
	}, len(apps))
	for i, a := range apps {
		out.Apps[i].App, out.Apps[i].CpuPct, out.Apps[i].MemBytes = a.App, a.CpuPct, a.MemBytes
	}
	return out
}

func all(vs ...*int64) bool {
	for _, v := range vs {
		if v == nil {
			return false
		}
	}
	return true
}

// DockerNow is the snapshot of the latest live sample, with the disk figures of the latest sample that measured them.
func (s *Server) DockerNow(stack *string) *api.DockerSnapshot {
	if s.Runner == nil {
		return nil
	}
	e, ok := s.Runner.DockerLive.Latest()
	if !ok {
		return nil
	}
	b := dockerRows(e.V)
	if e.V.Disk == nil {
		if d, ok := latestDisk(s.now(), s.Runner.DockerLive, s.Runner.DockerHist); ok {
			b.DockerVM[0] = withDisk(b.DockerVM[0], dockerRows(d).DockerVM[0])
		}
	}
	out := dockerSnapshot(b, stack)
	if out != nil {
		setExitCodes(out.Containers, e.V.Containers)
	}
	if out != nil && out.Disk != nil {
		out.Disk.Stale = slices.Contains(e.V.Stale, collect.PartDisk)
	}
	return out
}

// setExitCodes reads the live sample, because the stored container rows have no exit code column.
func setExitCodes(out []api.ContainerNow, sample []collect.Container) {
	codes := make(map[string]*int32, len(sample))
	for _, c := range sample {
		codes[c.Name] = c.ExitCode
	}
	for i := range out {
		out[i].ExitCode = codes[out[i].Container]
	}
}

func latestDisk(now time.Time, rings ...*ring.Ring[collect.DockerSample]) (collect.DockerSample, bool) {
	for _, rg := range rings {
		w := rg.Window(now)
		for i := len(w) - 1; i >= 0; i-- {
			if w[i].V.Disk != nil {
				return w[i].V, true
			}
		}
	}
	return collect.DockerSample{}, false
}

func withDisk(vm, disk store.DockerVmSample) store.DockerVmSample {
	vm.VmDiskBytes, vm.DiskUsedBytes, vm.DiskReclaimableBytes = disk.VmDiskBytes, disk.DiskUsedBytes,
		disk.DiskReclaimableBytes
	vm.ImagesBytes, vm.ImagesCount, vm.ImagesUnused = disk.ImagesBytes, disk.ImagesCount, disk.ImagesUnused
	vm.ImagesReclaimableBytes, vm.ImagesLargestBytes = disk.ImagesReclaimableBytes, disk.ImagesLargestBytes
	vm.VolumesBytes, vm.VolumesCount, vm.VolumesUnused = disk.VolumesBytes, disk.VolumesCount, disk.VolumesUnused
	vm.VolumesReclaimableBytes = disk.VolumesReclaimableBytes
	vm.BuildCacheBytes, vm.BuildCacheReclaimableBytes = disk.BuildCacheBytes, disk.BuildCacheReclaimableBytes
	vm.At = disk.At
	return vm
}

// dockerDisk needs the sizes and counts; reclaimable and unused read 0 for rows stored before the collector measured
// them, because the spec has no null for them. The largest image is left out until it was read.
func dockerDisk(v store.DockerVmSample) *api.DockerDisk {
	if !all(v.DiskUsedBytes, v.ImagesBytes, v.VolumesBytes, v.BuildCacheBytes) || v.ImagesCount == nil ||
		v.VolumesCount == nil {
		return nil
	}
	d := &api.DockerDisk{MeasuredAt: v.At.Time.UTC(), UsedBytes: *v.DiskUsedBytes,
		ReclaimableBytes: deref(v.DiskReclaimableBytes), VmDiskBytes: v.VmDiskBytes}
	d.Images.Bytes, d.Images.Count, d.Images.Unused = *v.ImagesBytes, *v.ImagesCount, deref32(v.ImagesUnused)
	d.Images.ReclaimableBytes, d.Images.LargestBytes = deref(v.ImagesReclaimableBytes), v.ImagesLargestBytes
	d.Volumes.Bytes, d.Volumes.Count, d.Volumes.Unused = *v.VolumesBytes, *v.VolumesCount, deref32(v.VolumesUnused)
	d.Volumes.ReclaimableBytes = deref(v.VolumesReclaimableBytes)
	d.BuildCache.Bytes, d.BuildCache.ReclaimableBytes = *v.BuildCacheBytes, deref(v.BuildCacheReclaimableBytes)
	return d
}

func deref32(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

type stackNow = struct {
	Containers int32          `json:"containers"`
	MemBytes   int64          `json:"memBytes"`
	RxBps      int64          `json:"rxBps"`
	Stack      api.StackLabel `json:"stack"`
	TxBps      int64          `json:"txBps"`
}

func deref(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func dockerSnapshot(b store.SampleBatch, stack *string) *api.DockerSnapshot {
	if len(b.DockerVM) == 0 {
		return nil
	}
	vm := b.DockerVM[0]
	out := &api.DockerSnapshot{At: vm.At.Time.UTC(), Network: docker.StackNetwork, Containers: []api.ContainerNow{},
		Stacks: []stackNow{}, Disk: dockerDisk(vm)}
	out.Vm.CpuPct, out.Vm.MemTotalBytes, out.Vm.MemUsedBytes, out.Vm.SwapUsedBytes = vm.CpuPct, vm.MemTotalBytes,
		vm.MemUsedBytes, vm.SwapUsedBytes
	if vm.Cpus != nil {
		out.Vm.Cpus = ptr(int32(*vm.Cpus))
	}
	stacks := map[string]*stackNow{}
	for _, c := range b.Containers {
		if stack != nil && c.Stack != *stack {
			continue
		}
		out.Containers = append(out.Containers, api.ContainerNow{Container: c.Container, Stack: c.Stack,
			State: api.ContainerState(c.State), Health: api.ContainerHealth(c.Health), CpuPct: c.CpuPct,
			MemBytes: c.MemBytes, MemLimitBytes: c.MemLimitBytes, RxBps: c.RxBps, TxBps: c.TxBps,
			Restarts: c.Restarts, StartedAt: timePtr(c.StartedAt), MergedNames: c.MergedNames})
		n := max(1, c.MergedNames)
		out.Counts.Containers += n
		if c.State == store.ContainerStateRunning {
			out.Counts.Running += n
		}
		switch c.Health {
		case store.ContainerHealthHealthy:
			out.Counts.Healthy += n
		case store.ContainerHealthUnhealthy:
			out.Counts.Unhealthy += n
		case store.ContainerHealthStarting:
			out.Counts.Starting += n
		default:
			out.Counts.NoHealthcheck += n
		}
		st := stacks[c.Stack]
		if st == nil {
			st = &stackNow{Stack: c.Stack}
			stacks[c.Stack] = st
		}
		st.Containers += n
		st.MemBytes += deref(c.MemBytes)
		st.RxBps += deref(c.RxBps)
		st.TxBps += deref(c.TxBps)
	}
	for _, st := range stacks {
		out.Stacks = append(out.Stacks, *st)
	}
	sort.Slice(out.Stacks, func(i, j int) bool { return out.Stacks[i].Stack < out.Stacks[j].Stack })
	return out
}

func badRange() error { return &api.PeriodError{Code: api.ErrorResponseCodeBadRange} }

type span struct {
	since, until time.Time
	res          api.Resolution
	interval     time.Duration
	live, memory bool
}

func (s *Server) metricsSpan(r *api.MetricsRange) (span, error) {
	rng := api.MetricsRangeLive
	if r != nil {
		rng = *r
	}
	paused := historyPaused(s.Runner)
	d, res, ok := api.MetricsWindow(rng, paused)
	if !ok {
		return span{}, badRange()
	}
	now := s.now()
	sp := span{since: now.Add(-d), until: now, res: res, live: rng == api.MetricsRangeLive, memory: paused}
	o := runnerOptions(s)
	sp.interval = o.Hist
	if sp.live {
		sp.interval, sp.memory = o.Live, true
	}
	return sp, nil
}

// fromMemory is the span narrowed to what the rings hold, for a database that stopped answering mid-request.
func fromMemory(sp span, rng api.MetricsRange) (span, bool) {
	d, res, ok := api.MetricsWindow(rng, true)
	if !ok {
		return sp, false
	}
	sp.since, sp.res, sp.memory = sp.until.Add(-d), res, true
	return sp, true
}

// step is the spacing of points at a resolution; a series starting within one step of the range start is complete.
func (sp span) step() time.Duration {
	switch sp.res {
	case api.ResolutionN1m:
		return time.Minute
	case api.ResolutionN1h:
		return time.Hour
	}
	return sp.interval
}

func (sp span) coverage(first *time.Time) (*time.Time, bool) {
	if first == nil {
		return nil, true
	}
	return first, sp.memory && !sp.live || first.After(sp.since.Add(sp.step()+sp.step()/2))
}

func runnerOptions(s *Server) struct{ Live, Hist time.Duration } {
	if s.Runner == nil {
		return struct{ Live, Hist time.Duration }{runner.DefaultLive, runner.DefaultHist}
	}
	o := s.Runner.Options()
	return struct{ Live, Hist time.Duration }{o.Live, o.Hist}
}

func (s *Server) GetMachine(ctx context.Context, r api.GetMachineRequestObject) (api.GetMachineResponseObject, error) {
	out, err := s.machine(ctx, r.Params)
	if err == nil {
		return api.GetMachine200JSONResponse(out), nil
	}
	if st, code, ok := failure(err); ok && st == 400 {
		return api.GetMachine400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse{Code: code}}, nil
	}
	if unavailable(err) {
		logUnavailable("/api/machine", err)
		return api.GetMachinedefaultJSONResponse{StatusCode: http.StatusServiceUnavailable,
			Body: api.ErrorResponse{Code: api.ErrorResponseCodeUnavailable}}, nil
	}
	return nil, err
}

type corePoint = struct {
	At      time.Time `json:"at"`
	BusyPct float32   `json:"busyPct"`
}

type coreSeries = struct {
	Core   int32        `json:"core"`
	Kind   api.CoreKind `json:"kind"`
	Points []corePoint  `json:"points"`
}

type ifacePoint = struct {
	At    time.Time `json:"at"`
	RxBps int64     `json:"rxBps"`
	TxBps int64     `json:"txBps"`
}

type ifaceSeries = struct {
	Interface api.InterfaceLabel `json:"interface"`
	PeakRxAt  *time.Time         `json:"peakRxAt"`
	PeakRxBps int64              `json:"peakRxBps"`
	Points    []ifacePoint       `json:"points"`
}

type machineSeries struct {
	points []api.MachinePoint
	cores  map[int32]*coreSeries
	ifaces map[string]*ifaceSeries
}

func newMachineSeries() *machineSeries {
	return &machineSeries{points: []api.MachinePoint{}, cores: map[int32]*coreSeries{}, ifaces: map[string]*ifaceSeries{}}
}

func (m *machineSeries) core(at time.Time, core int16, kind store.CoreKind, busy float32) {
	c := m.cores[int32(core)]
	if c == nil {
		c = &coreSeries{Core: int32(core), Kind: api.CoreKind(kind), Points: []corePoint{}}
		m.cores[int32(core)] = c
	}
	c.Points = append(c.Points, corePoint{At: at.UTC(), BusyPct: busy})
}

func (m *machineSeries) iface(at time.Time, name string, rx, tx, peak int64) {
	n := m.ifaces[name]
	if n == nil {
		n = &ifaceSeries{Interface: name, Points: []ifacePoint{}}
		m.ifaces[name] = n
	}
	n.Points = append(n.Points, ifacePoint{At: at.UTC(), RxBps: rx, TxBps: tx})
	if n.PeakRxAt == nil || peak > n.PeakRxBps {
		n.PeakRxBps, n.PeakRxAt = peak, ptr(at.UTC())
	}
}

func (s *Server) machine(ctx context.Context, prm api.GetMachineParams) (api.MachineResponse, error) {
	sp, err := s.metricsSpan(prm.Range)
	if err != nil {
		return api.MachineResponse{}, err
	}
	rng := api.MetricsRangeLive
	if prm.Range != nil {
		rng = *prm.Range
	}
	m := newMachineSeries()
	lost := false
	if !sp.memory {
		if err := s.machineFromDB(ctx, sp, m); err != nil {
			if sp, lost = fromMemory(sp, rng); !lost || !unavailable(err) {
				return api.MachineResponse{}, err
			}
			m = newMachineSeries()
		}
	}
	if sp.memory && s.Runner != nil {
		src := s.Runner.MachineHist
		if sp.live {
			src = s.Runner.MachineLive
		}
		m.fromRing(src.Window(sp.until), sp.since)
	}
	out := api.MachineResponse{Range: rng, Resolution: sp.res, DetailIntervalSeconds: int32(sp.interval / time.Second),
		HistoryPaused: lost || historyPaused(s.Runner), Now: s.MachineNow(), Series: m.points,
		CoreSeries: []coreSeries{}, InterfaceSeries: []ifaceSeries{}}
	var first *time.Time
	if len(m.points) > 0 {
		first = &m.points[0].At
	}
	out.CoverageFrom, out.Partial = sp.coverage(first)
	out.Partial = out.Partial || out.HistoryPaused
	for _, c := range m.cores {
		out.CoreSeries = append(out.CoreSeries, *c)
	}
	sort.Slice(out.CoreSeries, func(i, j int) bool { return out.CoreSeries[i].Core < out.CoreSeries[j].Core })
	for _, n := range m.ifaces {
		out.InterfaceSeries = append(out.InterfaceSeries, *n)
	}
	sort.Slice(out.InterfaceSeries, func(i, j int) bool {
		return out.InterfaceSeries[i].Interface < out.InterfaceSeries[j].Interface
	})
	return out, nil
}

func (m *machineSeries) fromRing(entries []ring.Entry[collect.MachineSample], since time.Time) {
	for _, e := range entries {
		if e.At.Before(since) {
			continue
		}
		b := machineRows(e.V)
		if len(b.Machine) == 0 {
			continue
		}
		x := b.Machine[0]
		m.points = append(m.points, api.MachinePoint{At: x.At.Time.UTC(), CpuBusyPct: x.CpuBusyPct,
			CpuBusyMaxPct: x.CpuBusyPct, Load1: x.Load1, MemUsedBytes: x.MemUsedBytes, SwapUsedBytes: x.SwapUsedBytes,
			DiskReadBps: x.DiskReadBps, DiskWriteBps: x.DiskWriteBps})
		for _, c := range b.Cores {
			m.core(c.At.Time, c.Core, c.Kind, c.BusyPct)
		}
		for _, n := range b.Interfaces {
			m.iface(n.At.Time, n.Interface, n.RxBps, n.TxBps, n.RxBps)
		}
	}
}

func (s *Server) machineFromDB(ctx context.Context, sp span, m *machineSeries) error {
	if s.DB == nil {
		return errNoDB
	}
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	q := s.q()
	since, until := ts(sp.since), ts(sp.until)
	switch sp.res {
	case api.ResolutionRaw:
		rows, err := q.MachineSeriesRaw(ctx, store.MachineSeriesRawParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, x := range rows {
			m.points = append(m.points, api.MachinePoint{At: x.At.Time.UTC(), CpuBusyPct: x.CpuBusyPct,
				CpuBusyMaxPct: x.CpuBusyPct, Load1: x.Load1, MemUsedBytes: x.MemUsedBytes,
				SwapUsedBytes: x.SwapUsedBytes, DiskReadBps: x.DiskReadBps, DiskWriteBps: x.DiskWriteBps})
		}
		cores, err := q.CoreSeriesRaw(ctx, store.CoreSeriesRawParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, c := range cores {
			m.core(c.At.Time, c.Core, c.Kind, c.BusyPct)
		}
		ifaces, err := q.InterfaceSeriesRaw(ctx, store.InterfaceSeriesRawParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, n := range ifaces {
			m.iface(n.At.Time, n.Interface, n.RxBps, n.TxBps, n.RxBps)
		}
	case api.ResolutionN1m:
		rows, err := q.MachineSeries1m(ctx, store.MachineSeries1mParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, x := range rows {
			m.points = append(m.points, rollupPoint(x.Bucket, x.CpuBusyAvg, x.CpuBusyMax, x.Load1Avg, x.MemUsedAvg,
				x.SwapUsedMax, x.DiskReadAvg, x.DiskWriteAvg))
		}
		cores, err := q.CoreSeries1m(ctx, store.CoreSeries1mParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, c := range cores {
			m.core(c.Bucket.Time, c.Core, c.Kind, c.BusyAvg)
		}
		ifaces, err := q.InterfaceSeries1m(ctx, store.InterfaceSeries1mParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, n := range ifaces {
			m.iface(n.Bucket.Time, n.Interface, n.RxAvg, n.TxAvg, n.RxMax)
		}
	default:
		rows, err := q.MachineSeries1h(ctx, store.MachineSeries1hParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, x := range rows {
			m.points = append(m.points, rollupPoint(x.Bucket, x.CpuBusyAvg, x.CpuBusyMax, x.Load1Avg, x.MemUsedAvg,
				x.SwapUsedMax, x.DiskReadAvg, x.DiskWriteAvg))
		}
		cores, err := q.CoreSeries1h(ctx, store.CoreSeries1hParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, c := range cores {
			m.core(c.Bucket.Time, c.Core, c.Kind, c.BusyAvg)
		}
		ifaces, err := q.InterfaceSeries1h(ctx, store.InterfaceSeries1hParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, n := range ifaces {
			m.iface(n.Bucket.Time, n.Interface, n.RxAvg, n.TxAvg, n.RxMax)
		}
	}
	return nil
}

func rollupPoint(at pgtype.Timestamptz, cpu, cpuMax, load *float32, mem, swap, rd, wr *int64) api.MachinePoint {
	return api.MachinePoint{At: at.Time.UTC(), CpuBusyPct: cpu, CpuBusyMaxPct: cpuMax, Load1: load, MemUsedBytes: mem,
		SwapUsedBytes: swap, DiskReadBps: rd, DiskWriteBps: wr}
}

func (s *Server) GetDocker(ctx context.Context, r api.GetDockerRequestObject) (api.GetDockerResponseObject, error) {
	out, err := s.docker(ctx, r.Params)
	if err == nil {
		return api.GetDocker200JSONResponse(out), nil
	}
	if st, code, ok := failure(err); ok && st == 400 {
		return api.GetDocker400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse{Code: code}}, nil
	}
	if unavailable(err) {
		logUnavailable("/api/docker", err)
		return api.GetDockerdefaultJSONResponse{StatusCode: http.StatusServiceUnavailable,
			Body: api.ErrorResponse{Code: api.ErrorResponseCodeUnavailable}}, nil
	}
	return nil, err
}

type vmPoint = struct {
	At           time.Time `json:"at"`
	CpuPct       *float32  `json:"cpuPct"`
	MemUsedBytes *int64    `json:"memUsedBytes"`
}

type containerPoint = struct {
	At       time.Time `json:"at"`
	CpuPct   *float32  `json:"cpuPct"`
	MemBytes *int64    `json:"memBytes"`
	RxBps    *int64    `json:"rxBps"`
	TxBps    *int64    `json:"txBps"`
}

type containerSeries = struct {
	Container api.ContainerLabel `json:"container"`
	Points    []containerPoint   `json:"points"`
	Stack     api.StackLabel     `json:"stack"`
}

type dockerSeries struct {
	vm         []vmPoint
	containers map[[2]string]*containerSeries
	events     []api.DockerEvent
}

func (d *dockerSeries) container(name, stack string, p containerPoint) {
	k := [2]string{stack, name}
	c := d.containers[k]
	if c == nil {
		c = &containerSeries{Container: name, Stack: stack, Points: []containerPoint{}}
		d.containers[k] = c
	}
	p.At = p.At.UTC()
	c.Points = append(c.Points, p)
}

func (s *Server) docker(ctx context.Context, prm api.GetDockerParams) (api.DockerResponse, error) {
	sp, err := s.metricsSpan(prm.Range)
	if err != nil {
		return api.DockerResponse{}, err
	}
	rng := api.MetricsRangeLive
	if prm.Range != nil {
		rng = *prm.Range
	}
	d := &dockerSeries{vm: []vmPoint{}, containers: map[[2]string]*containerSeries{}, events: []api.DockerEvent{}}
	eventsSince := sp.since
	if sp.live {
		eventsSince = sp.until.Add(-24 * time.Hour)
	}
	lost := false
	if !sp.memory {
		if err := s.dockerFromDB(ctx, sp, prm.Stack, eventsSince, d); err != nil {
			if sp, lost = fromMemory(sp, rng); !lost || !unavailable(err) {
				return api.DockerResponse{}, err
			}
			d = &dockerSeries{vm: []vmPoint{}, containers: map[[2]string]*containerSeries{}, events: []api.DockerEvent{}}
			eventsSince = sp.since
		}
	}
	if sp.memory {
		s.dockerFromMemory(sp, prm.Stack, eventsSince, d)
	}
	out := api.DockerResponse{Range: rng, Resolution: sp.res, DetailIntervalSeconds: int32(sp.interval / time.Second),
		HistoryPaused: lost || historyPaused(s.Runner), Now: s.DockerNow(prm.Stack), VmSeries: d.vm,
		ContainerSeries: []containerSeries{}, Events: d.events}
	if s.Runner != nil {
		st := s.Runner.Status.Get(collect.SourceDocker).State
		out.Available = out.Now != nil && st != collect.StateUnavailable
	}
	var first *time.Time
	if len(d.vm) > 0 {
		first = &d.vm[0].At
	}
	out.CoverageFrom, out.Partial = sp.coverage(first)
	out.Partial = out.Partial || out.HistoryPaused
	for _, c := range d.containers {
		out.ContainerSeries = append(out.ContainerSeries, *c)
	}
	sort.Slice(out.ContainerSeries, func(i, j int) bool {
		a, b := out.ContainerSeries[i], out.ContainerSeries[j]
		return a.Stack < b.Stack || a.Stack == b.Stack && a.Container < b.Container
	})
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].At.After(out.Events[j].At) })
	out.Events = out.Events[:min(len(out.Events), maxEvents)]
	return out, nil
}

func (s *Server) dockerFromMemory(sp span, stack *string, eventsSince time.Time, d *dockerSeries) {
	if s.Runner == nil {
		return
	}
	src := s.Runner.DockerHist
	if sp.live {
		src = s.Runner.DockerLive
	}
	inStack := map[string]bool{}
	for _, e := range src.Window(sp.until) {
		if e.At.Before(sp.since) {
			continue
		}
		b := dockerRows(e.V)
		if len(b.DockerVM) == 0 {
			continue
		}
		vm := b.DockerVM[0]
		d.vm = append(d.vm, vmPoint{At: vm.At.Time.UTC(), CpuPct: vm.CpuPct, MemUsedBytes: vm.MemUsedBytes})
		for _, c := range b.Containers {
			if stack != nil && c.Stack != *stack {
				continue
			}
			inStack[c.Container] = true
			d.container(c.Container, c.Stack, containerPoint{At: c.At.Time, CpuPct: c.CpuPct, MemBytes: c.MemBytes,
				RxBps: c.RxBps, TxBps: c.TxBps})
		}
	}
	b := history.Convert(collect.Batch{Events: eventsOf(s.Runner.Events.Window(sp.until))})
	for _, e := range b.Events {
		if e.At.Time.Before(eventsSince) || stack != nil && !inStack[e.Container] {
			continue
		}
		d.events = append(d.events, api.DockerEvent{At: e.At.Time.UTC(), Container: e.Container,
			Kind: api.DockerEventKind(e.Kind)})
	}
}

func eventsOf(entries []ring.Entry[collect.DockerEvent]) []collect.DockerEvent {
	out := make([]collect.DockerEvent, len(entries))
	for i, e := range entries {
		out[i] = e.V
	}
	return out
}

func (s *Server) dockerFromDB(ctx context.Context, sp span, stack *string, eventsSince time.Time,
	d *dockerSeries) error {
	if s.DB == nil {
		return errNoDB
	}
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	q := s.q()
	since, until := ts(sp.since), ts(sp.until)
	switch sp.res {
	case api.ResolutionRaw:
		vm, err := q.DockerVmSeriesRaw(ctx, store.DockerVmSeriesRawParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, x := range vm {
			d.vm = append(d.vm, vmPoint{At: x.At.Time.UTC(), CpuPct: x.CpuPct, MemUsedBytes: x.MemUsedBytes})
		}
		cs, err := q.ContainerSeriesRaw(ctx, store.ContainerSeriesRawParams{Since: since, Until: until, Stack: stack})
		if err != nil {
			return err
		}
		for _, c := range cs {
			d.container(c.Container, c.Stack, containerPoint{At: c.At.Time, CpuPct: c.CpuPct, MemBytes: c.MemBytes,
				RxBps: c.RxBps, TxBps: c.TxBps})
		}
	case api.ResolutionN1m:
		vm, err := q.DockerVmSeries1m(ctx, store.DockerVmSeries1mParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, x := range vm {
			d.vm = append(d.vm, vmPoint{At: x.Bucket.Time.UTC(), CpuPct: x.CpuAvg, MemUsedBytes: x.MemUsedAvg})
		}
		cs, err := q.ContainerSeries1m(ctx, store.ContainerSeries1mParams{Since: since, Until: until, Stack: stack})
		if err != nil {
			return err
		}
		for _, c := range cs {
			d.container(c.Container, c.Stack, containerPoint{At: c.Bucket.Time, CpuPct: c.CpuAvg, MemBytes: c.MemAvg,
				RxBps: c.RxAvg, TxBps: c.TxAvg})
		}
	default:
		vm, err := q.DockerVmSeries1h(ctx, store.DockerVmSeries1hParams{Since: since, Until: until})
		if err != nil {
			return err
		}
		for _, x := range vm {
			d.vm = append(d.vm, vmPoint{At: x.Bucket.Time.UTC(), CpuPct: x.CpuAvg, MemUsedBytes: x.MemUsedAvg})
		}
		cs, err := q.ContainerSeries1h(ctx, store.ContainerSeries1hParams{Since: since, Until: until, Stack: stack})
		if err != nil {
			return err
		}
		for _, c := range cs {
			d.container(c.Container, c.Stack, containerPoint{At: c.Bucket.Time, CpuPct: c.CpuAvg, MemBytes: c.MemAvg,
				RxBps: c.RxAvg, TxBps: c.TxAvg})
		}
	}
	events, err := q.DockerEvents(ctx, store.DockerEventsParams{Since: ts(eventsSince), Until: until, Stack: stack,
		MaxRows: maxEvents})
	if err != nil {
		return err
	}
	for _, e := range events {
		d.events = append(d.events, api.DockerEvent{At: e.At.Time.UTC(), Container: e.Container,
			Kind: api.DockerEventKind(e.Kind)})
	}
	return nil
}
