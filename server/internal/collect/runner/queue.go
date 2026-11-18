package runner

import (
	"slices"
	"time"

	"benchwatch/server/internal/collect"
)

type batcher interface {
	batch(max int) collect.Batch
	finish()
}

// queue holds unwritten rows of one source, oldest first. The first inflight rows are the batch being
// written: a retry sends them under the same key, less any that expired.
type queue[T any] struct {
	source   string
	max      int
	at       func(T) time.Time
	set      func(*collect.Batch, []T)
	items    []T
	inflight int
	key      collect.Key
}

// newQueue keeps room for 2 rows, because a batch in flight must leave one for the newest.
func newQueue[T any](source string, size int, at func(T) time.Time, set func(*collect.Batch, []T)) *queue[T] {
	return &queue[T]{source: source, max: max(size, 2), at: at, set: set}
}

func (q *queue[T]) len() int { return len(q.items) }

// push: the count cap includes the batch in flight, whose rows are never dropped.
func (q *queue[T]) push(v T) (dropped uint64) {
	q.items = append(q.items, v)
	if over := min(len(q.items)-q.max, len(q.items)-q.inflight); over > 0 {
		q.items = slices.Delete(q.items, q.inflight, q.inflight+over)
		dropped = uint64(over)
	}
	return dropped
}

func (q *queue[T]) expire(cut time.Time) uint64 {
	n := 0
	for n < len(q.items) && q.at(q.items[n]).Before(cut) {
		n++
	}
	q.items = slices.Delete(q.items, 0, n)
	q.inflight = max(0, q.inflight-n)
	return uint64(n)
}

// batch leaves room for one row outside the batch, so a full queue still takes the newest row.
func (q *queue[T]) batch(max int) collect.Batch {
	if q.inflight == 0 {
		q.inflight = min(max, len(q.items), q.max-1)
		if q.inflight > 0 {
			q.key = collect.Key{Source: q.source, At: q.at(q.items[0])}
		}
	}
	if q.inflight == 0 {
		return collect.Batch{}
	}
	b := collect.Batch{Key: q.key}
	q.set(&b, slices.Clone(q.items[:q.inflight]))
	return b
}

func (q *queue[T]) finish() {
	q.items = slices.Delete(q.items, 0, q.inflight)
	q.inflight = 0
}
