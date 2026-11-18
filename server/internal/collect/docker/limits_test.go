package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/clocktest"
	"benchwatch/server/internal/collect/docker/dockertest"
)

func readAll(body string, c bodyCap) (int, error) {
	rc := io.NopCloser(strings.NewReader(body))
	b, err := io.ReadAll(&cappedBody{rc: rc, cap: c})
	return len(b), err
}

func TestCappedBody(t *testing.T) {
	var tl tooLargeError
	whole := bodyCap{bytes: 100}
	if n, err := readAll(strings.Repeat(" ", 100), whole); n != 100 || err != nil {
		t.Fatalf("a body at the limit: %d %v", n, err)
	}
	_, err := readAll(strings.Repeat(" ", 101), whole)
	if !errors.As(err, &tl) || collect.ErrorCode(err) != "ETOOLARGE" {
		t.Fatalf("a body past the limit: %v", err)
	}
	perValue := bodyCap{bytes: 100, perValue: true}
	values := strings.Repeat(`{"a":"`+strings.Repeat("y", 91)+`"}`+"\n", 1000)
	if n, err := readAll(values, perValue); n != len(values) || err != nil {
		t.Fatalf("values at the limit: %d %v", n, err)
	}
	if _, err := readAll(values+`{"a":"`+strings.Repeat("z", 101)+`"}`, perValue); collect.ErrorCode(err) != "ETOOLARGE" {
		t.Fatalf("a value past the limit: %v", err)
	}
	split := "{\n" + strings.Repeat(`"k":1,`+"\n", 20) + `"k":1}`
	if _, err := readAll(split, perValue); collect.ErrorCode(err) != "ETOOLARGE" {
		t.Fatalf("a value split over short lines: %v", err)
	}
	separated := `{"a":"` + strings.Repeat(backslash+"\x1e\"}", 100)
	if _, err := readAll(separated, perValue); collect.ErrorCode(err) != "ETOOLARGE" {
		t.Fatalf("a string whose escapes sit before record separators: %v", err)
	}
	quoted := `{"a":"` + backslash + `"}` + strings.Repeat(",", 200) + `"}`
	if n, err := readAll(quoted, bodyCap{bytes: 1000, nodes: 2}); n != len(quoted) || err != nil {
		t.Fatalf("commas inside a string with an escaped quote are not nodes: %d %v", n, err)
	}
	nodes := bodyCap{bytes: 1000, nodes: 10}
	if n, err := readAll("["+strings.Repeat("{},", 4)+"{}]", nodes); n == 0 || err != nil {
		t.Fatalf("10 nodes: %d %v", n, err)
	}
	for _, body := range []string{"[" + strings.Repeat("{},", 5) + "{}]", strings.Repeat("[", 11)} {
		if _, err := readAll(body, nodes); !errors.As(err, &tl) || tl.limit != 10 {
			t.Fatalf("past the node limit: %v", err)
		}
	}
}

const backslash = "\x5c"

func TestOversizedResponsesFail(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	c := newTestCollector(t, path, nil, Options{})
	d.Pad("/containers/json", int(listCap.bytes)+1)
	if _, err := c.Snapshot(context.Background()); collect.ErrorCode(err) != "ETOOLARGE" {
		t.Fatalf("oversized list: %v", err)
	}
	d.Pad("/containers/json", 0)
	d.Pad("/info", int(infoCap.bytes)+1)
	d.Pad("/containers/"+dockertest.ID(1)+"/stats", int(statsCap.bytes)+1)
	d.Pad("/system/df", int(diskCap.bytes)+1)
	codes := map[string]string{}
	waitFor(t, func() bool {
		s, err := c.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range s.Partial {
			codes[p.Part] = p.Code
		}
		return codes[collect.PartDisk] != ""
	})
	if codes[collect.PartInfo] != "ETOOLARGE" || codes[collect.PartStats] != "ETOOLARGE" ||
		codes[collect.PartDisk] != "ETOOLARGE" {
		t.Fatalf("partial %v", codes)
	}
}

// CM9-L8: headers are capped like bodies, and plain-HTTP hosts must be loopback.
func TestTransportLimits(t *testing.T) {
	for host, ok := range map[string]bool{"tcp://127.0.0.1:2375": true, "tcp://localhost:2375": true,
		"http://[::1]:2375": true, "tcp://10.1.2.3:2375": false, "tcp://docker.example:2375": false} {
		if _, _, err := newClient(host); (err == nil) != ok {
			t.Errorf("%s: %v", host, err)
		}
	}
	path := dockertest.ShortSocket(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Pad", strings.Repeat("h", 256<<10))
		w.Write([]byte("OK"))
	})}
	go srv.Serve(ln)
	defer srv.Close()
	cli, _, err := newClient("unix://" + path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Ping(context.Background(), client.PingOptions{}); err == nil {
		t.Fatal("a 256 KiB header was accepted")
	}
}

