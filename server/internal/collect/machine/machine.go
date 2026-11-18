// Package machine samples this Mac: per-core CPU, load, memory, volumes, disk and network rates, busiest apps.
package machine

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"benchwatch/server/internal/collect"
)

type CPUTimes struct {
	Busy  float64
	Total float64
}

// IOCount is a cumulative counter pair: read/write for disks, receive/transmit for interfaces.
type IOCount struct {
	Name     string
	InBytes  uint64
	OutBytes uint64
	InOps    uint64
	OutOps   uint64
}

// Proc holds the process name only; Start tells a reused PID from the process that had it before.
type Proc struct {
	PID    int32
	Start  int64
	Name   string
	CPUSec float64
	RSS    uint64
}

// ItemError from a Source call means the items it returned are good but some could not be read; Part, when
// set, names the piece that failed (PartSwap for Memory) instead of the call's own part.
type ItemError struct {
	Part string
	Err  error
}

func (e ItemError) Error() string { return "some items failed: " + e.Err.Error() }
func (e ItemError) Unwrap() error { return e.Err }

// Source is the operating system boundary, faked in tests. Names it returns are raw; the Collector gates them.
type Source interface {
	CoreKinds(ctx context.Context, ncpu int) ([]collect.CoreKind, string)
	CPUTimes(ctx context.Context) ([]CPUTimes, error)
	Load(ctx context.Context) (collect.Load, error)
	Memory(ctx context.Context) (collect.Memory, error)
	Volumes(ctx context.Context) ([]collect.Volume, error)
	DiskIO(ctx context.Context) ([]IOCount, error)
	NetIO(ctx context.Context) ([]IOCount, error)
	Procs(ctx context.Context) ([]Proc, error)
}

// Snapshot holds raw names and never leaves the process. Load and Memory are nil when they failed,
// Procs is nil when skipped or failed, and Partial names every part that failed.
type Snapshot struct {
	At      time.Time
	CPU     []CPUTimes
	Load    *collect.Load
	Memory  *collect.Memory
	Volumes []collect.Volume
	Disk    []IOCount
	Net     []IOCount
	Procs   []Proc
	Partial []collect.PartError
}

func (s Snapshot) failed(part string) bool {
	for _, p := range s.Partial {
		if p.Part == part {
			return true
		}
	}
	return false
}

type Options struct {
	Now  func() time.Time
	Gate collect.NameGate
	TopN int
}

type Collector struct {
	src  Source
	now  func() time.Time
	gate collect.NameGate
	topN int

	mu         sync.Mutex
	kinds      []collect.CoreKind
	kindSource string
}

func New(src Source, opt Options) *Collector {
	if opt.Now == nil {
		opt.Now = collect.RealClock().Now
	}
	if opt.Gate == nil {
		opt.Gate = collect.DefaultNameGate
	}
	if opt.TopN <= 0 {
		opt.TopN = 10
	}
	opt.TopN = min(opt.TopN, collect.MaxRows)
	return &Collector{src: src, now: opt.Now, gate: opt.Gate, topN: opt.TopN}
}

// The second result is the evidence for the P/E mapping: "ioreg", "sysctl-order" or "none".
func (c *Collector) Kinds() ([]collect.CoreKind, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.kinds), c.kindSource
}

// Snapshot fails only when CPU times fail; any other part that fails is listed in Partial.
func (c *Collector) Snapshot(ctx context.Context, withProcs bool) (Snapshot, error) {
	s := Snapshot{At: c.now().UTC()}
	var err error
	if s.CPU, err = c.src.CPUTimes(ctx); err != nil {
		return Snapshot{}, err
	}
	c.mu.Lock()
	stale := len(c.kinds) != len(s.CPU)
	c.mu.Unlock()
	if stale {
		kinds, source := c.src.CoreKinds(ctx, len(s.CPU))
		c.mu.Lock()
		c.kinds, c.kindSource = kinds, source
		c.mu.Unlock()
	}
	note := func(part string, e error) bool {
		var ie ItemError
		if errors.As(e, &ie) {
			if ie.Part != "" {
				part = ie.Part
			}
			e = ie.Err
		}
		if e != nil {
			s.Partial = append(s.Partial, collect.NewPartError(part, e))
		}
		return e == nil || ie.Err != nil
	}
	if l, e := c.src.Load(ctx); note(collect.PartLoad, e) {
		s.Load = &l
	}
	if m, e := c.src.Memory(ctx); note(collect.PartMemory, e) {
		s.Memory = &m
	}
	if v, e := c.src.Volumes(ctx); note(collect.PartVolumes, e) {
		s.Volumes = v
	}
	if d, e := c.src.DiskIO(ctx); note(collect.PartDiskIO, e) {
		s.Disk = d
	}
	if n, e := c.src.NetIO(ctx); note(collect.PartNet, e) {
		s.Net = n
	}
	if withProcs {
		if p, e := c.src.Procs(ctx); note(collect.PartApps, e) {
			s.Procs = p
			if s.Procs == nil {
				s.Procs = []Proc{}
			}
		}
	}
	return s, nil
}

