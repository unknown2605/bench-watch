package docker

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"benchwatch/server/internal/collect"
)

const (
	// pruneMargin keeps state of a container the list missed because it was created just after the list call.
	pruneMargin = time.Minute
	// clockSkew is how far past our clock an event time may lie; later times are clamped to it.
	clockSkew = 2 * time.Second
	maxIDs    = 4096
	maxRecent = 1024
	// compactSlack keeps compaction amortised when few IDs are live.
	compactSlack = 64
)

// eventKey identifies an event as the daemon sent it, to drop the copies a reconnect replays.
type eventKey struct {
	nano int64
	hash uint64
}

// eventState counts restarts by container ID: the list has no count and inspect returns the environment. A start
// after exit 0 looks like a one-shot run again, so an `always` policy restarting a clean exit is not counted.
type eventState struct {
	mu       sync.Mutex
	restarts map[string]int
	// crashed: the last die had a non-zero or unknown exit code, so the next start is a restart.
	crashed map[string]bool
	// counted: the last start was counted, so the restart event that follows it adds nothing.
	counted map[string]bool
	started map[string]time.Time
	seen    map[string]tracked
	// order holds IDs by first event, stale ones until they outnumber the live ones; past maxIDs the oldest live go.
	order    []tracked
	seq      uint64
	recent   map[eventKey]bool
	recentQ  []eventKey
	lastNano int64
	// floor is the start of the stream asked for; an earlier event time is raised to it.
	floor   int64
	skipped int
}

// tracked: an order entry is live while seen holds the same seq for its ID, so a reused ID keeps its new entry.
type tracked struct {
	id  string
	at  time.Time
	seq uint64
}

func (e *eventState) init() {
	e.restarts, e.started = map[string]int{}, map[string]time.Time{}
	e.crashed, e.counted = map[string]bool{}, map[string]bool{}
	e.seen = map[string]tracked{}
	e.recent = map[eventKey]bool{}
}

func (e *eventState) replayed(k eventKey) bool {
	if e.recent[k] {
		return true
	}
	e.recent[k] = true
	e.recentQ = append(e.recentQ, k)
	if len(e.recentQ) > maxRecent {
		delete(e.recent, e.recentQ[0])
		e.recentQ = e.recentQ[1:]
	}
	return false
}

func (e *eventState) track(id string, at time.Time) {
	t, ok := e.seen[id]
	if !ok {
		e.seq++
		t = tracked{id: id, seq: e.seq}
		e.order = append(e.order, t)
	}
	t.at = at
	e.seen[id] = t
	for len(e.seen) > maxIDs && len(e.order) > 0 {
		if old := e.order[0]; old.id != id && e.live(old) {
			e.forget(old.id)
		}
		e.order = e.order[1:]
	}
	if len(e.order) > 2*len(e.seen)+compactSlack {
		e.order = slices.DeleteFunc(e.order, func(t tracked) bool { return !e.live(t) })
	}
}

// containerID is the daemon's ID shape; an event with any other ID is kept but not tracked, so a hostile
// stream cannot hold more than maxIDs short keys.
func containerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (e *eventState) live(t tracked) bool {
	cur, ok := e.seen[t.id]
	return ok && cur.seq == t.seq
}

// SkippedEvents counts exec events that got past the stream filter and were dropped.
func (c *Collector) SkippedEvents() int {
	c.events.mu.Lock()
	defer c.events.mu.Unlock()
	return c.events.skipped
}

func (e *eventState) counts() (map[string]int, map[string]time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := make(map[string]int, len(e.restarts))
	for k, v := range e.restarts {
		r[k] = v
	}
	s := make(map[string]time.Time, len(e.started))
	for k, v := range e.started {
		s[k] = v
	}
	return r, s
}

func (e *eventState) forget(id string) {
	delete(e.restarts, id)
	delete(e.crashed, id)
	delete(e.counted, id)
	delete(e.started, id)
	delete(e.seen, id)
}

func (e *eventState) prune(listed map[string]bool, before time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, t := range e.seen {
		if !listed[id] && t.at.Before(before) {
			e.forget(id)
		}
	}
}

func (e *eventState) last() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastNano
}

const kindExec = "exec"

var plainActions = []string{"create", "start", "restart", "stop", "die", "kill", "oom", "pause", "unpause",
	"destroy", "rename", "update"}

// kindOf maps an action to a closed set: exec actions carry the command after the colon, so it is dropped.
func kindOf(action string) string {
	base, detail, _ := strings.Cut(action, ":")
	detail = strings.TrimSpace(detail)
	switch {
	case slices.Contains(plainActions, base):
		return base
	case base == "health_status":
		if allowedHealth[detail] {
			return "health_" + detail
		}
		return "health"
	case strings.HasPrefix(base, "exec_"):
		return kindExec
	}
	return collect.Other
}

