// Package docker reads the Docker daemon through read calls only: container list, streamed stats,
// events, disk usage and info. It keeps container names, the compose project name and numbers, nothing else.
package docker

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"benchwatch/server/internal/collect"
)

const (
	projectLabel = "com.docker.compose.project"
	StackNetwork = "stack_full"
)

// api is every client method the collector may call, all of them reads; the client itself, with Dialer and
// the write methods, is never reachable from the Collector.
type api interface {
	Ping(ctx context.Context, options client.PingOptions) (client.PingResult, error)
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerStats(ctx context.Context, id string,
		options client.ContainerStatsOptions) (client.ContainerStatsResult, error)
	Events(ctx context.Context, options client.EventsListOptions) client.EventsResult
	Info(ctx context.Context, options client.InfoOptions) (client.SystemInfoResult, error)
	DiskUsage(ctx context.Context, options client.DiskUsageOptions) (client.DiskUsageResult, error)
	Close() error
}

// ErrAbsent is what a dial returns while no Docker socket exists; nothing is dialled then.
var ErrAbsent = errors.New("docker socket absent")

type Options struct {
	Host string
	// Resolve, when set instead of Host, finds the socket on every dial and every Absent call, so Docker that
	// starts or stops after the agent is picked up without a restart.
	Resolve func() (string, Presence)
	Now     func() time.Time
	Gate    collect.NameGate
	// CallTimeout bounds each call: ping, list, info, opening a stats or event stream.
	CallTimeout     time.Duration
	SnapshotTimeout time.Duration
	// IdleTimeout reopens an event stream that delivered nothing for that long.
	IdleTimeout time.Duration
	InfoEvery   time.Duration
	DiskEvery   time.Duration
	DiskTimeout time.Duration
	// MaxStreams caps the open stats streams, one per running container; 0 or less means 64.
	MaxStreams int
	// StreamIdle reopens a stats stream that sent no record for that long; its last record then counts as missing.
	StreamIdle time.Duration
	// StreamFresh is how long a stream's newest record counts as current, about three record intervals; 0 means 3 s,
	// and it never exceeds StreamIdle. After it the container's stats are missing while the stream keeps running.
	StreamFresh time.Duration
	// StreamBackoff is the first wait before reopening a stats stream, doubling up to MaxStreamBackoff.
	StreamBackoff    time.Duration
	MaxStreamBackoff time.Duration
}

type Collector struct {
	cli       api
	opt       Options
	closeIdle func()

	negotiated  atomic.Bool
	negotiating chan struct{}
	bgCtx       context.Context
	bgStop      context.CancelFunc
	bg          sync.WaitGroup

	mu       sync.Mutex
	infoAt   time.Time
	infoErr  error
	ncpu     int
	memTotal uint64
	disk     *collect.DockerDisk
	diskAt   time.Time
	diskErr  error
	diskBusy bool
	events   eventState

	smu     sync.Mutex
	streams map[string]*stream
	// closed is set under smu by Close, so no bg.Add can follow its Wait.
	closed bool
}

// raw is one container's counters. id is the internal key for rates, restarts and the stats call; it never
// leaves this package, and name is still raw here.
type raw struct {
	id        string
	name      string
	stack     string
	state     string
	health    string
	statusUp  time.Duration
	upKnown   bool
	stackFull string
	statsErr  error
	statsOK   bool
	cpuTotal  uint64
	cpuSystem uint64
	online    uint32
	memUsed   uint64
	memLimit  uint64
	rx, tx    uint64
	// read is the daemon's record time; the *Now fields are that record's own rates, for a record seen twice.
	read                 time.Time
	cpuNow, rxNow, txNow float64
	cpuNowOK, netNowOK   bool
}

type Snapshot struct {
	At         time.Time
	containers []raw
	ncpu       int
	memTotal   uint64
	infoKnown  bool
	infoStale  bool
	disk       *collect.DockerDisk
	diskStale  bool
	// Partial lists Docker calls that failed while the container list succeeded.
	Partial []collect.PartError
}