// Derive turns two snapshots into rates over the time between them. A part that failed in either
// snapshot is listed in Missing and left empty.
func (c *Collector) Derive(prev, cur Snapshot) collect.MachineSample {
	dt := cur.At.Sub(prev.At).Seconds()
	out := collect.MachineSample{At: cur.At, Interval: dt, Load: cur.Load, Memory: cur.Memory}
	missing := func(part string, needPrev bool) bool {
		if cur.failed(part) || (needPrev && prev.failed(part)) {
			out.Missing = append(out.Missing, part)
			return true
		}
		return false
	}
	missing(collect.PartLoad, false)
	missing(collect.PartMemory, false)
	missing(collect.PartSwap, false)
	missing(collect.PartVolumes, false)
	out.Volumes = c.volumes(cur.Volumes)
	c.mu.Lock()
	kinds := c.kinds
	c.mu.Unlock()
	out.Cores = make([]collect.Core, len(cur.CPU))
	for i, t := range cur.CPU {
		kind := collect.CoreUnknown
		if i < len(kinds) {
			kind = kinds[i]
		}
		out.Cores[i] = collect.Core{Index: i, Kind: kind}
		if i < len(prev.CPU) {
			db, dtot := t.Busy-prev.CPU[i].Busy, t.Total-prev.CPU[i].Total
			if dtot > 0 && db >= 0 {
				out.Cores[i].Busy = round1(min(100, 100*db/dtot))
			}
		}
	}
	if dt <= 0 {
		return out
	}
	missing(collect.PartDiskIO, true)
	for _, r := range c.rates(prev.Disk, cur.Disk, dt) {
		out.DiskIO = append(out.DiskIO, collect.DiskIO{Name: r.name, ReadBps: r.in, WriteBps: r.out,
			ReadOps: r.inOps, WriteOps: r.outOps, Count: r.count})
	}
	missing(collect.PartNet, true)
	for _, r := range c.rates(prev.Net, cur.Net, dt) {
		out.Net = append(out.Net, collect.NetIO{Name: r.name, RxBps: r.in, TxBps: r.out, Count: r.count})
	}
	if !missing(collect.PartApps, true) && cur.Procs != nil && prev.Procs != nil {
		out.TopCPU, out.TopMem, out.DockerVM = c.topApps(prev, cur, dt)
		out.AppsWindow = dt
	}
	return out
}

func (c *Collector) Apps(prev, cur Snapshot) (byCPU, byMem []collect.App, window float64, ok bool) {
	window = cur.At.Sub(prev.At).Seconds()
	if prev.Procs == nil || cur.Procs == nil || window <= 0 {
		return nil, nil, 0, false
	}
	byCPU, byMem, _ = c.topApps(prev, cur, window)
	return byCPU, byMem, window, true
}

func (c *Collector) volumes(raw []collect.Volume) []collect.Volume {
	var out []collect.Volume
	var other *collect.Volume
	taken := map[string]bool{}
	for _, v := range raw {
		taken[c.gate.Name(v.Name)] = true
	}
	for _, v := range raw {
		name := c.gate.Name(v.Name)
		if name == collect.Other && v.Kind == "internal" && !taken["root"] {
			name = "root"
			taken["root"] = true
		}
		if name != collect.Other {
			out = append(out, collect.Volume{Name: name, Total: v.Total, Free: v.Free, Kind: v.Kind, Filesystem: v.Filesystem})
			continue
		}
		if other == nil {
			other = &collect.Volume{Name: collect.Other}
		}
		other.Total += v.Total
		other.Free += v.Free
		other.Count++
	}
	if other != nil {
		out = append(out, *other)
	}
	return collect.CapRows(out, func(v collect.Volume) string { return v.Name },
		func(a, b collect.Volume) int { return cmp.Compare(b.Total, a.Total) }, collect.Volume{Name: collect.Other},
		func(o *collect.Volume, v collect.Volume) {
			o.Total, o.Free, o.Count = o.Total+v.Total, o.Free+v.Free, o.Count+1
		})
}

type rate struct {
	name                   string
	in, out, inOps, outOps float64
	count                  int
}

