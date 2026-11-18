//go:build darwin

package machine

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/sys/unix"

	"benchwatch/server/internal/collect"
)

// Darwin reads macOS directly for memory, processes and interfaces, because gopsutil's darwin code
// lacks compressed and cached memory, may read a process's arguments for its name, and runs netstat.
type Darwin struct{}

// The same pattern as the schema's is_filesystem_label check.
var fsLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,15}$`)

func NewDarwin() *Darwin { return &Darwin{} }

var lib struct {
	once         sync.Once
	err          error
	host         uint32
	numer, denom uint32
	pidinfo      func(pid int32, flavor int32, arg uint64, buf unsafe.Pointer, size int32) int32
	name         func(pid int32, buf unsafe.Pointer, size uint32) int32
	hostStats64  func(host uint32, flavor int32, info unsafe.Pointer, count *uint32) int32
}

func loadLib() error {
	lib.once.Do(func() {
		h, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = err
			return
		}
		var hostSelf func() uint32
		var timebase func(info unsafe.Pointer) int32
		purego.RegisterLibFunc(&lib.pidinfo, h, "proc_pidinfo")
		purego.RegisterLibFunc(&lib.name, h, "proc_name")
		purego.RegisterLibFunc(&lib.hostStats64, h, "host_statistics64")
		purego.RegisterLibFunc(&hostSelf, h, "mach_host_self")
		purego.RegisterLibFunc(&timebase, h, "mach_timebase_info")
		// mach_host_self adds a port reference per call, so it is taken once.
		lib.host = hostSelf()
		var tb [2]uint32
		if timebase(unsafe.Pointer(&tb[0])) != 0 || tb[1] == 0 {
			lib.err = errors.New("timebase")
			return
		}
		lib.numer, lib.denom = tb[0], tb[1]
	})
	return lib.err
}

func (d *Darwin) CPUTimes(ctx context.Context) ([]CPUTimes, error) {
	ts, err := cpu.TimesWithContext(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make([]CPUTimes, len(ts))
	for i, t := range ts {
		busy := t.User + t.System + t.Nice + t.Irq + t.Softirq + t.Steal
		out[i] = CPUTimes{Busy: busy, Total: busy + t.Idle + t.Iowait}
	}
	return out, nil
}

func (d *Darwin) Load(ctx context.Context) (collect.Load, error) {
	a, err := load.AvgWithContext(ctx)
	if err != nil {
		return collect.Load{}, err
	}
	return collect.Load{One: a.Load1, Five: a.Load5, Fifteen: a.Load15}, nil
}

// vmStatistics64 mirrors struct vm_statistics64 in <mach/vm_statistics.h> (38 integer_t words).
type vmStatistics64 struct {
	FreeCount, ActiveCount, InactiveCount, WireCount              uint32
	ZeroFill, Reactivations, Pageins, Pageouts, Faults, CowFaults uint64
	Lookups, Hits, Purges                                         uint64
	PurgeableCount, SpeculativeCount                              uint32
	Decompressions, Compressions, Swapins, Swapouts               uint64
	CompressorPageCount, ThrottledCount                           uint32
	ExternalPageCount, InternalPageCount                          uint32
	TotalUncompressedPagesInCompressor                            uint64
}

const hostVMInfo64 = 4

func (d *Darwin) Memory(ctx context.Context) (collect.Memory, error) {
	if err := loadLib(); err != nil {
		return collect.Memory{}, err
	}
	var vs vmStatistics64
	count := uint32(unsafe.Sizeof(vs) / 4)
	if kr := lib.hostStats64(lib.host, hostVMInfo64, unsafe.Pointer(&vs), &count); kr != 0 {
		return collect.Memory{}, fmt.Errorf("host_statistics64 %d", kr)
	}
	page, err := unix.SysctlUint32("vm.pagesize")
	if err != nil {
		return collect.Memory{}, err
	}
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return collect.Memory{}, err
	}
	p := uint64(page)
	m := collect.Memory{
		Total:      total,
		App:        p * uint64(vs.InternalPageCount-min(vs.PurgeableCount, vs.InternalPageCount)),
		Wired:      p * uint64(vs.WireCount),
		Compressed: p * uint64(vs.CompressorPageCount),
		Cached:     p * uint64(vs.ExternalPageCount+vs.PurgeableCount),
		Free:       p * uint64(vs.FreeCount-min(vs.SpeculativeCount, vs.FreeCount)),
	}
	m.Used = m.App + m.Wired + m.Compressed
	sw, err := mem.SwapMemoryWithContext(ctx)
	if err != nil {
		return m, ItemError{Part: collect.PartSwap, Err: err}
	}
	m.SwapTotal, m.SwapUsed = sw.Total, sw.Used
	return m, nil
}

// bootName is the name of the /Volumes entry that links to the boot volume ("Macintosh HD"), or "root".
func bootName(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "root"
	}
	for _, e := range entries {
		if to, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil && to == "/" {
			return e.Name()
		}
	}
	return "root"
}

// volumeName maps a mount point to a short name, never a path; other mounts are skipped.
func volumeName(mount string) (string, bool) {
	switch {
	case mount == "/":
		return bootName("/Volumes"), true
	case mount == "/System/Volumes/Data":
		return "data", true
	case strings.HasPrefix(mount, "/Volumes/") && !strings.Contains(mount[len("/Volumes/"):], "/"):
		return mount[len("/Volumes/"):], true
	}
	return "", false
}

func (d *Darwin) Volumes(ctx context.Context) ([]collect.Volume, error) {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []collect.Volume
	var failed error
	for _, p := range parts {
		name, ok := volumeName(p.Mountpoint)
		if !ok {
			continue
		}
		var st unix.Statfs_t
		if err := unix.Statfs(p.Mountpoint, &st); errors.Is(err, unix.ENOENT) {
			continue
		} else if err != nil {
			failed = cmp.Or(failed, err)
			continue
		}
		bs := uint64(st.Bsize)
		v := collect.Volume{Name: name, Total: st.Blocks * bs, Free: st.Bavail * bs, Filesystem: fsName(st.Fstypename[:])}
		if p.Mountpoint == "/" {
			v.Kind = "internal"
		}
		out = append(out, v)
	}
	return out, itemErr(failed)
}

// fsName reads a NUL-terminated statfs type name; a name the schema would refuse becomes empty.
func fsName(raw []byte) string {
	cut, _, _ := bytes.Cut(raw, []byte{0})
	name := strings.ToLower(string(cut))
	if !fsLabel.MatchString(name) {
		return ""
	}
	return name
}

func itemErr(err error) error {
	if err == nil {
		return nil
	}
	return ItemError{Err: err}
}

func (d *Darwin) DiskIO(ctx context.Context) ([]IOCount, error) {
	m, err := disk.IOCountersWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IOCount, 0, len(m))
	for name, s := range m {
		out = append(out, IOCount{Name: name, InBytes: s.ReadBytes, OutBytes: s.WriteBytes, InOps: s.ReadCount,
			OutOps: s.WriteCount})
	}
	return out, nil
}

// NetIO reads each interface's 64-bit counters from net.link.generic.ifdata.<index>.general (IFMIB_IFDATA),
// the source netstat -b uses; the routing socket (NET_RT_IFLIST2) reports byte counts cut to 32 bits here.
func (d *Darwin) NetIO(_ context.Context) ([]IOCount, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]IOCount, 0, len(ifs))
	var failed error
	var buf [256]byte
	for _, i := range ifs {
		mib := [6]int32{unix.CTL_NET, unix.AF_LINK, 0, ifmibIfdata, int32(i.Index), ifdataGeneral}
		size := uintptr(len(buf))
		_, _, e := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
		if e == unix.ENOENT || e == unix.ENXIO {
			continue
		}
		c, err := ifmibResult(buf[:], size, e)
		if err != nil {
			failed = cmp.Or(failed, err)
			continue
		}
		c.Name = i.Name
		out = append(out, c)
	}
	return out, itemErr(failed)
}

// ifmibResult checks the error first: a failed call can report a size past the buffer.
func ifmibResult(buf []byte, size uintptr, e syscall.Errno) (IOCount, error) {
	if e != 0 {
		return IOCount{}, e
	}
	if size > uintptr(len(buf)) {
		return IOCount{}, errShortIfmib
	}
	c, ok := parseIfmib(buf[:size])
	if !ok {
		return IOCount{}, errShortIfmib
	}
	return c, nil
}

var errShortIfmib = errors.New("short ifmibdata")

const (
	ifmibIfdata   = 2
	ifdataGeneral = 1
	// struct ifmibdata is packed: 52 bytes of header, then if_data64 (128 bytes).
	ifmibDataOff = 52
	ifmibSize    = ifmibDataOff + 128
)

// parseIfmib reads if_data64: ipackets at 24, opackets 40, ibytes 64, obytes 72.
func parseIfmib(b []byte) (IOCount, bool) {
	if len(b) < ifmibSize {
		return IOCount{}, false
	}
	d := b[ifmibDataOff:]
	le := binary.LittleEndian
	return IOCount{InOps: le.Uint64(d[24:]), OutOps: le.Uint64(d[40:]), InBytes: le.Uint64(d[64:]),
		OutBytes: le.Uint64(d[72:])}, true
}

// procTaskInfo mirrors struct proc_taskinfo in <sys/proc_info.h>.
type procTaskInfo struct {
	VirtualSize, ResidentSize, TotalUser, TotalSystem, ThreadsUser, ThreadsSystem uint64
	Policy, Faults, Pageins, CowFaults, MessagesSent, MessagesReceived            int32
	SyscallsMach, SyscallsUnix, Csw, Threadnum, Numrunning, Priority              int32
}

const procPidTaskInfo = 4

// Procs lists every process this user may inspect; others (root daemons) fail proc_pidinfo and are skipped.
func (d *Darwin) Procs(_ context.Context) ([]Proc, error) {
	if err := loadLib(); err != nil {
		return nil, err
	}
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	scale := float64(lib.numer) / float64(lib.denom) / 1e9
	out := make([]Proc, 0, len(kps))
	var nb [64]byte
	for i := range kps {
		kp := &kps[i].Proc
		if kp.P_pid <= 0 {
			continue
		}
		var ti procTaskInfo
		size := int32(unsafe.Sizeof(ti))
		if lib.pidinfo(kp.P_pid, procPidTaskInfo, 0, unsafe.Pointer(&ti), size) != size {
			continue
		}
		name := ""
		if n := lib.name(kp.P_pid, unsafe.Pointer(&nb[0]), uint32(len(nb))); n > 0 && int(n) <= len(nb) {
			name = string(nb[:n])
		} else {
			name = string(bytes.TrimRight(kp.P_comm[:], "\x00"))
		}
		out = append(out, Proc{
			PID:    kp.P_pid,
			Start:  kp.P_starttime.Sec*1000 + int64(kp.P_starttime.Usec)/1000,
			Name:   name,
			CPUSec: float64(ti.TotalUser+ti.TotalSystem) * scale,
			RSS:    ti.ResidentSize,
		})
	}
	return out, nil
}

const ioregPath = "/usr/sbin/ioreg"

// CoreKinds reads each CPU's cluster-type and logical-cpu-id from the IODeviceTree, which needs no sudo.
// Without them it falls back to the order macOS uses on Apple silicon: efficiency cores first.
func (d *Darwin) CoreKinds(ctx context.Context, ncpu int) ([]collect.CoreKind, string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ioregPath, "-p", "IODeviceTree", "-c", "IOPlatformDevice", "-k", "cluster-type", "-r",
		"-d", "1", "-w0")
	cmd.Env = []string{}
	ecount, ecErr := unix.SysctlUint32("hw.perflevel1.logicalcpu")
	if out, err := cmd.Output(); err == nil {
		if kinds, ok := ParseIOReg(out, ncpu); ok && (ecErr != nil || countKind(kinds, collect.CoreE) == int(ecount)) {
			return kinds, "ioreg"
		}
	}
	if ecErr != nil || int(ecount) > ncpu {
		return unknownKinds(ncpu), "none"
	}
	kinds := make([]collect.CoreKind, ncpu)
	for i := range kinds {
		kinds[i] = collect.CoreP
		if i < int(ecount) {
			kinds[i] = collect.CoreE
		}
	}
	return kinds, "sysctl-order"
}
