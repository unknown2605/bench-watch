// Package clocktest is a manual collect.Clock for tests: time moves only on Advance or Sleep.
package clocktest

import (
	"sync"
	"time"

	"benchwatch/server/internal/collect"
)

type Clock struct {
	mu      sync.Mutex
	wall    time.Time
	mono    time.Duration
	tickers []*ticker
	afters  []after
}

type ticker struct {
	c     *Clock
	every time.Duration
	next  time.Duration
	ch    chan time.Time
	dead  bool
}

type after struct {
	at time.Duration
	ch chan time.Time
}

func New(wall time.Time) *Clock { return &Clock{wall: wall.Round(0)} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *Clock) Elapsed() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

func (c *Clock) NewTicker(d time.Duration) collect.Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &ticker{c: c, every: d, next: c.mono + d, ch: make(chan time.Time, 1)}
	c.tickers = append(c.tickers, t)
	return t
}

func (c *Clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.wall
		return ch
	}
	c.afters = append(c.afters, after{at: c.mono + d, ch: ch})
	return ch
}

func (c *Clock) Tickers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.tickers {
		if !t.dead {
			n++
		}
	}
	return n
}

func (c *Clock) Afters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.afters)
}

// Advance moves both clocks; a ticker that is due fires once, and a full channel drops the tick, as time.Ticker does.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mono += d
	c.wall = c.wall.Add(d)
	for _, t := range c.tickers {
		if t.dead || t.next > c.mono {
			continue
		}
		for t.next <= c.mono {
			t.next += t.every
		}
		select {
		case t.ch <- c.wall:
		default:
		}
	}
	kept := c.afters[:0]
	for _, a := range c.afters {
		if a.at <= c.mono {
			a.ch <- c.wall
		} else {
			kept = append(kept, a)
		}
	}
	c.afters = kept
}

// Sleep moves wall time only, as a macOS sleep does to Go's monotonic clock.
func (c *Clock) Sleep(d time.Duration) {
	c.mu.Lock()
	c.wall = c.wall.Add(d)
	c.mu.Unlock()
}

func (t *ticker) C() <-chan time.Time { return t.ch }
func (t *ticker) Stop() {
	t.c.mu.Lock()
	t.dead = true
	t.c.mu.Unlock()
}