// allocated counts TotalAlloc, so memory the GC freed during f still counts.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func emptyObjects(size int) string {
	return "[" + strings.Repeat("{},", size/3-1) + "{}]"
}

func TestAListOfEmptyObjectsIsRefusedWithinItsCap(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	c := newTestCollector(t, path, nil, Options{})
	d.Body("/containers/json", emptyObjects(8<<20))
	var err error
	if grew := allocated(func() { _, err = c.Snapshot(context.Background()) }); grew > 64<<20 {
		t.Fatalf("an 8 MiB list of {} allocated %d MiB", grew>>20)
	}
	if collect.ErrorCode(err) != "ETOOLARGE" {
		t.Fatalf("an 8 MiB list of {}: %v", err)
	}
	d.Body("/containers/json", "")
	d.Body("/system/df", `{"ContainerUsage":{"Items":`+emptyObjects(16<<20-40)+`}}`)
	var s Snapshot
	grew := allocated(func() {
		waitFor(t, func() bool {
			s, err = c.Snapshot(context.Background())
			disk := func(p collect.PartError) bool { return p.Part == collect.PartDisk }
			return err != nil || slices.ContainsFunc(s.Partial, disk)
		})
	})
	if err != nil || grew > 64<<20 {
		t.Fatalf("a 16 MiB disk report of {}: %v, %d MiB allocated", err, grew>>20)
	}
	for _, p := range s.Partial {
		if p.Part == collect.PartDisk && p.Code != "ETOOLARGE" {
			t.Fatalf("disk partial %v", p)
		}
	}
}

