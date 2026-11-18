//go:build darwin

package machine

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"benchwatch/server/internal/collect"
)

// TestDarwinSource reads the real Mac (read-only) and checks the figures are plausible.
func TestDarwinSource(t *testing.T) {
	ctx := context.Background()
	c := New(NewDarwin(), Options{})
	a, err := c.Snapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	b, err := c.Snapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Partial) > 0 {
		t.Fatalf("partial failures: %v", b.Partial)
	}
	s := c.Derive(a, b)
	kinds, source := c.Kinds()
	if len(s.Cores) == 0 || source == "none" {
		t.Fatalf("cores %d kind source %s", len(s.Cores), source)
	}
	m := s.Memory
	if m == nil || m.Total == 0 || m.Used == 0 || m.Used > m.Total || m.Wired == 0 ||
		m.App+m.Wired+m.Compressed != m.Used {
		t.Fatalf("memory implausible: %+v", m)
	}
	if len(s.Volumes) == 0 || len(b.Disk) == 0 || len(b.Net) == 0 {
		t.Fatalf("volumes %d disks %d interfaces %d", len(s.Volumes), len(b.Disk), len(b.Net))
	}
	var boot *collect.Volume
	for i := range s.Volumes {
		if s.Volumes[i].Kind == "internal" {
			boot = &s.Volumes[i]
		}
	}
	if boot == nil || boot.Filesystem == "" || boot.Total == 0 {
		t.Fatalf("no boot volume with a file system among %d volumes", len(s.Volumes))
	}
	t.Logf("boot volume %q %s", boot.Name, boot.Filesystem)
	if len(b.Procs) < 20 || len(s.TopCPU) == 0 || len(s.TopMem) == 0 {
		t.Fatalf("procs %d top cpu %d top mem %d", len(b.Procs), len(s.TopCPU), len(s.TopMem))
	}
	for _, app := range append(s.TopCPU, s.TopMem...) {
		if app.Name != collect.Other && !collect.DefaultNameGate(app.Name) {
			t.Fatalf("unsafe app name kept")
		}
	}
	t.Logf("cores %d kinds %v source %s; load %.2f; mem used %d of %d; procs %d; ifaces %d; disks %d",
		len(s.Cores), kinds, source, s.Load.One, m.Used>>20, m.Total>>20, len(b.Procs), len(b.Net), len(b.Disk))
}

func TestParseIfmib(t *testing.T) {
	b := make([]byte, ifmibSize)
	d := b[ifmibDataOff:]
	binary.LittleEndian.PutUint64(d[24:], 7)
	binary.LittleEndian.PutUint64(d[40:], 8)
	binary.LittleEndian.PutUint64(d[64:], 37_346_203_975)
	binary.LittleEndian.PutUint64(d[72:], 4_855_267_212)
	c, ok := parseIfmib(b)
	if !ok || c.InOps != 7 || c.OutOps != 8 || c.InBytes != 37_346_203_975 || c.OutBytes != 4_855_267_212 {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := parseIfmib(b[:100]); ok {
		t.Fatal("short buffer accepted")
	}
}

func TestAFailedIfmibCallIsCheckedBeforeItsSize(t *testing.T) {
	var buf [256]byte
	if _, err := ifmibResult(buf[:], 4096, syscall.ENOMEM); err != syscall.ENOMEM {
		t.Fatalf("a failed call with a size past the buffer: %v", err)
	}
	if _, err := ifmibResult(buf[:], 4096, 0); err != errShortIfmib {
		t.Fatalf("a size past the buffer: %v", err)
	}
	if _, err := ifmibResult(buf[:], 8, 0); err != errShortIfmib {
		t.Fatalf("a short answer: %v", err)
	}
}

func TestBootNameIsTheLinkToRoot(t *testing.T) {
	dir := t.TempDir()
	if got := bootName(dir); got != "root" {
		t.Fatalf("empty folder: %q", got)
	}
	if err := os.Mkdir(filepath.Join(dir, "Backup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(dir, "Macintosh HD")); err != nil {
		t.Fatal(err)
	}
	if got := bootName(dir); got != "Macintosh HD" {
		t.Fatalf("boot name %q", got)
	}
	if got := bootName(filepath.Join(dir, "missing")); got != "root" {
		t.Fatalf("missing folder: %q", got)
	}
}

func TestFsNameStopsAtNULAndRefusesOddNames(t *testing.T) {
	if got := fsName([]byte{'A', 'P', 'F', 'S', 0, 'x'}); got != "apfs" {
		t.Fatalf("got %q", got)
	}
	if got := fsName([]byte{'a', ' ', 'b', 0}); got != "" {
		t.Fatalf("got %q", got)
	}
}
