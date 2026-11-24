package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/runner"
)

func newServer(t *testing.T, db DB, r *runner.Runner) (*Server, http.Handler) {
	t.Helper()
	hub, err := NewHub()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: db, Runner: r, Hub: hub}
	h, err := Handler(s)
	if err != nil {
		t.Fatal(err)
	}
	return s, h
}

func get(h http.Handler, host, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func code(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e api.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body is not an ErrorResponse")
	}
	return string(e.Code)
}

func TestHostAllowlistRefusesOtherHosts(t *testing.T) {
	_, h := newServer(t, nil, nil)
	for _, host := range AllowedHosts {
		if rec := get(h, host, "/api/status"); rec.Code != http.StatusOK {
			t.Errorf("host %q: status %d, want 200", host, rec.Code)
		}
	}
	for _, host := range []string{"", "localhost", "localhost:8790", "127.0.0.1", "127.0.0.1:80", "evil.example",
		"benchwatch-next.localhost.evil.example", "benchwatch-next.localhost:8790", "[::1]:8790",
		"0.0.0.0:8790", "benchwatch-next.localhost."} {
		rec := get(h, host, "/api/status")
		if rec.Code != http.StatusMisdirectedRequest || code(t, rec) != "bad_request" {
			t.Errorf("host %q: status %d, want 421 bad_request", host, rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("host %q: CORS header sent", host)
		}
	}
	if rec := get(h, "BENCHWATCH-NEXT.LOCALHOST", "/api/status"); rec.Code != http.StatusOK {
		t.Errorf("host names are case-insensitive: status %d", rec.Code)
	}
}

func TestAnswersGetOnlyWithoutCORS(t *testing.T) {
	_, h := newServer(t, nil, nil)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions,
		http.MethodPatch} {
		req := httptest.NewRequest(m, "/api/status", nil)
		req.Host = AllowedHosts[0]
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed || code(t, rec) != "bad_method" {
			t.Errorf("%s: status %d, want 405 bad_method", m, rec.Code)
		}
		for k := range rec.Header() {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: CORS header %s sent", m, k)
			}
		}
	}
	rec := get(h, AllowedHosts[0], "/api/status")
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers %v", rec.Header())
	}
}

func TestWithoutDatabaseTranscriptsAnswerUnavailable(t *testing.T) {
	_, h := newServer(t, nil, nil)
	rec := get(h, AllowedHosts[0], "/api/status")
	var st api.StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if st.Database.State != api.DatabaseStateUnavailable || st.Health == api.HealthNominal {
		t.Errorf("database %s health %s", st.Database.State, st.Health)
	}
	for _, p := range []string{"/api/meta", "/api/overview", "/api/tokens", "/api/context", "/api/sessions",
		"/api/subagents", "/api/fills", "/api/guard", "/api/sessions/abcd1234?project=benchwatch"} {
		rec := get(h, AllowedHosts[0], p)
		if rec.Code != http.StatusServiceUnavailable || code(t, rec) != "unavailable" {
			t.Errorf("%s: status %d, want 503 unavailable", p, rec.Code)
		}
	}
	for p, want := range map[string]string{"/api/overview?range=all&from=2026-01-01&to=2026-01-02": "bad_range",
		"/api/tokens?from=2026-01-01": "bad_date", "/api/context?from=2099-01-01&to=2099-01-02": "bad_date"} {
		if rec := get(h, AllowedHosts[0], p); rec.Code != http.StatusBadRequest || code(t, rec) != want {
			t.Errorf("%s: status %d code %s, want 400 %s", p, rec.Code, code(t, rec), want)
		}
	}
}