func (s Snapshot) Count() int { return len(s.containers) }

func New(opt Options) (*Collector, error) {
	host, pick := opt.Host, (func() (string, error))(nil)
	if opt.Resolve != nil {
		host = "unix:///benchwatch-resolved.sock"
		pick = func() (string, error) {
			h, presence := opt.Resolve()
			path, ok := strings.CutPrefix(h, "unix://")
			if presence != Found || !ok {
				return "", ErrAbsent
			}
			return path, nil
		}
	}
	if host == "" {
		return nil, errors.New("docker host not set")
	}
	cli, tr, err := newClientPick(host, pick)
	if err != nil {
		return nil, err
	}
	c := newCollector(cli, opt)
	c.closeIdle = tr.CloseIdleConnections
	return c, nil
}

func newCollector(cli api, opt Options) *Collector {
	if opt.Now == nil {
		opt.Now = collect.RealClock().Now
	}
	if opt.Gate == nil {
		opt.Gate = collect.DefaultNameGate
	}
	if opt.CallTimeout <= 0 {
		opt.CallTimeout = 2 * time.Second
	}
	if opt.SnapshotTimeout <= 0 {
		opt.SnapshotTimeout = 3 * opt.CallTimeout
	}
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = 2 * time.Minute
	}
	if opt.InfoEvery <= 0 {
		opt.InfoEvery = time.Minute
	}
	if opt.DiskEvery <= 0 {
		opt.DiskEvery = 5 * time.Minute
	}
	if opt.DiskTimeout <= 0 {
		opt.DiskTimeout = time.Minute
	}
	if opt.MaxStreams <= 0 {
		opt.MaxStreams = 64
	}
	if opt.StreamIdle <= 0 {
		opt.StreamIdle = 10 * time.Second
	}
	if opt.StreamFresh <= 0 {
		opt.StreamFresh = 3 * time.Second
	}
	opt.StreamFresh = min(opt.StreamFresh, opt.StreamIdle)
	if opt.StreamBackoff <= 0 {
		opt.StreamBackoff = time.Second
	}
	if opt.MaxStreamBackoff <= 0 {
		opt.MaxStreamBackoff = 30 * time.Second
	}
	c := &Collector{cli: cli, opt: opt, negotiating: make(chan struct{}, 1), streams: map[string]*stream{}}
	c.bgCtx, c.bgStop = context.WithCancel(context.Background())
	c.events.init()
	return c
}

// Absent is true while no socket exists for a collector built with Resolve; it only looks at the file system.
func (c *Collector) Absent() bool {
	if c.opt.Resolve == nil {
		return false
	}
	_, presence := c.opt.Resolve()
	return presence != Found
}

// Stopped is true while the socket is resolved (Docker Desktop is installed or DOCKER_HOST names one) but its file
// does not exist: Docker is there and not running. A socket that exists and does not answer is not stopped.
func (c *Collector) Stopped() bool {
	if c.opt.Resolve == nil {
		return false
	}
	host, presence := c.opt.Resolve()
	path, ok := strings.CutPrefix(host, "unix://")
	if presence != Found || !ok {
		return false
	}
	// Stat, not Lstat: a dangling symlink (macOS /var/run/docker.sock after Docker Desktop quits) is a missing socket.
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// Release closes the stats streams without closing the collector, for the time Docker is absent.
func (c *Collector) Release() { c.stopStreams() }

func (c *Collector) Close() error {
	c.smu.Lock()
	c.closed = true
	c.smu.Unlock()
	c.bgStop()
	c.bg.Wait()
	err := c.cli.Close()
	if c.closeIdle != nil {
		c.closeIdle()
	}
	return err
}

func (c *Collector) now() time.Time { return c.opt.Now().Round(0).UTC() }

func (c *Collector) call(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.opt.CallTimeout)
}

