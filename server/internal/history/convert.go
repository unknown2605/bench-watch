// Package history writes collector samples into PostgreSQL and keeps the rollups and partitions current.
package history

import (
	"math"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/store"
)

// The same patterns as the schema's is_*_label checks, so one bad name drops its row instead of the batch.
var (
	containerLabel  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	stackLabel      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	displayLabel    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()+&-]{0,63}$`)
	filesystemLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,15}$`)
	interfaceLabel  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()+-]{0,31}$`)
)

var containerStates = map[string]store.ContainerState{
	"created": store.ContainerStateCreated, "running": store.ContainerStateRunning,
	"paused": store.ContainerStatePaused, "restarting": store.ContainerStateRestarting,
	"removing": store.ContainerStateRemoving, "exited": store.ContainerStateExited, "dead": store.ContainerStateDead,
}

var containerHealth = map[string]store.ContainerHealth{
	"healthy": store.ContainerHealthHealthy, "unhealthy": store.ContainerHealthUnhealthy,
	"starting": store.ContainerHealthStarting,
}

var eventKinds = map[string]store.DockerEventKind{}

func init() {
	for _, k := range []store.DockerEventKind{store.DockerEventKindCreate, store.DockerEventKindStart,
		store.DockerEventKindRestart, store.DockerEventKindStop, store.DockerEventKindDie, store.DockerEventKindKill,
		store.DockerEventKindOom, store.DockerEventKindPause, store.DockerEventKindUnpause,
		store.DockerEventKindDestroy, store.DockerEventKindRename, store.DockerEventKindUpdate,
		store.DockerEventKindHealthHealthy, store.DockerEventKindHealthUnhealthy,
		store.DockerEventKindHealthStarting, store.DockerEventKindHealth, store.DockerEventKindOther} {
		eventKinds[string(k)] = k
	}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func ptr[T any](v T) *T { return &v }

func f32(v float64) float32 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return float32(v)
}

func pct(v float64) float32 { return min(f32(v), 100) }

func i64(v float64) int64 {
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	if v >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(math.Round(v))
}

func u64(v uint64) int64 { return int64(min(v, math.MaxInt64)) }

func merged(name string, count int) int32 {
	if name == collect.Other && count > 0 {
		return int32(min(count, math.MaxInt32))
	}
	return 0
}

// Convert maps one collector batch onto the schema; rows whose names the schema would refuse are left out.
func Convert(b collect.Batch) store.SampleBatch {
	var out store.SampleBatch
	for _, s := range b.Machine {
		addMachine(&out, s)
	}
	for _, s := range b.Docker {
		addDocker(&out, s)
	}
	for _, e := range b.Events {
		kind, ok := eventKinds[e.Kind]
		if !ok || !containerLabel.MatchString(e.Name) {
			continue
		}
		out.Events = append(out.Events, store.DockerEvent{At: ts(e.At), Container: e.Name, Kind: kind})
	}
	return out
}

func addMachine(out *store.SampleBatch, s collect.MachineSample) {
	at := ts(s.At)
	row := store.MachineSample{At: at}
	var busy float64
	n := 0
	for _, c := range s.Cores {
		var kind store.CoreKind
		switch c.Kind {
		case collect.CoreP:
			kind = store.CoreKindPerformance
		case collect.CoreE:
			kind = store.CoreKindEfficiency
		}
		busy += c.Busy
		n++
		if kind == "" || c.Index < 0 || c.Index > 1023 {
			continue
		}
		out.Cores = append(out.Cores, store.MachineCoreSample{At: at, Core: int16(c.Index), Kind: kind, BusyPct: pct(c.Busy)})
	}
	if n > 0 {
		row.CpuBusyPct = ptr(pct(busy / float64(n)))
	}
	if s.Load != nil {
		row.Load1, row.Load5, row.Load15 = ptr(f32(s.Load.One)), ptr(f32(s.Load.Five)), ptr(f32(s.Load.Fifteen))
	}
	// The schema keeps memory and swap as one group, so a sample without swap stores no memory figures.
	if m := s.Memory; m != nil && !s.Lacks(collect.PartSwap) {
		row.MemTotalBytes, row.MemUsedBytes, row.MemAppBytes = ptr(u64(m.Total)), ptr(u64(m.Used)), ptr(u64(m.App))
		row.MemCachedBytes, row.MemCompressedBytes = ptr(u64(m.Cached)), ptr(u64(m.Compressed))
		row.SwapUsedBytes, row.SwapTotalBytes = ptr(u64(m.SwapUsed)), ptr(u64(m.SwapTotal))
	}
	if !s.Lacks(collect.PartDiskIO) {
		var r, w float64
		for _, d := range s.DiskIO {
			r += d.ReadBps
			w += d.WriteBps
		}
		row.DiskReadBps, row.DiskWriteBps = ptr(i64(r)), ptr(i64(w))
	}
	out.Machine = append(out.Machine, row)
	for _, v := range s.Volumes {
		if !displayLabel.MatchString(v.Name) {
			continue
		}
		row := store.MachineVolumeSample{At: at, Volume: v.Name, TotalBytes: u64(v.Total), FreeBytes: u64(v.Free),
			MergedNames: merged(v.Name, v.Count)}
		if v.Kind == string(store.VolumeKindInternal) {
			row.Kind = ptr(store.VolumeKindInternal)
		}
		if filesystemLabel.MatchString(v.Filesystem) {
			row.Filesystem = ptr(v.Filesystem)
		}
		out.Volumes = append(out.Volumes, row)
	}
	for _, i := range s.Net {
		if !interfaceLabel.MatchString(i.Name) {
			continue
		}
		out.Interfaces = append(out.Interfaces, store.MachineInterfaceSample{At: at, Interface: i.Name,
			RxBps: i64(i.RxBps), TxBps: i64(i.TxBps), MergedNames: merged(i.Name, i.Count)})
	}
	seen := map[string]bool{}
	for _, a := range append(append([]collect.App(nil), s.TopCPU...), s.TopMem...) {
		if seen[a.Name] || !displayLabel.MatchString(a.Name) {
			continue
		}
		seen[a.Name] = true
		out.Apps = append(out.Apps, store.MachineAppSample{At: at, App: a.Name, CpuPct: f32(a.CPU), MemBytes: u64(a.RSS)})
	}
}

func addDocker(out *store.SampleBatch, s collect.DockerSample) {
	at := ts(s.At)
	vm := store.DockerVmSample{At: at}
	if !s.Lacks(collect.PartInfo) {
		vm.Cpus = ptr(int16(min(max(s.VM.NCPU, 0), math.MaxInt16)))
		vm.MemTotalBytes = ptr(u64(s.VM.MemTotal))
	}
	vm.MemUsedBytes, vm.CpuPct = ptr(u64(s.VM.MemUsed)), ptr(f32(s.VM.CPU))
	if s.VM.SwapUsed != nil {
		vm.SwapUsedBytes = ptr(u64(*s.VM.SwapUsed))
	}
	if d := s.Disk; d != nil {
		clamp := func(v int64) int64 { return max(v, 0) }
		vm.ImagesBytes, vm.ImagesCount = ptr(clamp(d.Images)), ptr(int32(min(clamp(d.ImageCount), math.MaxInt32)))
		vm.VolumesBytes, vm.VolumesCount = ptr(clamp(d.Volumes)), ptr(int32(min(clamp(d.VolumeCount), math.MaxInt32)))
		vm.BuildCacheBytes = ptr(clamp(d.BuildCache))
		vm.DiskReclaimableBytes = ptr(clamp(d.Reclaimable))
		vm.ImagesReclaimableBytes = ptr(clamp(d.ImagesReclaimable))
		if d.LargestImage != nil {
			vm.ImagesLargestBytes = ptr(clamp(*d.LargestImage))
		}
		vm.ImagesUnused = ptr(int32(min(clamp(d.ImagesUnused), math.MaxInt32)))
		vm.VolumesReclaimableBytes = ptr(clamp(d.VolumesReclaimable))
		vm.VolumesUnused = ptr(int32(min(clamp(d.VolumesUnused), math.MaxInt32)))
		vm.BuildCacheReclaimableBytes = ptr(clamp(d.BuildCacheReclaimable))
		vm.DiskUsedBytes = ptr(clamp(d.Images) + clamp(d.Volumes) + clamp(d.BuildCache) + clamp(d.Containers))
	}
	for _, c := range s.Containers {
		n := int32(1)
		if c.Name == collect.Other && c.Count > 0 {
			n = int32(min(c.Count, math.MaxInt32))
		}
		vm.Containers += n
		if c.State == "running" {
			vm.Running += n
		}
		state, ok := containerStates[c.State]
		if !ok || !containerLabel.MatchString(c.Name) {
			continue
		}
		stack := c.Stack
		if stack == "" {
			stack = "none"
		}
		if !stackLabel.MatchString(stack) {
			continue
		}
		health, ok := containerHealth[c.Health]
		if !ok {
			health = store.ContainerHealthNone
		}
		row := store.DockerContainerSample{At: at, Container: c.Name, Stack: stack, State: state, Health: health,
			Restarts: int32(min(max(c.Restarts, 0), math.MaxInt32)), MergedNames: merged(c.Name, c.Count)}
		lacksStats := contains(c.Missing, collect.PartStats)
		if !lacksStats {
			row.MemBytes = ptr(u64(c.MemUsed))
			if c.MemLimit > 0 {
				row.MemLimitBytes = ptr(u64(c.MemLimit))
			}
			if !contains(c.Missing, collect.PartRates) {
				row.CpuPct, row.RxBps, row.TxBps = ptr(f32(c.CPU)), ptr(i64(c.RxBps)), ptr(i64(c.TxBps))
			}
		}
		if state == store.ContainerStateRunning && c.UptimeSec > 0 && c.Name != collect.Other {
			row.StartedAt = ts(s.At.Add(-time.Duration(c.UptimeSec) * time.Second).Truncate(time.Second))
		}
		out.Containers = append(out.Containers, row)
	}
	out.DockerVM = append(out.DockerVM, vm)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