// rates pairs counters by raw name, skips a counter that went backwards (a reset) or stayed idle, and
// merges every unsafe name into one Other row.
func (c *Collector) rates(prev, cur []IOCount, dt float64) []rate {
	old := make(map[string]IOCount, len(prev))
	for _, p := range prev {
		old[p.Name] = p
	}
	byName := map[string]*rate{}
	for _, n := range cur {
		p, ok := old[n.Name]
		if !ok || n.InBytes < p.InBytes || n.OutBytes < p.OutBytes || n.InOps < p.InOps || n.OutOps < p.OutOps {
			continue
		}
		if n.InBytes == p.InBytes && n.OutBytes == p.OutBytes {
			continue
		}
		name := c.gate.Name(n.Name)
		r := byName[name]
		if r == nil {
			r = &rate{name: name}
			byName[name] = r
		}
		r.in += float64(n.InBytes-p.InBytes) / dt
		r.out += float64(n.OutBytes-p.OutBytes) / dt
		r.inOps += float64(n.InOps-p.InOps) / dt
		r.outOps += float64(n.OutOps-p.OutOps) / dt
		if name == collect.Other {
			r.count++
		}
	}
	out := make([]rate, 0, len(byName))
	for _, r := range byName {
		out = append(out, *r)
	}
	out = collect.CapRows(out, func(r rate) string { return r.name },
		func(a, b rate) int { return cmp.Compare(b.in+b.out, a.in+a.out) }, rate{name: collect.Other},
		func(o *rate, r rate) {
			o.in, o.out, o.inOps, o.outOps, o.count = o.in+r.in, o.out+r.out, o.inOps+r.inOps, o.outOps+r.outOps, o.count+1
		})
	for i := range out {
		r := &out[i]
		r.in, r.out, r.inOps, r.outOps = round1(r.in), round1(r.out), round1(r.inOps), round1(r.outOps)
	}
	return out
}

type procKey struct {
	pid   int32
	start int64
}

// vmPrefixes match the Docker Desktop VM process: Docker VMM (com.docker.sailor holds Docker.raw here),
// Apple Virtualization or QEMU.
var vmPrefixes = []string{"com.docker.sailor", "com.apple.Virtualization.VirtualMachine", "qemu-system-"}

// isVM also takes a name cut to p_comm's 15 bytes.
func isVM(name string) bool {
	for _, p := range vmPrefixes {
		if strings.HasPrefix(name, p) || (len(name) >= 15 && strings.HasPrefix(p, name)) {
			return true
		}
	}
	return false
}

func (c *Collector) topApps(prev, cur Snapshot, dt float64) (byCPU, byMem []collect.App, vm *collect.App) {
	before := make(map[procKey]float64, len(prev.Procs))
	for _, p := range prev.Procs {
		before[procKey{p.PID, p.Start}] = p.CPUSec
	}
	since := prev.At.UnixMilli()
	apps := map[string]*collect.App{}
	merged := map[string]bool{}
	for _, p := range cur.Procs {
		var used float64
		if b, ok := before[procKey{p.PID, p.Start}]; ok {
			used = p.CPUSec - b
		} else if p.Start >= since {
			used = p.CPUSec
		}
		name := c.gate.Name(p.Name)
		a := apps[name]
		if a == nil {
			a = &collect.App{Name: name}
			apps[name] = a
		}
		a.CPU += max(0, 100*used/dt)
		a.RSS += p.RSS
		a.Procs++
		if name == collect.Other && !merged[p.Name] {
			merged[p.Name] = true
			a.Count++
		}
		if isVM(p.Name) {
			if vm == nil {
				vm = &collect.App{Name: name}
			}
			vm.CPU += max(0, 100*used/dt)
			vm.RSS += p.RSS
			vm.Procs++
		}
	}
	all := make([]collect.App, 0, len(apps))
	for _, a := range apps {
		a.CPU = round1(a.CPU)
		all = append(all, *a)
	}
	if vm != nil {
		vm.CPU = round1(vm.CPU)
	}
	byCPU = slices.Clone(all)
	sort.Slice(byCPU, func(i, j int) bool {
		if byCPU[i].CPU != byCPU[j].CPU {
			return byCPU[i].CPU > byCPU[j].CPU
		}
		return byCPU[i].Name < byCPU[j].Name
	})
	byMem = all
	sort.Slice(byMem, func(i, j int) bool {
		if byMem[i].RSS != byMem[j].RSS {
			return byMem[i].RSS > byMem[j].RSS
		}
		return byMem[i].Name < byMem[j].Name
	})
	return slices.Clone(byCPU[:min(c.topN, len(byCPU))]), slices.Clone(byMem[:min(c.topN, len(byMem))]), vm
}

func round1(v float64) float64 {
	if v < 0 {
		return -round1(-v)
	}
	return float64(int64(v*10+0.5)) / 10
}
