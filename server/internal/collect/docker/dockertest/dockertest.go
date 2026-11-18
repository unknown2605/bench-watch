// Package dockertest is a fake Docker daemon on a Unix socket for tests, with privacy canaries in every field.
package dockertest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Canary is planted in every field the collector must never keep.
const Canary = "CANARY"

// UnsafeNames are the two container names the default gate refuses; their IDs are ID(5) and ID(6).
var UnsafeNames = []string{"Canary Secret Token Value Here", "123e4567-e89b-12d3-a456-426614174000"}

// IDPrefix starts every fake container ID, so a leaked ID is found like a canary.
const IDPrefix = "ca7a1d"

// ID returns the n-th fake container ID: 64 lowercase hex digits, the shape of a real one.
func ID(n int) string { return fmt.Sprintf("%s%058x", IDPrefix, n) }

// Daemon answers the Docker read endpoints on a Unix socket and records every request.
type Daemon struct {
	t    *testing.T
	path string
	srv  *httptest.Server

	mu      sync.Mutex
	reqs    []string
	bad     []string
	calls   map[string]uint64
	fail    map[string]int
	hang    map[string]bool
	delay   map[string]time.Duration
	pad     map[string]int
	body    map[string]string
	filters []string
	since   []string
	quiet   bool
	flood   bool
	lines   bool
	seq     bool
	// frames counts stats records sent per container over all its streams; every is the gap between records.
	frames  map[string]uint64
	every   time.Duration
	streams map[string]int
	cut     map[string]chan struct{}
}

// ShortSocket returns a socket path under /tmp, because a Unix socket path must stay under 104 bytes.
func ShortSocket(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "cmdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

func Start(t *testing.T, path string) *Daemon {
	f := &Daemon{t: t, path: path, calls: map[string]uint64{}, fail: map[string]int{}, hang: map[string]bool{},
		delay: map[string]time.Duration{}, pad: map[string]int{}, body: map[string]string{},
		frames: map[string]uint64{}, every: 10 * time.Millisecond, streams: map[string]int{}, cut: map[string]chan struct{}{}}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.srv.Listener = ln
	f.srv.Start()
	return f
}

// Stop closes the listener and every open connection, as when Docker Desktop quits.
func (f *Daemon) Stop() {
	f.srv.CloseClientConnections()
	f.srv.Close()
	os.Remove(f.path)
}

// Fail answers requests whose path starts with prefix with status (0 clears it); the body carries a canary.
// The verbose disk call is "/system/df?verbose" here, for Fail, Pad, Body and the other switches.
func (f *Daemon) Fail(prefix string, status int) {
	f.mu.Lock()
	f.fail[prefix] = status
	f.mu.Unlock()
}

// Hang holds requests whose path starts with prefix until the client gives up.
func (f *Daemon) Hang(prefix string, on bool) {
	f.mu.Lock()
	f.hang[prefix] = on
	f.mu.Unlock()
}

// Delay holds answers to paths starting with prefix for d (0 clears it).
func (f *Daemon) Delay(prefix string, d time.Duration) {
	f.mu.Lock()
	f.delay[prefix] = d
	f.mu.Unlock()
}

// Pad sends n bytes of JSON whitespace before the body of paths starting with prefix.
func (f *Daemon) Pad(prefix string, n int) {
	f.mu.Lock()
	f.pad[prefix] = n
	f.mu.Unlock()
}

// Flood makes the event stream send one line that never ends.
func (f *Daemon) Flood(on bool) {
	f.mu.Lock()
	f.flood = on
	f.mu.Unlock()
}

// FloodLines makes the event stream send one event that never ends, as up to 64 MiB of short lines.
func (f *Daemon) FloodLines(on bool) {
	f.mu.Lock()
	f.lines = on
	f.mu.Unlock()
}

// FloodSeq sends the event stream as json-seq: one string of escaped quotes and braces split by record
// separators, which a tracker that kept the separators would read as many small values.
func (f *Daemon) FloodSeq(on bool) {
	f.mu.Lock()
	f.seq = on
	f.mu.Unlock()
}

// StatsEvery sets the gap between two records of a stats stream (10 ms by default).
func (f *Daemon) StatsEvery(d time.Duration) {
	f.mu.Lock()
	f.every = d
	f.mu.Unlock()
}

// Frames is how many stats records the container with this ID has been sent.
func (f *Daemon) Frames(id string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frames[id]
}

// Streams is how many stats streams of this ID are open now.
func (f *Daemon) Streams(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams[id]
}

// Cut ends every open stats stream of this ID, as when the daemon drops the connection.
func (f *Daemon) Cut(id string) {
	f.mu.Lock()
	if ch, ok := f.cut[id]; ok {
		close(ch)
		delete(f.cut, id)
	}
	f.mu.Unlock()
}

// Body: an empty body restores the default answer; p must match exactly, not as a prefix.
func (f *Daemon) Body(p, body string) {
	f.mu.Lock()
	f.body[p] = body
	f.mu.Unlock()
}

func (f *Daemon) EventSince() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.since...)
}

