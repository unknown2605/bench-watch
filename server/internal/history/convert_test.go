package history

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/store"
)

var at0 = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

func sampleBatch(at time.Time) collect.Batch {
	swap := uint64(7)
	return collect.Batch{
		Machine: []collect.MachineSample{{
			At: at, Interval: 10,
			Cores: []collect.Core{{Index: 0, Kind: collect.CoreP, Busy: 40}, {Index: 1, Kind: collect.CoreE, Busy: 20},
				{Index: 2, Kind: collect.CoreUnknown, Busy: 0}},
			Load: &collect.Load{One: 1, Five: 2, Fifteen: 3},
			Memory: &collect.Memory{Total: 100, Used: 60, App: 30, Wired: 20, Compressed: 10, Cached: 5,
				SwapTotal: 9, SwapUsed: 1},
			Volumes: []collect.Volume{
				{Name: "Macintosh HD", Total: 10, Free: 4, Kind: "internal", Filesystem: "apfs"}, {Name: "/bad", Total: 1},
				{Name: collect.Other, Total: 3, Free: 1, Count: 2}},
			DiskIO: []collect.DiskIO{{Name: "disk0", ReadBps: 10.4, WriteBps: 20.6}, {Name: "disk1", ReadBps: 1}},
			Net:    []collect.NetIO{{Name: "Wi-Fi", RxBps: 5, TxBps: 6}, {Name: "bad/if", RxBps: 1}},
			TopCPU: []collect.App{{Name: "Docker", CPU: 30, RSS: 1000}, {Name: "Safari", CPU: 10, RSS: 50}},
			TopMem: []collect.App{{Name: "Docker", CPU: 30, RSS: 1000}, {Name: "Code Helper (GPU)", CPU: 1, RSS: 900}},
		}},
		Docker: []collect.DockerSample{{
			At: at, Interval: 10,
			Containers: []collect.Container{
				{Name: "mariadb_service", Stack: "infrastructure-services", State: "running", Health: "healthy",
					CPU: 1.5, MemUsed: 200, MemLimit: 1000, RxBps: 3, TxBps: 4, UptimeSec: 3600},
				{Name: "lonely", State: "exited", Missing: []string{collect.PartStats}},
				{Name: "warming", State: "running", Missing: []string{collect.PartRates}, MemUsed: 9},
				{Name: "bad name", State: "running"},
				{Name: collect.Other, State: "running", Count: 3},
				{Name: "odd", State: collect.Other},
			},
			VM: collect.DockerVM{NCPU: 8, MemTotal: 8000, MemUsed: 209, CPU: 1.5, SwapUsed: &swap},
			Disk: &collect.DockerDisk{At: at, Images: 10, ImageCount: 2, Volumes: 20, VolumeCount: 3, BuildCache: 5,
				Containers: 1, ImagesReclaimable: 4, ImagesUnused: 1, LargestImage: ptr(int64(9)), VolumesReclaimable: 3,
				VolumesUnused: 2, BuildCacheReclaimable: -5, Reclaimable: 7},
		}},
		Events: []collect.DockerEvent{{At: at, Name: "mariadb_service", Kind: "start"},
			{At: at, Name: "mariadb_service", Kind: "exec"}, {At: at, Name: "x y", Kind: "die"}},
	}
}

