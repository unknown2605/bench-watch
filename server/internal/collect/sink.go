package collect

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrPermanent marks a write that can never succeed, such as a row the schema refuses: the batch is dropped.
var ErrPermanent = errors.New("permanent sink error")

// IsPermanent is true for errors.Is(err, ErrPermanent) or an error with Permanent() true; anything else is retried.
func IsPermanent(err error) bool {
	if errors.Is(err, ErrPermanent) {
		return true
	}
	var p interface{ Permanent() bool }
	return errors.As(err, &p) && p.Permanent()
}

// Key identifies a batch: its source and the time of its first row when first sent. A retry resends the same
// rows under the same key, less rows that expired meanwhile.
type Key struct {
	Source string
	At     time.Time
}

// Batch holds rows of one source only. The writer inserts with ON CONFLICT DO NOTHING on each table's
// (at[, name]) key, so a batch written twice stores its rows once.
type Batch struct {
	Key     Key
	Machine []MachineSample
	Docker  []DockerSample
	Events  []DockerEvent
}

func (b Batch) Len() int { return len(b.Machine) + len(b.Docker) + len(b.Events) }

// Sink.Write must return soon after ctx is done. An error that is not permanent is retried with the same batch
// until its rows expire, so the writer returns permanent errors for rows that can never be stored.
type Sink interface {
	Write(ctx context.Context, b Batch) error
}

// MemSink is the in-memory test sink. Like the PostgreSQL writer it keeps a row once per (at[, name]) key.
type MemSink struct {
	mu      sync.Mutex
	fail    error
	failN   int
	writes  int
	dups    int
	batches []Batch
	rows    map[string]bool
}

func (m *MemSink) SetFail(err error) {
	m.mu.Lock()
	m.fail, m.failN = err, -1
	if err == nil {
		m.failN = 0
	}
	m.mu.Unlock()
}

func (m *MemSink) FailNext(n int, err error) {
	m.mu.Lock()
	m.fail, m.failN = err, n
	m.mu.Unlock()
}

func (m *MemSink) Write(ctx context.Context, b Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if m.failN != 0 && m.fail != nil {
		if m.failN > 0 {
			m.failN--
		}
		return m.fail
	}
	if m.rows == nil {
		m.rows = map[string]bool{}
	}
	var kept Batch
	kept.Key = b.Key
	for _, s := range b.Machine {
		if m.insert("m" + s.At.Format(time.RFC3339Nano)) {
			kept.Machine = append(kept.Machine, s)
		}
	}
	for _, s := range b.Docker {
		if m.insert("d" + s.At.Format(time.RFC3339Nano)) {
			kept.Docker = append(kept.Docker, s)
		}
	}
	for _, e := range b.Events {
		if m.insert("e" + e.At.Format(time.RFC3339Nano) + e.Name + e.Kind) {
			kept.Events = append(kept.Events, e)
		}
	}
	if kept.Len() > 0 {
		m.batches = append(m.batches, kept)
	}
	return nil
}

func (m *MemSink) insert(key string) bool {
	if m.rows[key] {
		m.dups++
		return false
	}
	m.rows[key] = true
	return true
}

// Writes counts every call, failed or not; Duplicates counts rows that were already stored.
func (m *MemSink) Writes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writes
}

func (m *MemSink) Duplicates() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dups
}

func (m *MemSink) Batches() []Batch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Batch(nil), m.batches...)
}

func (m *MemSink) Totals() (machine, docker, events int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.batches {
		machine += len(b.Machine)
		docker += len(b.Docker)
		events += len(b.Events)
	}
	return
}