func (f *Daemon) Quiet(on bool) {
	f.mu.Lock()
	f.quiet = on
	f.mu.Unlock()
}

func (f *Daemon) Requests() (all, bad []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...), append([]string(nil), f.bad...)
}

func (f *Daemon) EventFilters() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.filters...)
}

func (f *Daemon) match(m map[string]int, p string) int {
	for k, v := range m {
		if v != 0 && strings.HasPrefix(p, k) {
			return v
		}
	}
	return 0
}

func (f *Daemon) serve(w http.ResponseWriter, r *http.Request) {
	p := version.ReplaceAllString(r.URL.Path, "")
	if p == "/system/df" && r.URL.Query().Get("verbose") == "1" {
		p += "?verbose"
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+p)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		f.bad = append(f.bad, r.Method+" "+p)
	}
	f.calls[p]++
	n := f.calls[p]
	status := f.match(f.fail, p)
	hang := false
	for k, v := range f.hang {
		hang = hang || (v && strings.HasPrefix(p, k))
	}
	quiet, flood, lines, seq, pad, body := f.quiet, f.flood, f.lines, f.seq, f.match(f.pad, p), f.body[p]
	var delay time.Duration
	for k, v := range f.delay {
		if strings.HasPrefix(p, k) {
			delay = max(delay, v)
		}
	}
	if p == "/events" {
		f.filters = append(f.filters, r.URL.Query().Get("filters"))
		f.since = append(f.since, r.URL.Query().Get("since"))
	}
	f.mu.Unlock()
	if hang {
		<-r.Context().Done()
		return
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Api-Version", "1.56")
	w.Header().Set("Content-Type", "application/json")
	if pad > 0 {
		w.Write([]byte(strings.Repeat(" ", pad)))
	}
	if status != 0 {
		w.WriteHeader(status)
		w.Write([]byte(`{"message":"CANARY_ERROR /Users/x/CANARY"}`))
		return
	}
	switch {
	case body != "":
		w.Write([]byte(body))
	case p == "/_ping":
		w.Write([]byte("OK"))
	case p == "/containers/json":
		w.Write([]byte(containersJSON))
	case strings.HasPrefix(p, "/containers/") && strings.HasSuffix(p, "/stats") && r.URL.Query().Get("stream") == "true":
		f.streamStats(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "/containers/"), "/stats"))
	case strings.HasPrefix(p, "/containers/") && strings.HasSuffix(p, "/stats"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/containers/"), "/stats")
		c := counters[id]
		fmt.Fprintf(w, statsJSON, id, c.base+n*c.cpuStep, n*1e10, n*c.netStep, n*c.netStep/2)
	case p == "/events":
		w.Header().Set("Content-Type", "application/x-ndjson")
		if seq {
			w.Header().Set("Content-Type", "application/json-seq")
		}
		w.WriteHeader(http.StatusOK)
		switch {
		case seq:
			w.Write([]byte("\x1e" + `{"Type":"container","Action":"`))
			chunk := []byte(strings.Repeat("\\\x1e\"}", 4096))
			for sent := 0; sent < 64<<20 && r.Context().Err() == nil; sent += len(chunk) {
				if _, err := w.Write(chunk); err != nil {
					break
				}
			}
		case flood:
			w.Write([]byte(`{"Type":"container","Action":"` + strings.Repeat("a", 1<<20)))
		case lines:
			w.Write([]byte(`{"Type":"container","Action":"start","Actor":{"ID":"x","Attributes":{` + "\n"))
			line := []byte(`"k":"` + strings.Repeat("v", 1000) + `",` + "\n")
			for sent := 0; sent < 64<<20 && r.Context().Err() == nil; sent += len(line) {
				if _, err := w.Write(line); err != nil {
					break
				}
			}
		case !quiet:
			now := time.Now().UnixNano()
			for i, ev := range events {
				fmt.Fprintf(w, ev+"\n", now-int64(len(events)-i)*int64(time.Second))
			}
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	case p == "/system/df":
		w.Write([]byte(`{"ImageUsage":{"TotalSize":3000,"TotalCount":3,"ActiveCount":1,"Reclaimable":1200},` +
			`"VolumeUsage":{"TotalSize":2000,"TotalCount":2,"ActiveCount":1,"Reclaimable":700},` +
			`"BuildCacheUsage":{"TotalSize":1000,"Reclaimable":300},"ContainerUsage":{"TotalSize":500}}`))
	case p == "/system/df?verbose":
		w.Write([]byte(`{"ImageUsage":{"TotalSize":3000,"TotalCount":3,"ActiveCount":1,"Reclaimable":1200,` +
			`"Items":[{"Id":"sha256:CANARY_DIGEST","RepoTags":["CANARY_TAG"],"Size":1700},` +
			`{"Id":"b","Size":900},{"Id":"c","Size":400}]}}`))
	case p == "/info":
		w.Write([]byte(`{"NCPU":8,"MemTotal":8000000000,"Name":"CANARY_HOST",` +
			`"HTTPProxy":"http://user:CANARY_PROXY@proxy:3128",` +
			`"Labels":["CANARY_LABEL"],"DockerRootDir":"/var/lib/CANARY"}`))
	default:
		f.mu.Lock()
		f.bad = append(f.bad, r.Method+" "+p)
		f.mu.Unlock()
		http.NotFound(w, r)
	}
}

