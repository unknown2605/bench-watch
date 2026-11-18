package history

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/store"
)

// Sink stores collector batches through store.WriteSamples; rows the schema refuses make the batch permanent.
type Sink struct {
	DB store.TxBeginner
	// Written, when set, learns the oldest sample of every stored batch, so the upkeep rolls up late batches too.
	Written *Written
}

// Written keeps the oldest sample time stored since the upkeep last took it.
type Written struct {
	mu     sync.Mutex
	oldest time.Time
}

func (w *Written) Note(t time.Time) {
	if w == nil || t.IsZero() {
		return
	}
	w.mu.Lock()
	if w.oldest.IsZero() || t.Before(w.oldest) {
		w.oldest = t
	}
	w.mu.Unlock()
}

func (w *Written) take() time.Time {
	if w == nil {
		return time.Time{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	t := w.oldest
	w.oldest = time.Time{}
	return t
}

func oldest(b collect.Batch) time.Time {
	var t time.Time
	for _, m := range b.Machine {
		if t.IsZero() || m.At.Before(t) {
			t = m.At
		}
	}
	for _, d := range b.Docker {
		if t.IsZero() || d.At.Before(t) {
			t = d.At
		}
	}
	return t
}

type permanent struct{ err error }

func (p permanent) Error() string   { return p.err.Error() }
func (p permanent) Unwrap() error   { return p.err }
func (p permanent) Permanent() bool { return true }

func (s Sink) Write(ctx context.Context, b collect.Batch) error {
	if s.DB == nil {
		return errors.New("no database")
	}
	sb := Convert(b)
	if _, err := store.WriteSamples(ctx, s.DB, sb); err != nil {
		if store.IsPermanent(err) {
			return permanent{fmt.Errorf("history write: %w", err)}
		}
		return err
	}
	s.Written.Note(oldest(b))
	return nil
}