// negotiate pings once with the caller's deadline before any other call. Left to the client, the first
// request negotiates under a lock that ignores contexts, so one call stuck on a silent socket blocks all others.
func (c *Collector) negotiate(ctx context.Context) error {
	if c.negotiated.Load() {
		return nil
	}
	select {
	case c.negotiating <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.negotiating }()
	if c.negotiated.Load() {
		return nil
	}
	if _, err := c.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		return err
	}
	c.negotiated.Store(true)
	return nil
}

// errClosed is what Snapshot returns after Close.
var errClosed = errors.New("docker collector closed")

// hard tells an unreachable or broken daemon from a slow one: a timeout or cancellation says nothing about the
// streams, which have their own idle and backoff handling.
func hard(err error) bool {
	code := collect.ErrorCode(err)
	return code != "ETIMEDOUT" && code != "ECANCELED"
}

// Snapshot lists containers and reads the newest record of each running one's stats stream. An error means
// Docker is unavailable; the streams close only when it is hard unavailable, not on a timeout. A failed stream or
// info call is listed in Partial instead.
func (c *Collector) Snapshot(ctx context.Context) (Snapshot, error) {
	c.smu.Lock()
	closed := c.closed
	c.smu.Unlock()
	if closed {
		return Snapshot{}, errClosed
	}
	ctx, cancel := context.WithTimeout(ctx, c.opt.SnapshotTimeout)
	defer cancel()
	nctx, ncancel := c.call(ctx)
	err := c.negotiate(nctx)
	ncancel()
	if err != nil {
		if hard(err) {
			c.stopStreams()
		}
		return Snapshot{}, err
	}
	listed := c.now()
	lctx, lcancel := c.call(ctx)
	list, err := c.cli.ContainerList(lctx, client.ContainerListOptions{All: true})
	lcancel()
	if err != nil {
		if hard(err) {
			c.stopStreams()
		}
		return Snapshot{}, err
	}
	snap := Snapshot{At: listed, containers: make([]raw, 0, len(list.Items))}
	ids := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		snap.containers = append(snap.containers, fromSummary(&list.Items[i]))
		ids[list.Items[i].ID] = true
	}
	list.Items = nil
	c.events.prune(ids, listed.Add(-pruneMargin))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); c.refreshInfo(ctx) }()
	var running []string
	for i := range snap.containers {
		if snap.containers[i].state == string(container.StateRunning) {
			running = append(running, snap.containers[i].id)
		}
	}
	c.awaitFirst(ctx, c.syncStreams(running))
	for i := range snap.containers {
		if r := &snap.containers[i]; r.state == string(container.StateRunning) {
			c.fill(r, c.streamFor(r.id))
		}
	}
	wg.Wait()
	c.maybeDisk()

	for i := range snap.containers {
		if e := snap.containers[i].statsErr; e != nil && !cerrdefs.IsNotFound(e) {
			snap.Partial = append(snap.Partial, collect.NewPartError(collect.PartStats, e))
			break
		}
	}
	c.mu.Lock()
	snap.ncpu, snap.memTotal = c.ncpu, c.memTotal
	snap.infoKnown, snap.infoStale = !c.infoAt.IsZero(), c.infoErr != nil
	snap.disk, snap.diskStale = c.disk, c.diskErr != nil
	if c.infoErr != nil {
		snap.Partial = append(snap.Partial, collect.NewPartError(collect.PartInfo, c.infoErr))
	}
	if c.diskErr != nil {
		snap.Partial = append(snap.Partial, collect.NewPartError(collect.PartDisk, c.diskErr))
	}
	c.mu.Unlock()
	return snap, nil
}

var allowedStates = map[string]bool{
	"created": true, "running": true, "paused": true, "restarting": true, "removing": true, "exited": true, "dead": true,
}

var allowedHealth = map[string]bool{"healthy": true, "unhealthy": true, "starting": true}

var statusHealth = regexp.MustCompile(`\((healthy|unhealthy|health: starting)\)`)

