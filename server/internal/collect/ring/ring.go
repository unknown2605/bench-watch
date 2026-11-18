// Package ring keeps the most recent samples in memory for the live views and for when PostgreSQL is down.
package ring

import (
	"sync"
	"time"
)

type Entry[T any] struct {
	At time.Time
	V  T
}

// Ring holds at most cap entries and none older than maxAge before the newest one. Times are compared as
// wall clock (monotonic reading stripped). Values are shared with readers: never mutate one after Push.
type Ring[T any] struct {
	mu     sync.RWMutex
	buf    []Entry[T]
	start  int
	n      int
	maxAge time.Duration
	pushed chan struct{}
}

func New[T any](capacity int, maxAge time.Duration) *Ring[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring[T]{buf: make([]Entry[T], capacity), maxAge: maxAge, pushed: make(chan struct{}, 1)}
}

// Pushed holds one token after any number of pushes since it was last read, so a single reader that takes the
// token and then calls Latest never misses a sample. Two readers would steal each other's tokens.
func (r *Ring[T]) Pushed() <-chan struct{} {
	return r.pushed
}

func (r *Ring[T]) Push(at time.Time, v T) {
	at = at.Round(0)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == len(r.buf) {
		r.buf[r.start] = Entry[T]{}
		r.start = (r.start + 1) % len(r.buf)
		r.n--
	}
	r.buf[(r.start+r.n)%len(r.buf)] = Entry[T]{At: at, V: v}
	r.n++
	r.evictLocked(at)
	select {
	case r.pushed <- struct{}{}:
	default:
	}
}

func (r *Ring[T]) evictLocked(now time.Time) {
	if r.maxAge <= 0 {
		return
	}
	cut := now.Add(-r.maxAge)
	for r.n > 0 && !r.buf[r.start].At.After(cut) {
		r.buf[r.start] = Entry[T]{}
		r.start = (r.start + 1) % len(r.buf)
		r.n--
	}
}

// Window returns entries newer than now-maxAge, oldest first, so a stalled source shows no stale hour.
func (r *Ring[T]) Window(now time.Time) []Entry[T] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry[T], 0, r.n)
	cut := now.Round(0).Add(-r.maxAge)
	for i := 0; i < r.n; i++ {
		e := r.buf[(r.start+i)%len(r.buf)]
		if r.maxAge > 0 && !e.At.After(cut) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (r *Ring[T]) Latest() (Entry[T], bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.n == 0 {
		return Entry[T]{}, false
	}
	return r.buf[(r.start+r.n-1)%len(r.buf)], true
}

func (r *Ring[T]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.n
}
