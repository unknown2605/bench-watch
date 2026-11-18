package transcripts

import (
	"context"
	"errors"
	"sync"
	"time"

	"benchwatch/server/internal/guardlog"
)

const GuardEvery = time.Minute

// Guard collector states, as the API names them.
const (
	GuardOK          = "ok"
	GuardStarting    = "starting"
	GuardUnavailable = "unavailable"
	GuardFailed      = "failed"
	GuardDisabled    = "disabled"
)

type GuardStatus struct {
	State     string
	ErrorCode string
	LastAt    time.Time
}

// Guard adds the new lines of the per-call guard log to the guard log tables every GuardEvery.
type Guard struct {
	// Dir holds guard.log and its rotations (guardlog.Dir).
	Dir string
	DB  guardlog.DB
	Now func() time.Time
	// Loc gives the local day and hour of a call; nil means time.Local.
	Loc *time.Location
	// WriteTimeout bounds one pass's write; zero means 30 seconds.
	WriteTimeout time.Duration

	mu     sync.RWMutex
	status GuardStatus
	files  []guardlog.File
	loaded bool
}

func (g *Guard) Status() GuardStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.status.State == "" {
		return GuardStatus{State: GuardStarting}
	}
	return g.status
}

func (g *Guard) set(state, code string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.status.State, g.status.ErrorCode = state, code
	if state == GuardOK {
		g.status.LastAt = g.now()
	}
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Guard) Run(ctx context.Context) {
	t := time.NewTicker(GuardEvery)
	defer t.Stop()
	for {
		g.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick reads one pass. The positions advance only after the pass's counts are committed, so a failed write is
// read again next time and a restart resumes from the stored positions.
func (g *Guard) Tick(ctx context.Context) {
	if g.DB == nil {
		g.set(GuardFailed, "ENOSINK")
		return
	}
	wctx, cancel := context.WithTimeout(ctx, g.writeTimeout())
	defer cancel()
	if !g.loaded {
		files, err := guardlog.Load(wctx, g.DB)
		if err != nil {
			g.set(GuardFailed, guardCode(err))
			return
		}
		g.files, g.loaded = files, true
	}
	p, err := guardlog.Reader{Dir: g.Dir, Loc: g.Loc}.Read(g.files)
	if err != nil {
		g.set(GuardFailed, guardCode(err))
		return
	}
	if !p.Present {
		g.set(GuardUnavailable, "")
		return
	}
	if err := guardlog.Write(wctx, g.DB, p, g.now()); err != nil {
		// The server may have committed before the error reached us, so the stored positions are the truth.
		g.files, g.loaded = nil, false
		g.set(GuardFailed, guardCode(err))
		return
	}
	g.files = p.Files
	if p.Refused {
		g.set(GuardFailed, guardlog.ErrNotRegular.Error())
		return
	}
	g.set(GuardOK, "")
}

func guardCode(err error) string {
	if errors.Is(err, guardlog.ErrState) {
		return guardlog.ErrState.Error()
	}
	return codeOf(err)
}

func (g *Guard) writeTimeout() time.Duration {
	if g.WriteTimeout > 0 {
		return g.WriteTimeout
	}
	return 30 * time.Second
}
