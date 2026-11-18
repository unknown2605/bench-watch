package guardlog

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"benchwatch/server/internal/store"
)

type DB interface {
	store.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

var hash16 = regexp.MustCompile(`^(?:[0-9a-f]{16})?$`)

// ErrState is the code for a stored state row the reader does not accept.
var ErrState = errors.New("EGUARDSTATE")

// Load returns the stored positions; none when no pass was stored yet.
func Load(ctx context.Context, db store.DBTX) ([]File, error) {
	row, err := store.New(db).GuardLogState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []File
	if json.Unmarshal(row.Files, &files) != nil {
		return nil, ErrState
	}
	for _, f := range files {
		if f.Offset < 0 || !hash16.MatchString(f.First) {
			return nil, ErrState
		}
	}
	return files, nil
}

type bucket struct {
	lo float64
	hi *float64
}

// Write adds the pass's counts and stores its positions in one transaction, so a pass is stored whole or not at all.
func Write(ctx context.Context, db DB, p Pass, now time.Time) (err error) {
	files, err := json.Marshal(append([]File{}, p.Files...))
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	q := store.New(tx)
	for k, c := range p.Hours {
		if err = q.AddGuardCallHour(ctx, store.AddGuardCallHourParams{Day: k.Day, Hour: int16(k.Hour),
			Tool: store.GuardTool(k.Tool), Decision: store.GuardDecision(k.Decision), Sub: k.Sub, Calls: c.Calls,
			TimeCaps: c.TimeCaps, FailClosed: c.FailClosed, SubDeny: c.SubDeny}); err != nil {
			return err
		}
	}
	if len(p.Latency) > 0 {
		var bs []store.GuardLatencyBucket
		if bs, err = q.GuardLatencyBuckets(ctx); err != nil {
			return err
		}
		type lkey struct {
			day  time.Time
			tool string
			b    int
		}
		sums := map[lkey]int64{}
		for k, n := range p.Latency {
			i := sort.Search(len(bs), func(i int) bool { return bs[i].LoMs > float64(k.Ms) }) - 1
			if i < 0 {
				return ErrState
			}
			sums[lkey{k.Day, k.Tool, i}] += n
		}
		for k, n := range sums {
			if err = q.AddGuardCallLatency(ctx, store.AddGuardCallLatencyParams{Day: k.day,
				Tool: store.GuardTool(k.tool), LoMs: bs[k.b].LoMs, HiMs: bs[k.b].HiMs, Calls: n}); err != nil {
				return err
			}
		}
	}
	for k, n := range p.Rules {
		if err = q.AddGuardRule(ctx, store.AddGuardRuleParams{Day: k.Day, Tool: store.GuardTool(k.Tool),
			Decision: store.GuardDecision(k.Decision), Rule: k.Rule, Calls: n}); err != nil {
			return err
		}
	}
	s := p.Skipped
	prm := store.SaveGuardLogStateParams{Files: files, Malformed: s.Malformed, Oversized: s.Oversized,
		BadVersion: s.BadVersion, BadRule: s.BadRule, NotRegular: s.NotRegular, Truncated: s.Truncated,
		ReadAt: pgtype.Timestamptz{Time: now, Valid: true}}
	if p.Deny != nil {
		tool := store.GuardTool(p.Deny.Tool)
		prm.LastDenyAt, prm.LastDenyTool = pgtype.Timestamptz{Time: p.Deny.At, Valid: true}, &tool
		if p.Deny.Rule != "" {
			prm.LastDenyRule = &p.Deny.Rule
		}
	}
	if err = q.SaveGuardLogState(ctx, prm); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