func machineSample(at time.Time) collect.MachineSample {
	return collect.MachineSample{At: at, Interval: 2,
		Cores: []collect.Core{{Index: 0, Kind: collect.CoreP, Busy: 40}, {Index: 1, Kind: collect.CoreE, Busy: 10}},
		Load:  &collect.Load{One: 1.5, Five: 1.2, Fifteen: 1},
		Memory: &collect.Memory{Total: 16 << 30, Used: 8 << 30, App: 6 << 30, Compressed: 1 << 30, Cached: 1 << 30,
			SwapTotal: 2 << 30, SwapUsed: 1 << 29},
		Volumes: []collect.Volume{{Name: "Macintosh HD", Total: 500 << 30, Free: 100 << 30}},
		DiskIO:  []collect.DiskIO{{Name: "disk0", ReadBps: 1000, WriteBps: 2000}},
		Net:     []collect.NetIO{{Name: "en0", RxBps: 500, TxBps: 100}},
		TopCPU:  []collect.App{{Name: "Safari", CPU: 12, RSS: 1 << 28, Procs: 3}},
		TopMem:  []collect.App{{Name: "Docker", CPU: 3, RSS: 1 << 30, Procs: 2}}}
}

func dockerSample(at time.Time) collect.DockerSample {
	return collect.DockerSample{At: at, Interval: 2,
		Containers: []collect.Container{
			{Name: "nginx", Stack: "web-services", State: "running", Health: "healthy", CPU: 1, MemUsed: 1 << 26,
				MemLimit: 1 << 30, RxBps: 10, TxBps: 20, UptimeSec: 3600},
			{Name: "mariadb", Stack: "infrastructure-services", State: "exited"}},
		VM: collect.DockerVM{NCPU: 8, MemTotal: 8 << 30, MemUsed: 2 << 30, CPU: 5},
		Disk: &collect.DockerDisk{At: at, Images: 1 << 30, ImageCount: 4, Volumes: 1 << 29, VolumeCount: 2,
			ImagesReclaimable: 300, ImagesUnused: 2, LargestImage: ptr(int64(700)), VolumesReclaimable: 100, VolumesUnused: 1,
			BuildCacheReclaimable: 50, Reclaimable: 450}}
}

func memoryRunner(now time.Time, n int, step time.Duration) *runner.Runner {
	r := runner.New(runner.Options{}, nil, nil, nil, nil)
	for i := n - 1; i >= 0; i-- {
		at := now.Add(-time.Duration(i) * step)
		r.MachineLive.Push(at, machineSample(at))
		r.MachineHist.Push(at, machineSample(at))
		r.DockerLive.Push(at, dockerSample(at))
		r.DockerHist.Push(at, dockerSample(at))
	}
	r.Events.Push(now, collect.DockerEvent{At: now, Name: "nginx", Kind: "start"})
	return r
}

// An unread largest image is left out of the answer, never sent as 0, and a failed disk refresh marks the figures.
func TestDockerDiskSaysWhatIsUnknownOrStale(t *testing.T) {
	now := time.Now()
	r := memoryRunner(now.Add(-2*time.Second), 2, 2*time.Second)
	sample := dockerSample(now)
	sample.Disk.LargestImage = nil
	sample.Stale = []string{collect.PartDisk}
	r.DockerLive.Push(now, sample)
	s, h := newServer(t, nil, r)
	s.Now = func() time.Time { return now }
	rec := get(h, AllowedHosts[0], "/api/docker")
	var d api.DockerResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil || d.Now == nil || d.Now.Disk == nil {
		t.Fatalf("docker live: status %d", rec.Code)
	}
	if dk := d.Now.Disk; !dk.Stale || dk.Images.LargestBytes != nil || dk.Images.Bytes != 1<<30 ||
		strings.Contains(rec.Body.String(), "largestBytes") || !strings.Contains(rec.Body.String(), `"stale":true`) {
		t.Fatalf("disk %+v: want stale, sizes kept and no largest image in the answer", dk)
	}
	if vm := dockerRows(sample).DockerVM[0]; vm.ImagesLargestBytes != nil || vm.ImagesBytes == nil {
		t.Fatalf("the stored row holds a largest image of %v, want none", vm.ImagesLargestBytes)
	}
}

