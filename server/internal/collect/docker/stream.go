package docker

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// frame is one stats record of a container's stream; got is our monotonic receive time, read the daemon's.
type frame struct {
	got                 time.Time
	read                time.Time
	cpuTotal, cpuSystem uint64
	online              uint32
	memUsed, memLimit   uint64
	rx, tx              uint64
	cpuNow              float64
	cpuNowOK            bool
	rxNow, txNow        float64
	netNowOK            bool
}

// stream follows one running container's stats until the container leaves the running list or the collector closes.
type stream struct {
	cancel context.CancelFunc
	done   chan struct{}
	ready  chan struct{}
	once   sync.Once

	mu    sync.Mutex
	last  *frame
	err   error
	opens int
}

func (s *stream) set(f frame) {
	s.mu.Lock()
	s.last, s.err = &f, nil
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
}

func (s *stream) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
}

func (s *stream) state() (*frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.err
}

type streamCapError struct{}

func (streamCapError) Error() string { return "more running containers than stats streams" }
func (streamCapError) Code() string  { return "ETOOLARGE" }

type streamIdleError struct{}

func (streamIdleError) Error() string { return "stats stream idle" }
func (streamIdleError) Code() string  { return "ETIMEDOUT" }

var errStreamIdle error = streamIdleError{}

// syncStreams opens a stream for each running ID up to MaxStreams and closes those of IDs no longer running;
// it returns the streams it opened.
func (c *Collector) syncStreams(running []string) []*stream {
	want := make(map[string]bool, len(running))
	for _, id := range running {
		want[id] = true
	}
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.closed {
		return nil
	}
	for id, s := range c.streams {
		if !want[id] {
			s.cancel()
			delete(c.streams, id)
		}
	}
	var opened []*stream
	for _, id := range running {
		if _, ok := c.streams[id]; ok || len(c.streams) >= c.opt.MaxStreams {
			continue
		}
		ctx, cancel := context.WithCancel(c.bgCtx)
		s := &stream{cancel: cancel, done: make(chan struct{}), ready: make(chan struct{})}
		c.streams[id] = s
		opened = append(opened, s)
		c.bg.Add(1)
		go func() {
			defer c.bg.Done()
			c.followStats(ctx, id, s)
		}()
	}
	return opened
}

func (c *Collector) stopStreams() {
	c.smu.Lock()
	defer c.smu.Unlock()
	for id, s := range c.streams {
		s.cancel()
		delete(c.streams, id)
	}
}

// Streams counts the open stats streams and how often they were opened, reconnects included.
func (c *Collector) Streams() (open, opens int) {
	c.smu.Lock()
	defer c.smu.Unlock()
	for _, s := range c.streams {
		s.mu.Lock()
		opens += s.opens
		s.mu.Unlock()
	}
	return len(c.streams), opens
}

// followStats reopens the stream with backoff while the container stays listed; a record resets the backoff.
func (c *Collector) followStats(ctx context.Context, id string, s *stream) {
	defer close(s.done)
	backoff := c.opt.StreamBackoff
	for ctx.Err() == nil {
		s.mu.Lock()
		s.opens++
		s.mu.Unlock()
		got, err := c.readStats(ctx, id, s)
		if ctx.Err() != nil {
			return
		}
		if got {
			backoff = c.opt.StreamBackoff
		}
		s.fail(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, c.opt.MaxStreamBackoff)
	}
}

type streamBody struct {
	statsBody
	Read        time.Time          `json:"read"`
	PreCPUStats container.CPUStats `json:"precpu_stats"`
}

