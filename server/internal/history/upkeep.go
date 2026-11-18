package history

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"benchwatch/server/internal/codelog"
	"benchwatch/server/internal/store"
)

// Upkeep rolls history samples into 1-minute and 1-hour buckets, creates partitions ahead and drops expired ones.
type Upkeep struct {
	DB    store.DBTX
	Every time.Duration
	// DropEvery: drop_expired_partitions rolls up what it drops first, so a long pause loses no averages.
	DropEvery time.Duration
	Now       func() time.Time
	// Written is the sink's record of late batches; a round rolls up from the oldest one it holds.
	Written *Written

	mu       sync.Mutex
	minuteAt time.Time
	lastDrop time.Time
	lastErr  error
	lastOK   time.Time
	dropped  int
}

func (u *Upkeep) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Upkeep) Run(ctx context.Context) {
	every := u.Every
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		err := u.Tick(ctx)
		if ctx.Err() == nil {
			codelog.Note("upkeep", codelog.Of(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one round; after an error the next round starts again from the last bucket it rolled up.
func (u *Upkeep) Tick(ctx context.Context) error {
	err := u.tick(ctx)
	u.mu.Lock()
	u.lastErr = err
	if err == nil {
		u.lastOK = u.now()
	}
	u.mu.Unlock()
	return err
}

func (u *Upkeep) tick(ctx context.Context) (err error) {
	late := u.Written.take()
	defer func() {
		if err != nil {
			u.Written.Note(late)
		}
	}()
	q := store.New(u.DB)
	now := u.now()
	if _, err := q.EnsurePartitions(ctx, store.EnsurePartitionsParams{At: ts(now), Ahead: 1}); err != nil {
		return err
	}
	u.mu.Lock()
	since := u.minuteAt
	u.mu.Unlock()
	if since.IsZero() {
		if since, err = u.resumeFrom(ctx, now); err != nil {
			return err
		}
	}
	if !late.IsZero() && late.Before(since) {
		since = late.Truncate(time.Minute)
	}
	if _, err := q.RollupMinutes(ctx, store.RollupMinutesParams{Since: ts(since), Until: ts(now)}); err != nil {
		return err
	}
	hourFrom := since.Truncate(time.Hour).Add(-time.Hour)
	if _, err := q.RollupHours(ctx, store.RollupHoursParams{Since: ts(hourFrom), Until: ts(now)}); err != nil {
		return err
	}
	u.mu.Lock()
	// Two minutes back, so samples flushed late into the last minute are counted on the next round.
	u.minuteAt = now.Truncate(time.Minute).Add(-2 * time.Minute)
	due := u.lastDrop.IsZero() || now.Sub(u.lastDrop) >= u.dropEvery()
	u.mu.Unlock()
	if !due {
		return nil
	}
	dropped, err := q.DropExpiredPartitions(ctx, ts(now))
	if err != nil {
		return err
	}
	u.mu.Lock()
	u.lastDrop = now
	u.dropped += len(dropped)
	u.mu.Unlock()
	return nil
}

func (u *Upkeep) dropEvery() time.Duration {
	if u.DropEvery > 0 {
		return u.DropEvery
	}
	return time.Hour
}

// resumeFrom starts at the newest stored minute bucket, or 15 days back (past the 14-day detail tier) when none.
func (u *Upkeep) resumeFrom(ctx context.Context, now time.Time) (time.Time, error) {
	var last pgtype.Timestamptz
	if err := u.DB.QueryRow(ctx, "SELECT max(bucket) FROM public.machine_rollup_1m").Scan(&last); err != nil {
		return time.Time{}, err
	}
	if !last.Valid || last.Time.After(now) {
		return now.Add(-15 * 24 * time.Hour), nil
	}
	return last.Time.Add(-2 * time.Minute), nil
}

// State reports the last round's error, when the last round succeeded, and how many partitions were dropped.
func (u *Upkeep) State() (lastErr error, lastOK time.Time, dropped int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastErr, u.lastOK, u.dropped
}