func TestAnEventSplitOverShortLinesFails(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	d.FloodLines(true)
	c := newTestCollector(t, path, nil, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := collect.NewStatus(nil)
	var st collect.SourceStatus
	grew := allocated(func() {
		go c.RunEvents(ctx, status, nil)
		waitFor(t, func() bool {
			st = status.Get(collect.SourceDockerEvents)
			return st.LastError != ""
		})
	})
	if st.State != collect.StateUnavailable || st.LastError != "ETOOLARGE" || grew > 16<<20 {
		t.Fatalf("status %s %s after %d MiB", st.State, st.LastError, grew>>20)
	}
}

// SEC9-H1: the client strips 0x1e from a json-seq stream, so the cap must not end the string at the quote after it.
func TestARecordSeparatedEventHitsItsCap(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	d.FloodSeq(true)
	c := newTestCollector(t, path, nil, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := collect.NewStatus(nil)
	var st collect.SourceStatus
	grew := allocated(func() {
		go c.RunEvents(ctx, status, nil)
		waitFor(t, func() bool {
			st = status.Get(collect.SourceDockerEvents)
			return st.LastError != ""
		})
	})
	if st.State != collect.StateUnavailable || st.LastError != "ETOOLARGE" || grew > 16<<20 {
		t.Fatalf("status %s %s after %d MiB", st.State, st.LastError, grew>>20)
	}
}

func TestEndlessEventLineFails(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	d.Flood(true)
	c := newTestCollector(t, path, nil, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := collect.NewStatus(nil)
	go c.RunEvents(ctx, status, nil)
	waitFor(t, func() bool {
		st := status.Get(collect.SourceDockerEvents)
		return st.State == collect.StateUnavailable && st.LastError == "ETOOLARGE"
	})
}

func TestEventsOpenIsBounded(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	d.Hang("/events", true)
	c := newTestCollector(t, path, nil, Options{CallTimeout: 300 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := collect.NewStatus(nil)
	go c.RunEvents(ctx, status, nil)
	waitFor(t, func() bool {
		st := status.Get(collect.SourceDockerEvents)
		return st.State == collect.StateUnavailable && st.LastError == "ETIMEDOUT"
	})
}

func TestEachCallHasItsOwnBudget(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	limit := time.Second
	c := newTestCollector(t, path, nil, Options{CallTimeout: limit})
	d.Delay("/containers/json", 600*time.Millisecond)
	d.Delay("/containers/"+dockertest.IDPrefix, 600*time.Millisecond)
	start := time.Now()
	s, err := c.Snapshot(context.Background())
	if took := time.Since(start); err != nil || len(s.Partial) != 0 || took < limit {
		t.Fatalf("calls that each fit their budget: err %v partial %v after %v", err, s.Partial, took)
	}
	c2 := newTestCollector(t, path, nil, Options{CallTimeout: limit, SnapshotTimeout: limit})
	start = time.Now()
	s, err = c2.Snapshot(context.Background())
	if took := time.Since(start); err != nil || len(s.Partial) == 0 || s.Partial[0].Code != "ETIMEDOUT" ||
		took > 2*limit {
		t.Fatalf("snapshot budget: err %v partial %v after %v", err, s.Partial, took)
	}
}

func TestSnapshotTimesAreUTC(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	zone := time.FixedZone("x", 5*3600)
	c := newTestCollector(t, path, nil, Options{Now: func() time.Time { return time.Now().In(zone) }})
	s, err := c.Snapshot(context.Background())
	if err != nil || s.At.Location() != time.UTC {
		t.Fatalf("snapshot at %v: %v", s.At, err)
	}
}

func TestAReconnectNeverAsksForEventsAfterNow(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	behind := func() time.Time { return time.Now().Add(-time.Hour) }
	c := newTestCollector(t, path, nil, Options{Now: behind, IdleTimeout: 300 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.RunEvents(ctx, collect.NewStatus(nil), nil)
	waitFor(t, func() bool { return len(d.EventSince()) >= 3 })
	cancel()
	for i, since := range d.EventSince() {
		if n, err := strconv.ParseInt(since, 10, 64); err != nil || n > behind().Unix() {
			t.Fatalf("stream %d asked since %s, after now %d", i, since, behind().Unix())
		}
	}
}

func TestEventTimesOutsideTheStreamAreClamped(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	c := newCollector(nil, Options{Now: func() time.Time { return now }})
	c.events.floor = now.Add(-time.Hour).UnixNano()
	far := msg("a", "start", now)
	far.TimeNano, far.Time = 0, 1<<62
	if ev, ok := c.apply(far); !ok || !ev.At.Equal(now.Add(clockSkew)) {
		t.Fatalf("seconds past the int64 nanosecond range: %v", ev.At)
	}
	early := msg("b", "start", now)
	early.TimeNano = -5
	if ev, ok := c.apply(early); !ok || !ev.At.Equal(now.Add(-time.Hour)) {
		t.Fatalf("a time before the stream start: %v", ev.At)
	}
}

func TestAReusedIDKeepsItsNewEntry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newCollector(nil, Options{Now: func() time.Time { return now }})
	tick := 0
	at := func() time.Time { tick++; return now.Add(time.Duration(tick)) }
	c.apply(msg("A", "start", at()))
	c.apply(msg("A", "destroy", at()))
	for i := range maxIDs - 1 {
		c.apply(msg(fmt.Sprintf("id%d", i), "start", at()))
	}
	c.apply(msg("A", "start", at()))
	c.apply(msg("one-more", "start", at()))
	if _, ok := c.events.seen[hexID("A")]; !ok || len(c.events.seen) != maxIDs {
		t.Fatalf("a restarted container lost its state: tracked %v, %d IDs", ok, len(c.events.seen))
	}
	if _, ok := c.events.seen[hexID("id0")]; ok {
		t.Fatal("the oldest live ID must go first")
	}
}

func TestSnapshotPrunesEventState(t *testing.T) {
	path := dockertest.ShortSocket(t)
	d := dockertest.Start(t, path)
	defer d.Stop()
	clk := clocktest.New(time.Now())
	c := newTestCollector(t, path, clk, Options{})
	c.apply(msg("zz", "start", clk.Now()))
	clk.Advance(2 * pruneMargin)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.events.seen[hexID("zz")]; ok {
		t.Fatal("a container gone from the list must be forgotten")
	}
}

func TestEventTimesAreClampedAndDeduped(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.FixedZone("x", 3600))
	c := newCollector(nil, Options{Now: func() time.Time { return now }})
	far, ok := c.apply(msg("a", "start", now.Add(time.Hour)))
	if !ok || !far.At.Equal(now.Add(clockSkew)) || far.At.Location() != time.UTC {
		t.Fatalf("future event at %v", far.At)
	}
	if _, ok := c.apply(msg("a", "start", now.Add(time.Hour))); ok {
		t.Fatal("a replayed event must be dropped")
	}
	huge := msg("b", "die", now)
	huge.TimeNano = 1<<63 - 1
	if ev, ok := c.apply(huge); !ok || ev.At.After(now.Add(clockSkew)) {
		t.Fatalf("maximum time: %v %v", ev.At, ok)
	}
	if _, ok := c.apply(msg("c", "start", now.Add(-time.Second))); !ok {
		t.Fatal("a later event must not be blocked by an earlier future time")
	}
	if _, ok := c.apply(msg("d", "start", now.Add(-time.Second))); !ok {
		t.Fatal("events of two containers at the same time are both kept")
	}
	if last := c.events.last(); last > now.Add(clockSkew).UnixNano() {
		t.Fatalf("resume point %v", time.Unix(0, last))
	}
}

func TestEventStateIsCapped(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newCollector(nil, Options{Now: func() time.Time { return now }})
	for i := range 3 * maxIDs {
		c.apply(msg(fmt.Sprintf("id%d", i), "start", now.Add(time.Duration(i))))
	}
	e := &c.events
	if len(e.seen) != maxIDs || len(e.started) != maxIDs || len(e.order) > 2*maxIDs || len(e.recent) != maxRecent ||
		len(e.recentQ) != maxRecent {
		t.Fatalf("seen %d started %d order %d recent %d", len(e.seen), len(e.started), len(e.order), len(e.recent))
	}
	if _, ok := e.seen[hexID(fmt.Sprintf("id%d", 3*maxIDs-1))]; !ok {
		t.Fatal("the newest container must stay")
	}
}

// SEC9-M1: an ID not of the daemon's shape is never kept, so long IDs cannot grow the event state.
func TestEventIDsOfAnotherShapeAreNotTracked(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newCollector(nil, Options{Now: func() time.Time { return now }})
	hex64 := hexID("x")
	for i, id := range []string{"", "abc", strings.ToUpper(hex64), hex64[:63], hex64 + "0", "g" + hex64[1:]} {
		m := msg("x", "start", now.Add(time.Duration(i)))
		m.Actor.ID = id
		if _, ok := c.apply(m); !ok {
			t.Fatalf("an event with an ID of another shape must still count, %d", i)
		}
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := range 3000 {
		m := msg("x", "start", now.Add(time.Duration(10+i)))
		m.Actor.ID = strings.Repeat("a", 32<<10) + strconv.Itoa(i)
		c.apply(m)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if len(c.events.seen) != 0 || len(c.events.order) != 0 || after.HeapAlloc > before.HeapAlloc+8<<20 {
		t.Fatalf("tracked %d IDs, heap grew %d MiB", len(c.events.seen), (int64(after.HeapAlloc)-int64(before.HeapAlloc))>>20)
	}
}

func TestDestroyedIDsLeaveNoStaleOrder(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := newCollector(nil, Options{Now: func() time.Time { return now }})
	tick := 0
	at := func() time.Time { tick++; return now.Add(time.Duration(tick)) }
	c.apply(msg("kept", "start", at()))
	for i := range 3 * maxIDs {
		c.apply(msg(fmt.Sprintf("id%d", i), "create", at()))
		c.apply(msg(fmt.Sprintf("id%d", i), "destroy", at()))
	}
	if e := &c.events; len(e.seen) != 1 || len(e.order) > 2*len(e.seen)+64 {
		t.Fatalf("%d live IDs keep %d order entries", len(e.seen), len(e.order))
	}
}

func TestContainerRowsAreCapped(t *testing.T) {
	c := newCollector(nil, Options{})
	t0 := time.Unix(1_800_000_000, 0).UTC()
	mk := func(at time.Time, step uint64) Snapshot {
		s := Snapshot{At: at, infoKnown: true}
		for i := range 100 {
			state := "running"
			if i%2 == 1 {
				state = "exited"
			}
			s.containers = append(s.containers, raw{id: fmt.Sprintf("id%d", i), name: fmt.Sprintf("app%03d", i),
				state: state, statsOK: true, cpuTotal: step, cpuSystem: step * 100, memUsed: 1})
		}
		s.containers = append(s.containers,
			raw{id: "real", name: collect.Other, state: "running", statsOK: true, cpuTotal: step, cpuSystem: step * 100},
			raw{id: "tok", name: dockertest.UnsafeNames[1], state: "running", statsOK: true, cpuTotal: step,
				cpuSystem: step * 100},
			raw{id: "new", name: "fresh", state: "running", statsOK: true})
		return s
	}
	a, b := mk(t0, 1), mk(t0.Add(10*time.Second), 2)
	a.containers = a.containers[:len(a.containers)-1]
	s := c.Derive(a, b)
	others, real, running := 0, false, 0
	var other collect.Container
	for _, ct := range s.Containers {
		switch ct.Name {
		case collect.Other:
			others++
			other = ct
		case collect.OtherReal:
			real = true
		}
		if ct.State == "running" && ct.Name != collect.Other {
			running++
		}
	}
	if len(s.Containers) != collect.MaxRows || others != 1 || !real {
		t.Fatalf("%d rows, %d other rows, real other row %v", len(s.Containers), others, real)
	}
	if other.Count != 1+102-(collect.MaxRows-1) || running != 52 || other.State != collect.Other {
		t.Fatalf("other %+v; every running container must keep its row, %d of 52 did", other, running)
	}
	if !slices.Contains(s.Missing, collect.PartRates) {
		t.Fatalf("a running container without rates must mark the sample: %v", s.Missing)
	}
}