// StatsEpoch is the daemon time of record 0; record k is read k seconds later, whatever the real gap.
var StatsEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// streamStats sends one record per gap until the client leaves or Cut; record k carries k steps of each counter.
func (f *Daemon) streamStats(w http.ResponseWriter, r *http.Request, id string) {
	f.mu.Lock()
	f.streams[id]++
	cut, ok := f.cut[id]
	if !ok {
		cut = make(chan struct{})
		f.cut[id] = cut
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.streams[id]--
		f.mu.Unlock()
	}()
	c := counters[id]
	for {
		f.mu.Lock()
		f.frames[id]++
		k, every := f.frames[id], f.every
		f.mu.Unlock()
		read := StatsEpoch.Add(time.Duration(k) * time.Second).Format(time.RFC3339Nano)
		fmt.Fprintf(w, streamJSON, id, read, c.base+k*c.cpuStep, k*1e10, c.base+(k-1)*c.cpuStep, (k-1)*1e10,
			k*c.netStep, k*c.netStep/2)
		w.Write([]byte("\n"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-cut:
			return
		case <-time.After(every):
		}
	}
}

var version = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

// counters give each container its own stats series: the n-th call returns base + n*cpuStep CPU,
// n*1e10 system CPU and n*netStep received bytes. Bases far apart make a mix-up between containers obvious.
var counters = map[string]struct{ base, cpuStep, netStep uint64 }{
	ID(1): {0, 4e8, 1000},
	ID(2): {1e9, 2e8, 0},
	ID(3): {2e9, 1e8, 0},
	ID(5): {5e12, 4e8, 2000},
	ID(6): {9e15, 8e8, 4000},
}

// StatsCPU is the CPU percent each container shows between two consecutive stats calls (4 online CPUs).
var StatsCPU = map[string]float64{"web": 16, "worker": 8, "sidecar": 4, "other": 16 + 32}

// events are replayed on every stream open, stamped one second apart and ending now.
var events = []string{
	event("container", "die", ID(1), `"name":"web","image":"CANARY_IMAGE","secret.label":"CANARY_LABEL"`),
	event("container", "start", ID(1), `"name":"web","com.docker.compose.project":"shop"`),
	event("container", "exec_start: sh -c 'echo CANARY_EXEC'", ID(1), `"name":"web","execID":"CANARY_EXECID"`),
	event("container", "health_status: healthy", ID(1), `"name":"web"`),
	event("network", "connect", "CANARY_NET", `"name":"CANARY_NETNAME"`),
	event("container", "die", ID(5), `"name":"Canary Secret Token Value Here"`),
	event("container", "start", ID(5), `"name":"Canary Secret Token Value Here"`),
	event("container", "die", ID(9), `"name":"gone"`),
	event("container", "start", ID(9), `"name":"gone"`),
}

// event is one stream line; timeNano stays a %d verb for the replay.
func event(typ, action, id, attrs string) string {
	return `{"Type":"` + typ + `","Action":"` + action + `","Actor":{"ID":"` + id + `","Attributes":{` + attrs +
		`}},"timeNano":%d}`
}

var containersJSON = mustJSON([]map[string]any{
	{
		"Id": ID(1), "Names": []string{"/web"}, "Image": "CANARY_IMAGE:latest", "ImageID": "sha256:CANARY_DIGEST",
		"Command": "sh -c 'export TOKEN=CANARY_TOKEN; serve'", "Created": 1, "State": "running",
		"Status": "Up 3 hours (healthy)",
		"Labels": map[string]string{
			"com.docker.compose.project": "shop", "com.docker.compose.service": "CANARY_SERVICE",
			"secret.label": "CANARY_LABEL", "com.docker.compose.project.config_files": "/Users/x/CANARY/compose.yml",
		},
		"Health": map[string]any{"Status": "healthy", "FailingStreak": 0},
		"Ports":  []map[string]any{{"IP": "127.0.0.1", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"}},
		"Mounts": []map[string]any{{"Type": "bind", "Source": "/Users/x/CANARY_MOUNT", "Destination": "/app"}},
		"NetworkSettings": map[string]any{"Networks": map[string]any{"stack_full": map[string]any{
			"IPAddress": "172.18.0.2", "Aliases": []string{"CANARY_ALIAS"}}}},
		"HostConfig": map[string]any{"NetworkMode": "stack_full"},
	},
	{
		"Id": ID(2), "Names": []string{"/worker"}, "Command": "CANARY_CMD", "State": "running",
		"Status": "Up About an hour",
		"Labels": map[string]string{"com.docker.compose.project": "shop", "CANARY_KEY": "x"},
		"NetworkSettings": map[string]any{"Networks": map[string]any{
			"stack_full": map[string]any{}, "other": map[string]any{}}},
		"HostConfig": map[string]any{"NetworkMode": "stack_full"},
	},
	{
		"Id": ID(3), "Names": []string{"/sidecar"}, "Command": "CANARY_CMD", "State": "running", "Status": "Up 5 minutes",
		"NetworkSettings": map[string]any{"Networks": map[string]any{"stack_full": map[string]any{}}},
		"HostConfig":      map[string]any{"NetworkMode": "container:" + ID(1)},
	},
	{
		"Id": ID(4), "Names": []string{"/old"}, "Command": "CANARY_CMD", "State": "exited",
		"Status": "Exited (0) 2 hours ago",
		"Labels": map[string]string{"secret": "CANARY_SECRET"},
	},
	{
		"Id": ID(5), "Names": []string{"/" + UnsafeNames[0]}, "State": "running", "Status": "Up 2 minutes",
		"Labels":     map[string]string{"com.docker.compose.project": "CANARY_STACK_" + UnsafeNames[1]},
		"HostConfig": map[string]any{"NetworkMode": "bridge"},
	},
	{
		"Id": ID(6), "Names": []string{"/" + UnsafeNames[1]}, "State": "running", "Status": "Up 9 minutes",
		"HostConfig": map[string]any{"NetworkMode": "bridge"},
	},
})

const statsJSON = `{"name":"/CANARY_NAME","id":"%s",
 "cpu_stats":{"cpu_usage":{"total_usage":%d},"system_cpu_usage":%d,"online_cpus":4},
 "memory_stats":{"usage":300000000,"limit":1000000000,"stats":{"inactive_file":100000000}},
 "networks":{"eth0":{"rx_bytes":%d,"tx_bytes":%d,"endpoint_id":"CANARY_ENDPOINT"}}}`

const streamJSON = `{"name":"/CANARY_NAME","id":"%s","read":"%s",
 "cpu_stats":{"cpu_usage":{"total_usage":%d},"system_cpu_usage":%d,"online_cpus":4},
 "precpu_stats":{"cpu_usage":{"total_usage":%d},"system_cpu_usage":%d,"online_cpus":4},
 "memory_stats":{"usage":300000000,"limit":1000000000,"stats":{"inactive_file":100000000}},
 "networks":{"eth0":{"rx_bytes":%d,"tx_bytes":%d,"endpoint_id":"CANARY_ENDPOINT"}}}`

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// StartSilent accepts connections on path and never answers, like a hung daemon.
func StartSilent(t *testing.T, path string) (stop func()) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	return func() {
		ln.Close()
		<-done
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		os.Remove(path)
	}
}