func (c *Collector) readStats(ctx context.Context, id string, s *stream) (bool, error) {
	nctx, ncancel := c.call(ctx)
	err := c.negotiate(nctx)
	ncancel()
	if err != nil {
		return false, err
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	opened := time.AfterFunc(c.opt.CallTimeout, cancel)
	res, err := c.cli.ContainerStats(sctx, id, client.ContainerStatsOptions{Stream: true})
	if !opened.Stop() {
		if err == nil {
			res.Body.Close()
		}
		return false, context.DeadlineExceeded
	}
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	idle := time.AfterFunc(c.opt.StreamIdle, cancel)
	defer idle.Stop()
	dec := json.NewDecoder(res.Body)
	prev, _ := s.state()
	got := false
	for {
		var b streamBody
		if err := dec.Decode(&b); err != nil {
			if ctx.Err() == nil && sctx.Err() != nil {
				return got, errStreamIdle
			}
			return got, err
		}
		idle.Reset(c.opt.StreamIdle)
		f := frameOf(&b, prev, time.Now())
		prev = &f
		s.set(f)
		got = true
	}
}

func frameOf(b *streamBody, prev *frame, got time.Time) frame {
	f := frame{got: got, read: b.Read.UTC(), cpuTotal: b.CPUStats.CPUUsage.TotalUsage, cpuSystem: b.CPUStats.SystemUsage,
		online: b.CPUStats.OnlineCPUs, memLimit: b.MemoryStats.Limit, memUsed: b.MemoryStats.Usage}
	// Same as docker stats: page cache that can be dropped is not counted as used.
	for _, k := range []string{"inactive_file", "total_inactive_file"} {
		if v, ok := b.MemoryStats.Stats[k]; ok && v < f.memUsed {
			f.memUsed -= v
			break
		}
	}
	for _, n := range b.Networks {
		f.rx += n.RxBytes
		f.tx += n.TxBytes
	}
	pre := b.PreCPUStats
	if pre.SystemUsage > 0 && f.cpuSystem > pre.SystemUsage && f.cpuTotal >= pre.CPUUsage.TotalUsage {
		online := float64(max(f.online, 1))
		f.cpuNow = round1(100 * online * float64(f.cpuTotal-pre.CPUUsage.TotalUsage) / float64(f.cpuSystem-pre.SystemUsage))
		f.cpuNowOK = true
	}
	if prev != nil && f.rx >= prev.rx && f.tx >= prev.tx {
		if dt := f.read.Sub(prev.read).Seconds(); dt > 0 {
			f.rxNow, f.txNow, f.netNowOK = round1(float64(f.rx-prev.rx)/dt), round1(float64(f.tx-prev.tx)/dt), true
		}
	}
	return f
}

// fill copies the newest record of r's stream into r; a failed stream or one whose record is older than StreamFresh
// leaves r without stats.
func (c *Collector) fill(r *raw, s *stream) {
	if s == nil {
		r.statsErr = streamCapError{}
		return
	}
	f, err := s.state()
	switch {
	case err != nil:
		r.statsErr = err
		return
	case f == nil:
		r.statsErr = context.DeadlineExceeded
		return
	case time.Since(f.got) > c.opt.StreamFresh:
		r.statsErr = errStreamIdle
		return
	}
	r.statsOK = true
	r.read = f.read
	r.cpuTotal, r.cpuSystem, r.online = f.cpuTotal, f.cpuSystem, f.online
	r.memUsed, r.memLimit, r.rx, r.tx = f.memUsed, f.memLimit, f.rx, f.tx
	r.cpuNow, r.cpuNowOK, r.rxNow, r.txNow, r.netNowOK = f.cpuNow, f.cpuNowOK, f.rxNow, f.txNow, f.netNowOK
}

// streamFor returns the stream of id, or nil when the cap left it without one.
func (c *Collector) streamFor(id string) *stream {
	c.smu.Lock()
	defer c.smu.Unlock()
	return c.streams[id]
}

// awaitFirst waits until every new stream has its first record or error, bounded by CallTimeout and ctx.
func (c *Collector) awaitFirst(ctx context.Context, opened []*stream) {
	if len(opened) == 0 {
		return
	}
	t := time.NewTimer(c.opt.CallTimeout)
	defer t.Stop()
	for _, s := range opened {
		select {
		case <-s.ready:
		case <-t.C:
			return
		case <-ctx.Done():
			return
		}
	}
}