// fromSummary copies the allowed fields; command, mounts, image, ports and every other label are never read.
func fromSummary(s *container.Summary) raw {
	r := raw{id: s.ID, state: string(s.State)}
	if !allowedStates[r.state] {
		r.state = collect.Other
	}
	if len(s.Names) > 0 {
		r.name = strings.TrimPrefix(s.Names[0], "/")
	}
	if v, ok := s.Labels[projectLabel]; ok {
		r.stack = v
	}
	if s.Health != nil && allowedHealth[string(s.Health.Status)] {
		r.health = string(s.Health.Status)
	} else if m := statusHealth.FindStringSubmatch(s.Status); m != nil {
		r.health = strings.TrimPrefix(m[1], "health: ")
	}
	r.statusUp, r.upKnown = parseUp(s.Status)
	// A container in another's network namespace reports that container's traffic, so it is not attributed twice.
	if s.NetworkSettings != nil && !strings.HasPrefix(s.HostConfig.NetworkMode, "container:") {
		if _, ok := s.NetworkSettings.Networks[StackNetwork]; ok {
			r.stackFull = "shared"
			if len(s.NetworkSettings.Networks) == 1 {
				r.stackFull = "only"
			}
		}
	}
	return r
}

type statsBody struct {
	CPUStats    container.CPUStats                `json:"cpu_stats"`
	MemoryStats container.MemoryStats             `json:"memory_stats"`
	Networks    map[string]container.NetworkStats `json:"networks"`
}

// refreshInfo retries every poll after a failure; until then the last good values stay, marked stale.
func (c *Collector) refreshInfo(ctx context.Context) {
	c.mu.Lock()
	due := c.infoErr != nil || c.now().Sub(c.infoAt) >= c.opt.InfoEvery
	c.mu.Unlock()
	if !due {
		return
	}
	ictx, cancel := c.call(ctx)
	ictx, tr := withTrip(ictx)
	res, err := c.cli.Info(ictx, client.InfoOptions{})
	err = tr.err(err)
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.infoErr = err
	if err == nil {
		c.infoAt, c.ncpu, c.memTotal = c.now(), res.Info.NCPU, uint64(max(0, res.Info.MemTotal))
	}
}

