package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"benchwatch/server/internal/collect"
)

// Fake is a scripted Source; tests set its fields between snapshots. Fail makes one part return an error.
type Fake struct {
	CPU   []CPUTimes
	Mem   collect.Memory
	Vols  []collect.Volume
	Disk  []IOCount
	Net   []IOCount
	Proc  []Proc
	Kinds []collect.CoreKind
	Err   error
	Fail  map[string]error
}

func (f *Fake) CoreKinds(context.Context, int) ([]collect.CoreKind, string) { return f.Kinds, "fake" }
func (f *Fake) CPUTimes(context.Context) ([]CPUTimes, error) {
	return append([]CPUTimes(nil), f.CPU...), f.Err
}
func (f *Fake) Load(context.Context) (collect.Load, error) {
	return collect.Load{One: 1, Five: 2, Fifteen: 3}, f.Fail[collect.PartLoad]
}
func (f *Fake) Memory(context.Context) (collect.Memory, error) {
	return f.Mem, f.Fail[collect.PartMemory]
}
func (f *Fake) Volumes(context.Context) ([]collect.Volume, error) {
	return f.Vols, f.Fail[collect.PartVolumes]
}
func (f *Fake) DiskIO(context.Context) ([]IOCount, error) {
	return append([]IOCount(nil), f.Disk...), f.Fail[collect.PartDiskIO]
}
func (f *Fake) NetIO(context.Context) ([]IOCount, error) {
	return append([]IOCount(nil), f.Net...), f.Fail[collect.PartNet]
}
func (f *Fake) Procs(context.Context) ([]Proc, error) {
	return append([]Proc(nil), f.Proc...), f.Fail[collect.PartApps]
}

func clockAt(t0 time.Time) (*time.Time, func() time.Time) {
	now := t0
	return &now, func() time.Time { return now }
}