func TestConvertKeepsSchemaShape(t *testing.T) {
	sb := Convert(sampleBatch(at0))
	if len(sb.Machine) != 1 || len(sb.DockerVM) != 1 {
		t.Fatalf("machine %d, vm %d rows", len(sb.Machine), len(sb.DockerVM))
	}
	m := sb.Machine[0]
	if m.CpuBusyPct == nil || *m.CpuBusyPct != 20 {
		t.Errorf("cpu busy %v, want the mean of every core (20)", m.CpuBusyPct)
	}
	if m.SwapUsedBytes == nil || *m.MemTotalBytes != 100 || *m.DiskReadBps != 11 || *m.DiskWriteBps != 21 {
		t.Errorf("memory or disk group wrong: %+v", m)
	}
	if len(sb.Cores) != 2 || sb.Cores[0].Kind != store.CoreKindPerformance ||
		sb.Cores[1].Kind != store.CoreKindEfficiency {
		t.Errorf("cores %+v, want P and E only", sb.Cores)
	}
	if len(sb.Volumes) != 2 || sb.Volumes[1].MergedNames != 2 || sb.Volumes[0].MergedNames != 0 {
		t.Errorf("volumes %+v", sb.Volumes)
	}
	boot := sb.Volumes[0]
	if boot.Kind == nil || *boot.Kind != store.VolumeKindInternal || boot.Filesystem == nil || *boot.Filesystem != "apfs" {
		t.Errorf("boot volume kind %v filesystem %v", boot.Kind, boot.Filesystem)
	}
	if v := sb.Volumes[1]; v.Kind != nil || v.Filesystem != nil {
		t.Errorf("merged volume kind %v filesystem %v", v.Kind, v.Filesystem)
	}
	if len(sb.Interfaces) != 1 || len(sb.Apps) != 3 {
		t.Errorf("interfaces %d (want 1), apps %d (want 3, union by name)", len(sb.Interfaces), len(sb.Apps))
	}
	if len(sb.Containers) != 4 {
		t.Fatalf("containers %d, want 4 (bad name and unknown state left out)", len(sb.Containers))
	}
	c := sb.Containers[0]
	if c.Stack != "infrastructure-services" || c.Health != store.ContainerHealthHealthy || !c.StartedAt.Valid ||
		c.CpuPct == nil || *c.RxBps != 3 {
		t.Errorf("first container %+v", c)
	}
	l := sb.Containers[1]
	if l.Stack != "none" || l.Health != store.ContainerHealthNone || l.MemBytes != nil || l.CpuPct != nil {
		t.Errorf("container without stats %+v", l)
	}
	if w := sb.Containers[2]; w.MemBytes == nil || w.CpuPct != nil || w.RxBps != nil || w.TxBps != nil {
		t.Errorf("container without rates %+v", w)
	}
	if o := sb.Containers[3]; o.MergedNames != 3 || o.StartedAt.Valid {
		t.Errorf("other row %+v", o)
	}
	vm := sb.DockerVM[0]
	if vm.Containers != 8 || vm.Running != 6 || *vm.DiskUsedBytes != 36 || *vm.SwapUsedBytes != 7 || *vm.Cpus != 8 {
		t.Errorf("vm row containers %d running %d disk %v", vm.Containers, vm.Running, vm.DiskUsedBytes)
	}
	if *vm.DiskReclaimableBytes != 7 || *vm.ImagesReclaimableBytes != 4 || *vm.ImagesUnused != 1 ||
		*vm.ImagesLargestBytes != 9 || *vm.VolumesReclaimableBytes != 3 || *vm.VolumesUnused != 2 ||
		*vm.BuildCacheReclaimableBytes != 0 {
		t.Errorf("reclaimable figures %+v", vm)
	}
	if len(sb.Events) != 1 || sb.Events[0].Kind != store.DockerEventKindStart {
		t.Errorf("events %+v", sb.Events)
	}
}

func TestConvertDropsMemoryGroupWithoutSwap(t *testing.T) {
	b := sampleBatch(at0)
	b.Machine[0].Missing = []string{collect.PartSwap, collect.PartDiskIO}
	m := Convert(b).Machine[0]
	if m.MemTotalBytes != nil || m.SwapUsedBytes != nil || m.DiskReadBps != nil || m.DiskWriteBps != nil {
		t.Errorf("memory and disk must be NULL as whole groups: %+v", m)
	}
	b.Machine[0].Missing = nil
	b.Machine[0].Memory, b.Machine[0].Load, b.Machine[0].Cores = nil, nil, nil
	m = Convert(b).Machine[0]
	if m.MemTotalBytes != nil || m.Load1 != nil || m.CpuBusyPct != nil {
		t.Errorf("unread parts must be NULL: %+v", m)
	}
}

type failBegin struct{ err error }

func (f failBegin) Begin(context.Context) (pgx.Tx, error) { return nil, f.err }

func TestSinkMarksSchemaRefusalsPermanent(t *testing.T) {
	refused := Sink{DB: failBegin{&pgconn.PgError{Code: "23514"}}}.Write(context.Background(), sampleBatch(at0))
	if !collect.IsPermanent(refused) {
		t.Errorf("check violation must be permanent, got %v", refused)
	}
	down := Sink{DB: failBegin{errors.New("connection refused")}}.Write(context.Background(), sampleBatch(at0))
	if down == nil || collect.IsPermanent(down) {
		t.Errorf("a connection error must be retried, got %v", down)
	}
	if err := (Sink{}).Write(context.Background(), sampleBatch(at0)); err == nil || collect.IsPermanent(err) {
		t.Errorf("no database must be retried, got %v", err)
	}
}