// apply returns the event to keep, or false for an exec event or a duplicate replayed after a reconnect.
func (c *Collector) apply(m events.Message) (collect.DockerEvent, bool) {
	e := &c.events
	nano := m.TimeNano
	if nano == 0 {
		nano = min(max(m.Time, 0), math.MaxInt64/int64(time.Second)) * int64(time.Second)
	}
	h := fnv.New64a()
	h.Write([]byte(m.Actor.ID + "\x00" + string(m.Action)))
	key := eventKey{nano, h.Sum64()}
	e.mu.Lock()
	defer e.mu.Unlock()
	nano = min(max(nano, e.floor), c.now().Add(clockSkew).UnixNano())
	if e.replayed(key) {
		return collect.DockerEvent{}, false
	}
	e.lastNano = max(e.lastNano, nano)
	kind := kindOf(string(m.Action))
	if kind == kindExec {
		e.skipped++
		return collect.DockerEvent{}, false
	}
	at := time.Unix(0, nano).UTC()
	ev := collect.DockerEvent{At: at, Name: c.opt.Gate.Name(m.Actor.Attributes["name"]), Kind: kind}
	id := m.Actor.ID
	if !containerID(id) {
		return ev, true
	}
	e.track(id, at)
	switch kind {
	case "die":
		e.crashed[id] = m.Actor.Attributes["exitCode"] != "0"
	case "start":
		e.counted[id] = e.crashed[id]
		if e.crashed[id] {
			e.restarts[id]++
			e.crashed[id] = false
		}
		e.started[id] = at
	case "restart":
		if !e.counted[id] {
			e.restarts[id]++
			e.counted[id] = true
		}
	case "destroy":
		e.forget(id)
	}
	return ev, true
}

var (
	errIdle      = errors.New("event stream idle")
	settleDelay  = 200 * time.Millisecond
	eventFilters = func() client.Filters {
		f := make(client.Filters).Add("type", "container")
		return f.Add("event", append(append([]string{}, plainActions...), "health_status")...)
	}()
)

// absentPoll is how often an absent socket is looked for again.
const absentPoll = 2 * time.Second

// RunEvents follows the event stream until ctx ends, reconnecting with backoff and resuming from the last event.
// The first connection asks for the past hour; the daemon keeps only its last 256 events of any kind.
func (c *Collector) RunEvents(ctx context.Context, status *collect.Status, emit func(collect.DockerEvent)) {
	backoff := time.Second
	since := c.now().Add(-time.Hour)
	for ctx.Err() == nil {
		if last := c.events.last(); last > 0 {
			since = time.Unix(0, min(last, c.now().UnixNano()))
		}
		if c.Absent() {
			backoff = time.Second
			select {
			case <-ctx.Done():
				return
			case <-time.After(absentPoll):
			}
			continue
		}
		err := c.follow(ctx, since, status, emit, &backoff)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errIdle) {
			continue
		}
		status.Fail(collect.SourceDockerEvents, collect.StateUnavailable, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (c *Collector) follow(ctx context.Context, since time.Time, status *collect.Status,
	emit func(collect.DockerEvent), backoff *time.Duration) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	nctx, ncancel := context.WithTimeout(ctx, c.opt.CallTimeout)
	err := c.negotiate(nctx)
	ncancel()
	if err != nil {
		return err
	}
	opened := time.AfterFunc(c.opt.CallTimeout, cancel)
	c.events.mu.Lock()
	c.events.floor = since.Unix() * int64(time.Second)
	c.events.mu.Unlock()
	res := c.cli.Events(sctx, client.EventsListOptions{Since: strconv.FormatInt(since.Unix(), 10), Filters: eventFilters})
	if !opened.Stop() {
		return context.DeadlineExceeded
	}
	// Events returns before it queues a connect error, so the stream counts as up only after a short wait.
	settle := time.NewTimer(settleDelay)
	defer settle.Stop()
	idle := time.NewTimer(c.opt.IdleTimeout)
	defer idle.Stop()
	up := false
	markUp := func() {
		if !up {
			up = true
			status.OK(collect.SourceDockerEvents)
			*backoff = time.Second
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-settle.C:
			markUp()
		case <-idle.C:
			return errIdle
		case err := <-res.Err:
			if err == nil {
				err = io.EOF
			}
			return err
		case m := <-res.Messages:
			markUp()
			idle.Reset(c.opt.IdleTimeout)
			if m.Type != events.ContainerEventType {
				continue
			}
			if ev, ok := c.apply(m); ok && emit != nil {
				emit(ev)
			}
		}
	}
}
