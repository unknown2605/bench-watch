package docker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/docker/dockertest"
)

func listOf(running map[int]bool) string {
	var items []string
	for n, on := range running {
		state := "exited"
		if on {
			state = "running"
		}
		items = append(items, fmt.Sprintf(`{"Id":%q,"Names":["/c%d"],"State":%q}`, dockertest.ID(n), n, state))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func snap(t *testing.T, c *Collector) Snapshot {
	t.Helper()
	s, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A container that starts gets a stream on the next snapshot, one that stops loses it, and only reads are sent.
func TestStreamsFollowContainersThatStartAndStop(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	c := newTestCollector(t, path, nil, Options{})
	d.Body("/containers/json", listOf(map[int]bool{1: true, 2: true}))
	snap(t, c)
	waitFor(t, func() bool { return d.Streams(dockertest.ID(1)) == 1 && d.Streams(dockertest.ID(2)) == 1 })

	d.Body("/containers/json", listOf(map[int]bool{1: true, 2: false}))
	snap(t, c)
	waitFor(t, func() bool { return d.Streams(dockertest.ID(2)) == 0 })
	if open, _ := c.Streams(); open != 1 || d.Streams(dockertest.ID(1)) != 1 {
		t.Fatalf("after a stop: %d streams, daemon sees %d for the running one", open, d.Streams(dockertest.ID(1)))
	}

	d.Body("/containers/json", listOf(map[int]bool{1: true, 2: true}))
	s := snap(t, c)
	if open, _ := c.Streams(); open != 2 || len(s.Partial) != 0 {
		t.Fatalf("after a start: %d streams, partial %v", open, s.Partial)
	}
	for _, r := range s.containers {
		if !r.statsOK {
			t.Fatalf("the started container has no stats in the snapshot that opened its stream: %+v", r)
		}
	}
	all, bad := d.Requests()
	if len(bad) > 0 {
		t.Fatalf("non-read requests: %v", bad)
	}
	stats := 0
	for _, r := range all {
		if strings.HasSuffix(r, "/stats") {
			stats++
		}
	}
	if stats != 3 {
		t.Fatalf("%d stats requests for three stream opens", stats)
	}
}

// Close ends every stream before it returns.
func TestCloseEndsStreams(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	o := Options{Host: "unix://" + path}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	d.Body("/containers/json", listOf(map[int]bool{1: true, 2: true}))
	snap(t, c)
	c.Close()
	waitFor(t, func() bool { return d.Streams(dockertest.ID(1)) == 0 && d.Streams(dockertest.ID(2)) == 0 })
}

// Docker going away closes the streams instead of leaving them to retry on their own.
func TestUnavailableDockerClosesStreams(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	c := newTestCollector(t, path, nil, Options{})
	d.Body("/containers/json", listOf(map[int]bool{1: true}))
	snap(t, c)
	d.Fail("/containers/json", 500)
	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("a failed list must fail the snapshot")
	}
	if open, _ := c.Streams(); open != 0 {
		t.Fatalf("%d streams left open", open)
	}
}

// One record read by two ticks gives that record's own rates, not a missing rate.
func TestARecordSeenTwiceKeepsItsRates(t *testing.T) {
	c := newCollector(nil, Options{})
	at := time.Unix(1_800_000_000, 0).UTC()
	r := raw{id: "a", name: "web", state: "running", statsOK: true, read: at, cpuTotal: 5, cpuSystem: 9,
		cpuNow: 12.5, cpuNowOK: true, rxNow: 300, txNow: 40, netNowOK: true}
	a := Snapshot{At: at, infoKnown: true, containers: []raw{r}}
	b := Snapshot{At: at.Add(time.Second), infoKnown: true, containers: []raw{r}}
	web := byName(c.Derive(a, b))["web"]
	if web.CPU != 12.5 || web.RxBps != 300 || web.TxBps != 40 || web.Missing != nil {
		t.Fatalf("same record twice: %+v", web)
	}
	r2 := r
	r2.read, r2.cpuTotal, r2.cpuSystem, r2.rx = at.Add(2*time.Second), 5+2, 9+8, 1000
	b.containers = []raw{r2}
	// 1 online CPU: 100 * 2/8 = 25%; 1000 bytes over the 2 s between the records, whatever the tick gap.
	if web := byName(c.Derive(a, b))["web"]; web.CPU != 25 || web.RxBps != 500 {
		t.Fatalf("two records: %+v", web)
	}
}

// The stats endpoint is opened as a stream, and the stream's records stay under the per-record cap.
func TestStatsAreAskedAsAStream(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	c := newTestCollector(t, path, nil, Options{})
	d.Body("/containers/json", listOf(map[int]bool{1: true}))
	d.StatsEvery(100 * time.Microsecond)
	snap(t, c)
	n := d.Frames(dockertest.ID(1))
	waitFor(t, func() bool { return d.Frames(dockertest.ID(1)) > n+uint64(statsCap.nodes) })
	if s := snap(t, c); len(s.Partial) != 0 || collect.ErrorCode(s.containers[0].statsErr) != "" {
		t.Fatalf("a long stream tripped its cap: %v", s.Partial)
	}
	if _, opens := c.Streams(); opens != 1 {
		t.Fatalf("%d opens for one healthy stream", opens)
	}
}