// maybeDisk runs disk usage in the background, because sizing volumes can take seconds on Docker Desktop.
func (c *Collector) maybeDisk() {
	c.mu.Lock()
	if c.diskBusy || (!c.diskAt.IsZero() && c.now().Sub(c.diskAt) < c.opt.DiskEvery) {
		c.mu.Unlock()
		return
	}
	c.diskBusy = true
	c.mu.Unlock()
	c.smu.Lock()
	closed := c.closed
	if !closed {
		c.bg.Add(1)
	}
	c.smu.Unlock()
	if closed {
		return
	}
	go func() {
		defer c.bg.Done()
		dctx, cancel := context.WithTimeout(c.bgCtx, c.opt.DiskTimeout)
		defer cancel()
		dctx, tr := withTrip(dctx)
		res, err := c.cli.DiskUsage(dctx,
			client.DiskUsageOptions{Containers: true, Images: true, BuildCache: true, Volumes: true})
		err = tr.err(err)
		var largest *int64
		var itemsErr error
		if err == nil {
			// Only images are listed in full, so a big build cache cannot push the main report over its cap.
			// Its own timeout, so a slow main request cannot starve it.
			ictx, icancel := context.WithTimeout(c.bgCtx, c.opt.DiskTimeout)
			defer icancel()
			var size int64
			if size, itemsErr = c.fetchLargestImage(ictx); itemsErr == nil {
				largest = &size
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.diskBusy, c.diskAt, c.diskErr = false, c.now(), cmp.Or(err, itemsErr)
		if err != nil {
			return
		}
		if itemsErr != nil && c.disk != nil {
			largest = c.disk.LargestImage
		}
		d := &collect.DockerDisk{
			At:          c.diskAt,
			Images:      res.Images.TotalSize,
			ImageCount:  res.Images.TotalCount,
			Volumes:     res.Volumes.TotalSize,
			VolumeCount: res.Volumes.TotalCount,
			BuildCache:  res.BuildCache.TotalSize,
			Containers:  res.Containers.TotalSize,

			ImagesReclaimable:     max(res.Images.Reclaimable, 0),
			ImagesUnused:          max(res.Images.TotalCount-res.Images.ActiveCount, 0),
			LargestImage:          largest,
			VolumesReclaimable:    max(res.Volumes.Reclaimable, 0),
			VolumesUnused:         max(res.Volumes.TotalCount-res.Volumes.ActiveCount, 0),
			BuildCacheReclaimable: max(res.BuildCache.Reclaimable, 0),
		}
		d.Reclaimable = d.ImagesReclaimable + d.VolumesReclaimable + d.BuildCacheReclaimable +
			max(res.Containers.Reclaimable, 0)
		c.disk = d
	}()
}

// fetchLargestImage requests the image list and returns the biggest size and nothing that identifies the image.
func (c *Collector) fetchLargestImage(ctx context.Context) (int64, error) {
	ctx, tr := withTrip(ctx)
	res, err := c.cli.DiskUsage(ctx, client.DiskUsageOptions{Images: true, Verbose: true})
	if err = tr.err(err); err != nil {
		return 0, err
	}
	var m int64
	for _, it := range res.Images.Items {
		m = max(m, it.Size)
	}
	return m, nil
}

// Derive turns two snapshots into rates, pairing containers by ID. Containers whose name fails the gate
// are merged into one Other row with summed numbers and their count.
func (c *Collector) Derive(prev, cur Snapshot) collect.DockerSample {
	dt := cur.At.Sub(prev.At).Seconds()
	out := collect.DockerSample{
		At: cur.At, Interval: dt, Disk: cur.disk,
		VM: collect.DockerVM{NCPU: cur.ncpu, MemTotal: cur.memTotal},
	}
	if !cur.infoKnown {
		out.Missing = append(out.Missing, collect.PartInfo)
	} else if cur.infoStale {
		out.Stale = append(out.Stale, collect.PartInfo)
	}
	if cur.disk == nil {
		out.Missing = append(out.Missing, collect.PartDisk)
	} else if cur.diskStale {
		out.Stale = append(out.Stale, collect.PartDisk)
	}
	old := make(map[string]*raw, len(prev.containers))
	for i := range prev.containers {
		old[prev.containers[i].id] = &prev.containers[i]
	}
	restarts, started := c.events.counts()
	var other *collect.Container
	statsMissing, ratesMissing := false, false
	out.Containers = make([]collect.Container, 0, len(cur.containers))
	for i := range cur.containers {
		r := &cur.containers[i]
		ct := collect.Container{
			Name: c.opt.Gate.Name(r.name), State: r.state, Health: r.health,
			MemUsed: r.memUsed, MemLimit: r.memLimit, Restarts: restarts[r.id], StackFull: r.stackFull,
		}
		if r.stack != "" {
			ct.Stack = c.opt.Gate.Name(r.stack)
		}
		running := r.state == string(container.StateRunning)
		if running {
			if t, ok := started[r.id]; ok && !t.After(cur.At) {
				ct.UptimeSec = int64(cur.At.Sub(t).Seconds())
			} else if r.upKnown {
				ct.UptimeSec, ct.UptimeApprox = int64(r.statusUp.Seconds()), true
			}
			if !r.statsOK {
				ct.Missing = append(ct.Missing, collect.PartStats)
				statsMissing = true
			}
			if !c.rates(&ct, old[r.id], r, dt) {
				ct.Missing = append(ct.Missing, collect.PartRates)
				ratesMissing = true
			}
		}
		out.VM.CPU += ct.CPU
		out.VM.MemUsed += ct.MemUsed
		switch r.stackFull {
		case "only":
			out.StackFull.Attributed++
			out.StackFull.RxBps += ct.RxBps
			out.StackFull.TxBps += ct.TxBps
		case "shared":
			out.StackFull.Shared++
		}
		if ct.Name != collect.Other {
			out.Containers = append(out.Containers, ct)
			continue
		}
		if other == nil {
			other = &collect.Container{Name: collect.Other}
		}
		mergeOther(other, ct)
	}
	if statsMissing {
		out.Missing = append(out.Missing, collect.PartStats)
	}
	if ratesMissing {
		out.Missing = append(out.Missing, collect.PartRates)
	}
	if other != nil {
		out.Containers = append(out.Containers, *other)
	}
	out.Containers = collect.CapRows(out.Containers, func(ct collect.Container) string { return ct.Name },
		byRunningThenName, collect.Container{Name: collect.Other}, mergeOther)
	for i := range out.Containers {
		if o := &out.Containers[i]; o.Name == collect.Other {
			o.CPU, o.RxBps, o.TxBps = round1(o.CPU), round1(o.RxBps), round1(o.TxBps)
		}
	}
	out.VM.CPU = round1(out.VM.CPU)
	out.StackFull.RxBps, out.StackFull.TxBps = round1(out.StackFull.RxBps), round1(out.StackFull.TxBps)
	return out
}

func byRunningThenName(a, b collect.Container) int {
	ra, rb := a.State == string(container.StateRunning), b.State == string(container.StateRunning)
	if ra != rb {
		if ra {
			return -1
		}
		return 1
	}
	return cmp.Compare(a.Name, b.Name)
}

// rates uses the counters of two records; with no earlier record, or the same record twice, it takes the record's
// own rates, which compare it with the record before it in the stream.
func (c *Collector) rates(ct *collect.Container, p, r *raw, dt float64) bool {
	if !r.statsOK {
		return false
	}
	if p == nil || !p.statsOK || (!r.read.IsZero() && r.read.Equal(p.read)) {
		if !r.cpuNowOK || !r.netNowOK {
			return false
		}
		ct.CPU, ct.RxBps, ct.TxBps = r.cpuNow, r.rxNow, r.txNow
		return true
	}
	if !p.read.IsZero() && r.read.After(p.read) {
		dt = r.read.Sub(p.read).Seconds()
	}
	if dt <= 0 {
		return false
	}
	if r.cpuTotal < p.cpuTotal || r.cpuSystem <= p.cpuSystem || r.rx < p.rx || r.tx < p.tx {
		return false
	}
	online := float64(max(r.online, 1))
	ct.CPU = round1(100 * online * float64(r.cpuTotal-p.cpuTotal) / float64(r.cpuSystem-p.cpuSystem))
	ct.RxBps = round1(float64(r.rx-p.rx) / dt)
	ct.TxBps = round1(float64(r.tx-p.tx) / dt)
	return true
}

func mergeOther(o *collect.Container, ct collect.Container) {
	if o.Count == 0 {
		o.State = ct.State
	} else if o.State != ct.State {
		o.State = collect.Other
	}
	o.CPU += ct.CPU
	o.MemUsed += ct.MemUsed
	o.MemLimit += ct.MemLimit
	o.RxBps += ct.RxBps
	o.TxBps += ct.TxBps
	o.Restarts += ct.Restarts
	o.Count++
	for _, m := range ct.Missing {
		if !slices.Contains(o.Missing, m) {
			o.Missing = append(o.Missing, m)
		}
	}
}

var upRe = regexp.MustCompile(`^Up (Less than a second|About a minute|About an hour|` +
	`(\d+) (second|minute|hour|day|week|month|year)s?)`)

// parseUp reads the lower bound of Docker's rounded "Up 3 hours" status (go-units HumanDuration).
func parseUp(status string) (time.Duration, bool) {
	m := upRe.FindStringSubmatch(status)
	if m == nil {
		return 0, false
	}
	switch m[1] {
	case "Less than a second":
		return 0, true
	case "About a minute":
		return time.Minute, true
	case "About an hour":
		return time.Hour, true
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, false
	}
	unit := map[string]time.Duration{
		"second": time.Second, "minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour,
		"week": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour, "year": 365 * 24 * time.Hour,
	}[m[3]]
	return time.Duration(n) * unit, true
}

func round1(v float64) float64 {
	if v < 0 {
		return -round1(-v)
	}
	return float64(int64(v*10+0.5)) / 10
}
