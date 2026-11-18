package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/codelog"
	"benchwatch/server/internal/transcripts"
)

const (
	PingEvery   = 15 * time.Second
	StatusEvery = 10 * time.Second
	// StatusPoll is how often status and live sessions are checked for a change; samples have their own pace.
	StatusPoll    = 2 * time.Second
	maxClients    = 16
	clientBacklog = 32
	// openBurst must stay under the page's first reconnect delay (1 s), or two reconnecting tabs evict each other.
	openBurst = 500 * time.Millisecond
)

var ping = []byte(": ping\n\n")

// Hub fans live frames out to the open /api/live streams; a client that falls behind is closed and reconnects.
type Hub struct {
	enc *api.LiveEncoder

	mu      sync.Mutex
	clients map[*client]struct{}
	opened  uint64
	last    map[string][]byte
	// Ping is the heartbeat interval; tests shorten it.
	Ping time.Duration
}

func NewHub() (*Hub, error) {
	enc, err := api.NewLiveEncoder()
	if err != nil {
		return nil, err
	}
	return &Hub{enc: enc, clients: map[*client]struct{}{}, last: map[string][]byte{}, Ping: PingEvery},
		nil
}

func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

type client struct {
	hub     *Hub
	seq     uint64
	at      time.Time
	abort   func()
	ctx     context.Context
	frames  chan []byte
	gone    chan struct{}
	once    sync.Once
	pending []byte
	ping    *time.Ticker
}

// Open registers a stream. A full hub closes one for it, so idle tabs cannot lock the page out: the newest if that
// opened within openBurst (a flood then evicts itself), else the oldest; picked under the lock, so the cap holds.
func (h *Hub) Open(ctx context.Context) *client {
	now := time.Now()
	h.mu.Lock()
	var evicted []*client
	for len(h.clients) >= maxClients {
		var oldest, newest *client
		for c := range h.clients {
			if oldest == nil || c.seq < oldest.seq {
				oldest = c
			}
			if newest == nil || c.seq > newest.seq {
				newest = c
			}
		}
		victim := oldest
		if now.Sub(newest.at) < openBurst {
			victim = newest
		}
		delete(h.clients, victim)
		evicted = append(evicted, victim)
	}
	h.opened++
	c := &client{hub: h, seq: h.opened, at: now, abort: api.LiveAbort(ctx), ctx: ctx,
		frames: make(chan []byte, clientBacklog), gone: make(chan struct{}), ping: time.NewTicker(h.Ping)}
	for _, t := range []string{"status", "machine", "docker", "sessions"} {
		if f := h.last[t]; f != nil {
			c.pending = append(c.pending, f...)
		}
	}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	for _, o := range evicted {
		o.drop()
	}
	return c
}

// drop also ends a write blocked on a client that stopped reading.
func (c *client) drop() {
	c.once.Do(func() {
		c.hub.mu.Lock()
		delete(c.hub.clients, c)
		c.hub.mu.Unlock()
		c.ping.Stop()
		close(c.gone)
		c.abort()
	})
}

func (c *client) Close() error {
	c.drop()
	return nil
}