// The exit code is in the live sample only: it reaches the snapshot, and an unknown one is left out, never sent as 0.
func TestDockerNowCarriesExitCodes(t *testing.T) {
	now := time.Now()
	r := memoryRunner(now.Add(-2*time.Second), 2, 2*time.Second)
	sample := dockerSample(now)
	sample.Containers[0].ExitCode = nil
	sample.Containers[1].ExitCode = ptr(int32(0))
	sample.Containers = append(sample.Containers,
		collect.Container{Name: "job", Stack: "web-services", State: "exited", ExitCode: ptr(int32(137))})
	r.DockerLive.Push(now, sample)
	s, h := newServer(t, nil, r)
	s.Now = func() time.Time { return now }
	var d api.DockerResponse
	rec := get(h, AllowedHosts[0], "/api/docker")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil || d.Now == nil {
		t.Fatalf("docker live: status %d", rec.Code)
	}
	got := map[string]*int32{}
	for _, c := range d.Now.Containers {
		got[c.Container] = c.ExitCode
	}
	if len(got) != 3 || got["nginx"] != nil || got["mariadb"] == nil || *got["mariadb"] != 0 ||
		got["job"] == nil || *got["job"] != 137 {
		t.Fatalf("exit codes %v", got)
	}
	if n := strings.Count(rec.Body.String(), `"exitCode"`); n != 2 {
		t.Fatalf("%d exitCode fields in the answer, want 2", n)
	}
	if one := s.DockerNow(ptr("web-services")); len(one.Containers) != 2 || *one.Containers[1].ExitCode != 137 {
		t.Fatalf("one stack: %+v", one.Containers)
	}
}

func TestSetExitCodesMatchesByName(t *testing.T) {
	out := []api.ContainerNow{{Container: "gone", ExitCode: ptr(int32(9))}, {Container: collect.Other}}
	setExitCodes(out, []collect.Container{{Name: "nginx", ExitCode: ptr(int32(1))},
		{Name: collect.Other, State: "exited", ExitCode: ptr(int32(2))}})
	if out[0].ExitCode != nil || out[1].ExitCode == nil || *out[1].ExitCode != 2 {
		t.Fatalf("exit codes %v %v", out[0].ExitCode, out[1].ExitCode)
	}
}

func TestMetricsFromMemory(t *testing.T) {
	now := time.Now()
	r := memoryRunner(now, 30, 2*time.Second)
	r.Status.OK(collect.SourceMachine)
	r.Status.OK(collect.SourceDocker)
	s, h := newServer(t, nil, r)
	s.Now = func() time.Time { return now }
	rec := get(h, AllowedHosts[0], "/api/machine")
	var m api.MachineResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
		t.Fatalf("machine live: status %d", rec.Code)
	}
	if len(m.Series) != 30 || m.Now == nil || m.Now.PerformanceCores != 1 || m.Now.EfficiencyCores != 1 ||
		len(m.CoreSeries) != 2 || len(m.InterfaceSeries) != 1 || m.Resolution != api.ResolutionRaw {
		t.Errorf("machine live: %d points, %d core series", len(m.Series), len(m.CoreSeries))
	}
	if !m.Partial || m.DetailIntervalSeconds != 1 {
		t.Errorf("one minute of a 5-minute range must be partial, detail 1 s: %v %d", m.Partial, m.DetailIntervalSeconds)
	}
	rec = get(h, AllowedHosts[0], "/api/docker?stack=web-services")
	var d api.DockerResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil {
		t.Fatalf("docker live: status %d", rec.Code)
	}
	if !d.Available || d.Now == nil || len(d.Now.Containers) != 1 || d.Now.Disk == nil || len(d.Events) != 1 ||
		len(d.ContainerSeries) != 1 || len(d.VmSeries) != 30 {
		t.Errorf("docker live: available %v containers %d events %d", d.Available, len(d.Now.Containers),
			len(d.Events))
	}
	if d.Now != nil && d.Now.Disk != nil {
		dk := d.Now.Disk
		if d.Now.Network != "stack_full" || dk.ReclaimableBytes != 450 || dk.Images.ReclaimableBytes != 300 ||
			dk.Images.Unused != 2 || dk.Images.LargestBytes == nil || *dk.Images.LargestBytes != 700 || dk.Stale ||
			dk.Volumes.ReclaimableBytes != 100 ||
			dk.Volumes.Unused != 1 || dk.BuildCache.ReclaimableBytes != 50 {
			t.Errorf("docker disk figures or network not served: network %q disk %+v", d.Now.Network, dk)
		}
	}
	if rec := get(h, AllowedHosts[0], "/api/machine?range=2h"); rec.Code != 400 {
		t.Errorf("unknown range: status %d", rec.Code)
	}
}