func TestDeriveRatesAndCores(t *testing.T) {
	t0 := time.Unix(10_000, 0)
	now, nowf := clockAt(t0)
	f := &Fake{
		Kinds: []collect.CoreKind{collect.CoreE, collect.CoreP},
		CPU:   []CPUTimes{{Busy: 10, Total: 100}, {Busy: 50, Total: 100}},
		Disk:  []IOCount{{Name: "disk0", InBytes: 1000, OutBytes: 2000, InOps: 10, OutOps: 20}},
		Net: []IOCount{{Name: "en0", InBytes: 5000, OutBytes: 100}, {Name: "utun3", InBytes: 7, OutBytes: 7},
			{Name: "en1", InBytes: 900, OutBytes: 900}},
	}
	c := New(f, Options{Now: nowf})
	a, err := c.Snapshot(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	*now = t0.Add(10 * time.Second)
	f.CPU = []CPUTimes{{Busy: 15, Total: 110}, {Busy: 57.5, Total: 130}}
	f.Disk[0] = IOCount{Name: "disk0", InBytes: 11000, OutBytes: 2000, InOps: 30, OutOps: 20}
	f.Net = []IOCount{{Name: "en0", InBytes: 15000, OutBytes: 600}, {Name: "utun3", InBytes: 7, OutBytes: 7},
		{Name: "en1", InBytes: 10, OutBytes: 10}}
	b, _ := c.Snapshot(context.Background(), false)
	s := c.Derive(a, b)
	if s.Interval != 10 || len(s.Cores) != 2 || s.Cores[0].Kind != collect.CoreE || s.Cores[0].Busy != 50 ||
		s.Cores[1].Busy != 25 {
		t.Fatalf("cores %+v interval %v", s.Cores, s.Interval)
	}
	if len(s.DiskIO) != 1 || s.DiskIO[0].ReadBps != 1000 || s.DiskIO[0].ReadOps != 2 || s.DiskIO[0].WriteBps != 0 {
		t.Fatalf("disk %+v", s.DiskIO)
	}
	// utun3 is idle and en1's counter went backwards (reset): both are left out.
	if len(s.Net) != 1 || s.Net[0].Name != "en0" || s.Net[0].RxBps != 1000 || s.Net[0].TxBps != 50 {
		t.Fatalf("net %+v", s.Net)
	}
	if s.TopCPU != nil || s.TopMem != nil || s.Missing != nil || s.Load == nil || s.Memory == nil {
		t.Fatalf("sample %+v", s)
	}
}

func TestFailedPartsAreMissingNotZero(t *testing.T) {
	t0 := time.Unix(30_000, 0)
	now, nowf := clockAt(t0)
	f := &Fake{
		CPU: []CPUTimes{{Busy: 1, Total: 2}}, Mem: collect.Memory{Total: 16 << 30, Used: 8 << 30},
		Vols: []collect.Volume{{Name: "root", Total: 100, Free: 50}},
		Disk: []IOCount{{Name: "disk0", InBytes: 10}}, Net: []IOCount{{Name: "en0", InBytes: 10}},
		Proc: []Proc{{PID: 1, Start: 1, Name: "node", CPUSec: 1, RSS: 1}},
	}
	c := New(f, Options{Now: nowf})
	a, _ := c.Snapshot(context.Background(), true)
	f.Fail = map[string]error{
		collect.PartLoad: syscall.EIO, collect.PartMemory: syscall.EPERM, collect.PartVolumes: syscall.EACCES,
		collect.PartNet: syscall.ENOENT, collect.PartApps: syscall.EPERM,
	}
	*now = t0.Add(10 * time.Second)
	f.CPU = []CPUTimes{{Busy: 2, Total: 4}}
	f.Disk = []IOCount{{Name: "disk0", InBytes: 110}}
	b, err := c.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	for _, p := range b.Partial {
		codes[p.Part] = p.Code
	}
	want := map[string]string{collect.PartLoad: "ESYSCALL", collect.PartMemory: "EPERM", collect.PartVolumes: "EACCES",
		collect.PartNet: "ENOENT", collect.PartApps: "EPERM"}
	for k, v := range want {
		if codes[k] != v {
			t.Fatalf("partial %v, want %v", codes, want)
		}
	}
	s := c.Derive(a, b)
	for _, part := range []string{collect.PartLoad, collect.PartMemory, collect.PartVolumes, collect.PartNet,
		collect.PartApps} {
		if !s.Lacks(part) {
			t.Errorf("%s not marked missing: %v", part, s.Missing)
		}
	}
	if s.Lacks(collect.PartDiskIO) || len(s.DiskIO) != 1 || s.DiskIO[0].ReadBps != 10 {
		t.Fatalf("disk should still be there: %+v %v", s.DiskIO, s.Missing)
	}
	if s.Load != nil || s.Memory != nil || s.Volumes != nil || s.Net != nil || s.TopCPU != nil || s.AppsWindow != 0 {
		t.Fatalf("failed parts must be empty, not zero: %+v", s)
	}
	js, _ := json.Marshal(s)
	if !strings.Contains(string(js), `"memory":null`) || !strings.Contains(string(js), `"load":null`) {
		t.Fatalf("missing memory must serialise as null: %s", js)
	}

	f.Fail = nil
	*now = t0.Add(20 * time.Second)
	f.Net = []IOCount{{Name: "en0", InBytes: 20}}
	c2, _ := c.Snapshot(context.Background(), true)
	if s := c.Derive(b, c2); !s.Lacks(collect.PartNet) || !s.Lacks(collect.PartApps) || s.Lacks(collect.PartMemory) {
		t.Fatalf("a rate needs both ends: missing %v", s.Missing)
	}
}

func TestTopAppsGroupByNameAndPIDReuse(t *testing.T) {
	t0 := time.Unix(20_000, 0)
	now, nowf := clockAt(t0)
	f := &Fake{
		CPU: []CPUTimes{{Busy: 1, Total: 2}},
		Proc: []Proc{
			{PID: 10, Start: 1, Name: "node", CPUSec: 100, RSS: 100 << 20},
			{PID: 11, Start: 1, Name: "node", CPUSec: 50, RSS: 50 << 20},
			{PID: 12, Start: 1, Name: "old", CPUSec: 999, RSS: 1 << 20},
			{PID: 13, Start: 1, Name: "com.docker.sailor", CPUSec: 10, RSS: 900 << 20},
		},
	}
	c := New(f, Options{Now: nowf, TopN: 3})
	a, _ := c.Snapshot(context.Background(), true)
	*now = t0.Add(10 * time.Second)
	f.Proc = []Proc{
		{PID: 10, Start: 1, Name: "node", CPUSec: 105, RSS: 100 << 20},
		{PID: 11, Start: 1, Name: "node", CPUSec: 52, RSS: 50 << 20},
		// PID 12 was reused by a new process that started inside the window.
		{PID: 12, Start: t0.Add(5 * time.Second).UnixMilli(), Name: "/Users/x/secret --token abc", CPUSec: 1, RSS: 2 << 20},
		{PID: 13, Start: 1, Name: "com.docker.sailor", CPUSec: 19, RSS: 900 << 20},
	}
	b, _ := c.Snapshot(context.Background(), true)
	s := c.Derive(a, b)
	if len(s.TopCPU) != 3 || s.TopCPU[0].Name != "com.docker.sailor" || s.TopCPU[0].CPU != 90 || s.AppsWindow != 10 {
		t.Fatalf("top cpu %+v", s.TopCPU)
	}
	if s.TopCPU[1].Name != "node" || s.TopCPU[1].CPU != 70 || s.TopCPU[1].Procs != 2 || s.TopCPU[1].RSS != 150<<20 {
		t.Fatalf("node group %+v", s.TopCPU[1])
	}
	if s.TopCPU[2].Name != collect.Other || s.TopCPU[2].CPU != 10 {
		t.Fatalf("reused pid %+v", s.TopCPU[2])
	}
	if s.TopMem[0].Name != "com.docker.sailor" || s.DockerVM == nil || s.DockerVM.CPU != 90 {
		t.Fatalf("top mem %+v vm %+v", s.TopMem, s.DockerVM)
	}
	if cap(s.TopCPU) != 3 || cap(s.TopMem) != 3 {
		t.Fatalf("top lists keep the full array: cap %d %d", cap(s.TopCPU), cap(s.TopMem))
	}
	cpu, _, w, ok := c.Apps(a, b)
	if !ok || w != 10 || !slices.Equal(cpu, s.TopCPU) {
		t.Fatalf("Apps %v %v %+v", ok, w, cpu)
	}
}

func TestTopListsStayWithinMaxRows(t *testing.T) {
	now := time.Unix(20_000, 0)
	f := &Fake{CPU: []CPUTimes{{Busy: 1, Total: 2}}}
	for i := range 200 {
		f.Proc = append(f.Proc, Proc{PID: int32(100 + i), Start: 1, Name: fmt.Sprintf("app%03d", i), RSS: uint64(i+1) << 20})
	}
	c := New(f, Options{Now: func() time.Time { return now }, TopN: 100000})
	a, _ := c.Snapshot(context.Background(), true)
	now = now.Add(10 * time.Second)
	for i := range f.Proc {
		f.Proc[i].CPUSec = float64(i + 1)
	}
	b, _ := c.Snapshot(context.Background(), true)
	if s := c.Derive(a, b); len(s.TopCPU) != collect.MaxRows || len(s.TopMem) != collect.MaxRows {
		t.Fatalf("TopN 100000 gave %d and %d rows", len(s.TopCPU), len(s.TopMem))
	}
}

// refused holds names the default gate refuses; tests assert none reaches a sample and each merges into Other.
var refused = []string{"/Users/x/secret", "curl -H Authorization", "123e4567-e89b-12d3-a456-426614174000",
	"4f9c2b1a8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b2a1f0e9d8c7b6a5f4e3d2c1b"}

func TestUnsafeNamesMergeIntoOneOtherRow(t *testing.T) {
	t0 := time.Unix(40_000, 0)
	now, nowf := clockAt(t0)
	f := &Fake{CPU: []CPUTimes{{Busy: 1, Total: 2}}}
	for i, n := range refused {
		f.Vols = append(f.Vols, collect.Volume{Name: n, Total: 100, Free: 10})
		f.Disk = append(f.Disk, IOCount{Name: n, InBytes: uint64(i) * 1000})
		f.Net = append(f.Net, IOCount{Name: n, InBytes: uint64(i) * 1e6})
		f.Proc = append(f.Proc, Proc{PID: int32(i + 1), Start: 1, Name: n, CPUSec: float64(i), RSS: 1 << 20})
	}
	f.Vols = append(f.Vols, collect.Volume{Name: "root", Total: 7, Free: 3})
	f.Net = append(f.Net, IOCount{Name: "en0", InBytes: 0})
	c := New(f, Options{Now: nowf})
	a, _ := c.Snapshot(context.Background(), true)
	*now = t0.Add(10 * time.Second)
	for i := range refused {
		f.Disk[i].InBytes += uint64(i+1) * 100
		f.Net[i].InBytes += uint64(i+1) * 1000
		f.Proc[i].CPUSec += float64(i + 1)
	}
	f.Net[len(refused)].InBytes = 50
	b, _ := c.Snapshot(context.Background(), true)
	s := c.Derive(a, b)

	js, _ := json.Marshal(s)
	for _, n := range refused {
		if strings.Contains(string(js), n) {
			t.Fatalf("unsafe name %q reached the sample: %s", n, js)
		}
	}
	vols := map[string]collect.Volume{}
	for _, v := range s.Volumes {
		vols[v.Name] = v
	}
	if o := vols[collect.Other]; len(s.Volumes) != 2 || o.Total != 400 || o.Free != 40 || o.Count != 4 ||
		vols["root"].Total != 7 {
		t.Fatalf("volumes %+v", s.Volumes)
	}
	// 100+200+300+400 bytes over 10 s on disk; 1000+..+4000 on the interfaces; 1+2+3+4 s of CPU over 10 s.
	if len(s.DiskIO) != 1 || s.DiskIO[0].Name != collect.Other || s.DiskIO[0].ReadBps != 100 || s.DiskIO[0].Count != 4 {
		t.Fatalf("disk %+v", s.DiskIO)
	}
	if len(s.Net) != 2 || s.Net[1].Name != collect.Other || s.Net[1].RxBps != 1000 || s.Net[1].Count != 4 ||
		s.Net[0].RxBps != 5 {
		t.Fatalf("net %+v", s.Net)
	}
	if len(s.TopCPU) != 1 || s.TopCPU[0].Name != collect.Other || s.TopCPU[0].CPU != 100 || s.TopCPU[0].Procs != 4 {
		t.Fatalf("apps %+v", s.TopCPU)
	}
}

func TestCustomGateIsUsed(t *testing.T) {
	f := &Fake{CPU: []CPUTimes{{Busy: 1, Total: 2}}, Vols: []collect.Volume{{Name: "root"}, {Name: "data"}}}
	c := New(f, Options{Gate: func(s string) bool { return s == "data" }})
	a, _ := c.Snapshot(context.Background(), false)
	s := c.Derive(a, a)
	if len(s.Volumes) != 2 || s.Volumes[0].Name != "data" || s.Volumes[1].Name != collect.Other {
		t.Fatalf("volumes %+v", s.Volumes)
	}
}

func TestParseIOReg(t *testing.T) {
	// Trimmed from this Mac's ioreg output (M2 Pro): cpu-id skips 7, logical-cpu-id does not.
	out := []byte(`+-o cpu0@0  <class IOPlatformDevice>
      "logical-cpu-id" = 0
      "cluster-type" = <"E">
+-o cpu1@1  <class IOPlatformDevice>
      "cluster-type" = <"E">
      "logical-cpu-id" = 1
+-o cpu4@100  <class IOPlatformDevice>
      "cluster-type" = <"P">
      "logical-cpu-id" = 2
+-o cpu8@200  <class IOPlatformDevice>
      "cluster-type" = <"P">
      "logical-cpu-id" = 3
`)
	kinds, ok := ParseIOReg(out, 4)
	if !ok || !slices.Equal(kinds, []collect.CoreKind{"E", "E", "P", "P"}) {
		t.Fatalf("parse failed %v %v", kinds, ok)
	}
	if _, ok := ParseIOReg(out, 5); ok {
		t.Fatal("a missing index must fail")
	}
	dup := append(slices.Clone(out), []byte("+-o cpu9@201\n \"cluster-type\" = <\"P\">\n \"logical-cpu-id\" = 3\n")...)
	if _, ok := ParseIOReg(dup, 4); ok {
		t.Fatal("a duplicate index must fail")
	}
	if _, ok := ParseIOReg(dup, 5); ok {
		t.Fatal("a duplicate index hiding a missing one must fail")
	}
	trailer := append(slices.Clone(out),
		[]byte("+-o pmgr@300  <class IOPlatformDevice>\n \"cluster-type\" = <\"E\">\n")...)
	if kinds, ok := ParseIOReg(trailer, 4); !ok || kinds[3] != collect.CoreP {
		t.Fatalf("a trailing non-CPU node relabelled the last core: %v %v", kinds, ok)
	}
	nested := []byte("+-o cpu0@0\n | +-o child@1\n |   \"cluster-type\" = <\"P\">\n \"logical-cpu-id\" = 0\n")
	if _, ok := ParseIOReg(nested, 1); ok {
		t.Fatal("properties after a child node must not type the CPU")
	}
}

func TestOneFailedItemKeepsTheOthers(t *testing.T) {
	t0 := time.Unix(50_000, 0)
	now, nowf := clockAt(t0)
	f := &Fake{
		CPU: []CPUTimes{{Busy: 1, Total: 2}}, Mem: collect.Memory{Total: 16 << 30, Used: 8 << 30},
		Vols: []collect.Volume{{Name: "root", Total: 100, Free: 50}},
		Net:  []IOCount{{Name: "en0", InBytes: 10}},
	}
	c := New(f, Options{Now: nowf})
	a, _ := c.Snapshot(context.Background(), false)
	f.Fail = map[string]error{
		collect.PartVolumes: ItemError{Err: syscall.EIO}, collect.PartNet: ItemError{Err: syscall.ENXIO},
		collect.PartMemory: ItemError{Part: collect.PartSwap, Err: syscall.EPERM},
	}
	*now = t0.Add(10 * time.Second)
	f.Net = []IOCount{{Name: "en0", InBytes: 110}}
	b, _ := c.Snapshot(context.Background(), false)
	s := c.Derive(a, b)
	if len(s.Volumes) != 1 || len(s.Net) != 1 || s.Net[0].RxBps != 10 || s.Memory == nil || s.Memory.Total != 16<<30 {
		t.Fatalf("items read must stay: volumes %+v net %+v memory %+v", s.Volumes, s.Net, s.Memory)
	}
	if !slices.Equal(s.Missing, []string{collect.PartSwap, collect.PartVolumes, collect.PartNet}) {
		t.Fatalf("missing %v", s.Missing)
	}
	codes := map[string]string{}
	for _, p := range b.Partial {
		codes[p.Part] = p.Code
	}
	if codes[collect.PartSwap] != "EPERM" || codes[collect.PartVolumes] != "ESYSCALL" || codes[collect.PartMemory] != "" {
		t.Fatalf("partial %v", codes)
	}
}

func TestRowsAreCappedWithOneOtherRow(t *testing.T) {
	t0 := time.Unix(60_000, 0)
	now, nowf := clockAt(t0)
	f := &Fake{CPU: []CPUTimes{{Busy: 1, Total: 2}}}
	for i := range 100 {
		f.Vols = append(f.Vols, collect.Volume{Name: fmt.Sprintf("vol%d", i), Total: uint64(i + 1), Free: 1})
		f.Net = append(f.Net, IOCount{Name: fmt.Sprintf("en%d", i), InBytes: 0})
		f.Disk = append(f.Disk, IOCount{Name: fmt.Sprintf("disk%d", i), InBytes: 0})
	}
	f.Vols = append(f.Vols, collect.Volume{Name: refused[0], Total: 1000, Free: 1})
	for i := range 30 {
		f.Proc = append(f.Proc, Proc{PID: int32(i + 1), Start: 1, Name: fmt.Sprintf("/Users/x/tool%d", i), CPUSec: 0, RSS: 1})
	}
	f.Proc = append(f.Proc, Proc{PID: 99, Start: 1, Name: collect.Other, CPUSec: 0, RSS: 1})
	c := New(f, Options{Now: nowf})
	a, _ := c.Snapshot(context.Background(), true)
	*now = t0.Add(10 * time.Second)
	for i := range 100 {
		f.Net[i].InBytes, f.Disk[i].InBytes = uint64(i+1)*10, uint64(i+1)*10
	}
	for i := range f.Proc {
		f.Proc[i].CPUSec = 1
	}
	b, _ := c.Snapshot(context.Background(), true)
	s := c.Derive(a, b)
	others := func(names []string) (n int) {
		for _, v := range names {
			if v == collect.Other {
				n++
			}
		}
		return n
	}
	var vols, nets, disks, apps []string
	for _, v := range s.Volumes {
		vols = append(vols, v.Name)
	}
	for _, v := range s.Net {
		nets = append(nets, v.Name)
	}
	for _, v := range s.DiskIO {
		disks = append(disks, v.Name)
	}
	for _, v := range s.TopCPU {
		apps = append(apps, v.Name)
	}
	for _, l := range [][]string{vols, nets, disks} {
		if len(l) != collect.MaxRows || others(l) != 1 || !slices.IsSorted(l) {
			t.Fatalf("list of %d rows, %d other rows", len(l), others(l))
		}
	}
	o := s.Volumes[slices.Index(vols, collect.Other)]
	if o.Count != 1+100-(collect.MaxRows-1) || o.Total != 1000+uint64((100-63)*(100-63+1)/2) {
		t.Fatalf("other volume %+v", o)
	}
	if _, ok := slices.BinarySearch(vols, "vol99"); !ok {
		t.Fatal("the largest volumes must keep their rows")
	}
	if others(apps) != 1 || s.TopCPU[0].Name != collect.Other || s.TopCPU[0].Count != 30 || s.TopCPU[0].Procs != 30 {
		t.Fatalf("apps %+v", s.TopCPU)
	}
	if !slices.Contains(apps, collect.OtherReal) {
		t.Fatalf("a process named other must keep its own row: %v", apps)
	}
}

func TestRefusedBootVolumeNameStaysRootWithItsKind(t *testing.T) {
	f := &Fake{Vols: []collect.Volume{
		{Name: "Ana's SSD", Total: 500, Free: 200, Kind: "internal", Filesystem: "apfs"},
		{Name: "my backup", Total: 100, Free: 50, Kind: "external", Filesystem: "exfat"},
	}}
	by := map[string]collect.Volume{}
	for _, v := range New(f, Options{}).volumes(f.Vols) {
		by[v.Name] = v
	}
	if r := by["root"]; len(by) != 2 || r.Kind != "internal" || r.Filesystem != "apfs" || r.Total != 500 ||
		by[collect.Other].Kind != "" {
		t.Fatalf("volumes %+v", by)
	}
}

func TestRefusedBootVolumeNeverDuplicatesARealRootRow(t *testing.T) {
	for _, order := range []bool{false, true} {
		vols := []collect.Volume{
			{Name: "Ana's SSD", Total: 500, Free: 200, Kind: "internal", Filesystem: "apfs"},
			{Name: "root", Total: 100, Free: 50, Kind: "external", Filesystem: "exfat"},
		}
		if order {
			vols[0], vols[1] = vols[1], vols[0]
		}
		seen := map[string]int{}
		by := map[string]collect.Volume{}
		for _, v := range New(&Fake{Vols: vols}, Options{}).volumes(vols) {
			seen[v.Name]++
			by[v.Name] = v
		}
		if seen["root"] != 1 || seen[collect.Other] != 1 {
			t.Fatalf("order %v: names %v", order, seen)
		}
		if o := by[collect.Other]; by["root"].Total != 100 || o.Total != 500 || o.Free != 200 {
			t.Fatalf("order %v: volumes %+v", order, by)
		}
	}
}