// Read blocks until a frame or a heartbeat is due and ends the stream with EOF once the request is gone.
func (c *client) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		select {
		case <-c.ctx.Done():
			c.drop()
			return 0, io.EOF
		case <-c.gone:
			return 0, io.EOF
		case f := <-c.frames:
			c.pending = f
		case <-c.ping.C:
			c.pending = ping
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (h *Hub) publish(t string, ev api.LiveEvent) {
	frame, err := h.enc.Frame(context.Background(), ev)
	if err != nil {
		h.failed(t)
		return
	}
	codelog.Note("live "+t, "")
	h.mu.Lock()
	if t != "activity" {
		h.last[t] = frame
	}
	var slow []*client
	for c := range h.clients {
		select {
		case c.frames <- frame:
		default:
			slow = append(slow, c)
		}
	}
	h.mu.Unlock()
	for _, c := range slow {
		c.drop()
	}
}

// failed forgets the last good frame of a type that no longer encodes, so a new stream never replays it as current.
func (h *Hub) failed(t string) {
	h.mu.Lock()
	delete(h.last, t)
	h.mu.Unlock()
	codelog.Note("live "+t, "EFRAME")
}

func (h *Hub) send(t string, ev api.LiveEvent, err error) {
	if err != nil {
		h.failed(t)
		return
	}
	h.publish(t, ev)
}

func (h *Hub) Status(v api.StatusResponse) {
	var ev api.LiveEvent
	h.send("status", ev, ev.FromLiveStatusEvent(api.LiveStatusEvent{Data: v}))
}

func (h *Hub) Machine(v api.MachineSnapshot) {
	var ev api.LiveEvent
	h.send("machine", ev, ev.FromLiveMachineEvent(api.LiveMachineEvent{Data: v}))
}

func (h *Hub) Docker(v api.DockerSnapshot) {
	var ev api.LiveEvent
	h.send("docker", ev, ev.FromLiveDockerEvent(api.LiveDockerEvent{Data: v}))
}

func (h *Hub) Sessions(v []api.LiveSession) {
	var ev api.LiveEvent
	h.send("sessions", ev, ev.FromLiveSessionsEvent(api.LiveSessionsEvent{Data: v}))
}

func (h *Hub) Activity(v api.ActivityItem) {
	var ev api.LiveEvent
	h.send("activity", ev, ev.FromLiveActivityEvent(api.LiveActivityEvent{Data: v}))
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ActivityItem maps a scanner activity onto the API shape; fields that do not apply stay absent.
func ActivityItem(a transcripts.Activity) api.ActivityItem {
	return api.ActivityItem{Kind: api.ActivityKind(a.Kind), At: a.At.UTC(), Project: nonEmpty(a.Project),
		Session: nonEmpty(a.Session), AgentType: nonEmpty(a.AgentType), ErrorCode: codePtr(a.ErrorCode),
		Count: a.Count, TokensBefore: a.TokensBefore, TokensAfter: a.TokensAfter}
}

func (s *Server) GetLive(ctx context.Context, _ api.GetLiveRequestObject) (api.GetLiveResponseObject, error) {
	return api.GetLive200TexteventStreamResponse{Body: s.Hub.Open(ctx)}, nil
}

// Publish sends a frame as soon as a live sample is pushed, and the status and live sessions when they change
// (status also every StatusEvery, checked every `every`), until ctx is done; it sleeps while no sample arrives.
func (s *Server) Publish(ctx context.Context, every time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.publishStatus(ctx, max(every, StatusPoll))
	}()
	defer func() { <-done }()
	if s.Runner == nil {
		<-ctx.Done()
		return
	}
	var machineAt, dockerAt time.Time
	for {
		if e, ok := s.Runner.MachineLive.Latest(); ok && !e.At.Equal(machineAt) {
			machineAt = e.At
			if m := s.MachineNow(); m != nil {
				s.Hub.Machine(*m)
			}
		}
		if e, ok := s.Runner.DockerLive.Latest(); ok && !e.At.Equal(dockerAt) {
			dockerAt = e.At
			if d := s.DockerNow(nil); d != nil {
				s.Hub.Docker(*d)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.Runner.MachineLive.Pushed():
		case <-s.Runner.DockerLive.Pushed():
		}
	}
}

func (s *Server) publishStatus(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var statusAt time.Time
	var status, sessions []byte
	for {
		now := s.now()
		st := s.StatusNow(ctx)
		key := st
		key.GeneratedAt = time.Time{}
		key.Machine.LastAt, key.Docker.LastAt, key.Guard.LastAt = nil, nil, nil
		key.Scan.NextRunInSeconds, key.Scan.LastRunAt = nil, nil
		if b, _ := json.Marshal(key); !bytes.Equal(b, status) || now.Sub(statusAt) >= StatusEvery {
			status, statusAt = b, now
			s.Hub.Status(st)
		}
		live := s.liveSessions(nil, nil)
		if b, _ := json.Marshal(live); !bytes.Equal(b, sessions) {
			sessions = b
			s.Hub.Sessions(live)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