func TestPausedHistoryServesTheLastHourFromMemory(t *testing.T) {
	now := time.Now()
	r := memoryRunner(now, 10, 10*time.Second)
	r.Status.Fail(collect.SourceHistory, collect.StatePaused, errors.New("down"))
	s, h := newServer(t, nil, r)
	s.Now = func() time.Time { return now }
	for _, rng := range []string{"24h", "7d", "1h"} {
		rec := get(h, AllowedHosts[0], "/api/machine?range="+rng)
		var m api.MachineResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &m) != nil {
			t.Fatalf("%s: status %d", rng, rec.Code)
		}
		if !m.HistoryPaused || !m.Partial || m.Resolution != api.ResolutionRaw || len(m.Series) != 10 {
			t.Errorf("%s: paused %v partial %v resolution %s points %d", rng, m.HistoryPaused, m.Partial,
				m.Resolution, len(m.Series))
		}
		rec = get(h, AllowedHosts[0], "/api/docker?range="+rng)
		if rec.Code != 200 {
			t.Errorf("docker %s: status %d", rng, rec.Code)
		}
	}
	var st api.StatusResponse
	_ = json.Unmarshal(get(h, AllowedHosts[0], "/api/status").Body.Bytes(), &st)
	if !st.Database.HistoryPaused {
		t.Error("status does not say history is paused")
	}
}

func TestLiveStreamSendsFramesAndHeartbeats(t *testing.T) {
	now := time.Now()
	r := memoryRunner(now, 3, 2*time.Second)
	s, h := newServer(t, nil, r)
	s.Hub.Ping = 50 * time.Millisecond
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Publish(ctx, 20*time.Millisecond)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/live", nil)
	req.Host = AllowedHosts[1]
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d type %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	seen := map[string]bool{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	deadline := time.After(5 * time.Second)
	lines := make(chan string)
	go func() {
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for !(seen["machine"] && seen["docker"] && seen["status"] && seen["sessions"] && seen["ping"]) {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended early, saw %v", seen)
			}
			switch {
			case strings.HasPrefix(l, "event: "):
				seen[strings.TrimPrefix(l, "event: ")] = true
			case l == ": ping":
				seen["ping"] = true
			case strings.HasPrefix(l, "data: ") && !json.Valid([]byte(strings.TrimPrefix(l, "data: "))):
				t.Errorf("data line is not one JSON value")
			}
		case <-deadline:
			t.Fatalf("saw only %v", seen)
		}
	}
	cancel()
	waitFor(t, func() bool { return s.Hub.Clients() == 0 })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("condition not reached")
}

func TestHubClosesSlowAndOldestClients(t *testing.T) {
	hub, err := NewHub()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var open []*client
	for range maxClients {
		c := hub.Open(ctx)
		if c == nil {
			t.Fatal("hub full too early")
		}
		open = append(open, c)
	}
	for _, c := range open {
		c.at = c.at.Add(-openBurst)
	}
	// A full hub closes its oldest stream for the new one instead of refusing it.
	newest := hub.Open(ctx)
	select {
	case <-open[0].gone:
	default:
		t.Fatal("hub kept its oldest client past its cap")
	}
	if hub.Clients() != maxClients {
		t.Fatalf("%d clients open, want %d", hub.Clients(), maxClients)
	}
	open = append(open[1:], newest)
	snap := machineSnapshot(machineRows(machineSample(time.Now())))
	for range clientBacklog + 1 {
		hub.Machine(*snap)
	}
	if hub.Clients() != 0 {
		t.Errorf("%d clients that never read are still open", hub.Clients())
	}
	buf := make([]byte, 1<<16)
	for _, c := range open[:1] {
		for {
			if _, err := c.Read(buf); err != nil {
				break
			}
		}
	}
}
