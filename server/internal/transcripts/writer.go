// Package transcripts runs the transcript scanner on a timer, writes its days into PostgreSQL and keeps the live
// session list and activity feed in memory.
package transcripts

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/store"
)

// DB is what the scanner needs from a pool or a connection.
type DB interface {
	store.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WriteDays replaces the given days in one transaction: clear_transcript_days(days), then the rows of those days.
func WriteDays(ctx context.Context, db DB, rows *aggregate.Rows, days []time.Time) (err error) {
	if len(days) == 0 {
		return nil
	}
	keep := make(map[time.Time]bool, len(days))
	for _, d := range days {
		keep[d] = true
	}
	r := rows.Only(keep)
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
	if _, err = q.ClearTranscriptDays(ctx, days); err != nil {
		return err
	}
	// Both tables are keyed by a hash alone; a key stored under another day (a time zone change) would collide.
	boundaries := make([]string, len(r.Boundaries))
	for i, b := range r.Boundaries {
		boundaries[i] = b.Boundary
	}
	results := make([]string, len(r.TopResults))
	for i, t := range r.TopResults {
		results[i] = t.Result
	}
	if _, err = tx.Exec(ctx, "DELETE FROM public.compaction_events WHERE boundary = ANY($1)", boundaries); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM public.tool_result_top WHERE result = ANY($1)", results); err != nil {
		return err
	}
	steps := []func() error{
		func() error { _, e := q.InsertUsageDaily(ctx, r.Usage); return e },
		func() error { _, e := q.InsertContextBandDaily(ctx, r.ContextBands); return e },
		func() error { _, e := q.InsertCompactionDaily(ctx, r.Compactions); return e },
		func() error { _, e := q.InsertCompactionEvents(ctx, r.Boundaries); return e },
		func() error { _, e := q.InsertSessionDaily(ctx, r.Sessions); return e },
		func() error { _, e := q.InsertSubagentDaily(ctx, r.Subagents); return e },
		func() error { _, e := q.InsertSubagentRunDaily(ctx, r.SubagentRuns); return e },
		func() error { _, e := q.InsertToolResultDaily(ctx, r.Results); return e },
		func() error { _, e := q.InsertToolResultSizeDaily(ctx, r.ResultSizes); return e },
		func() error { _, e := q.InsertToolResultTop(ctx, r.TopResults); return e },
		func() error { _, e := q.InsertToolErrorDaily(ctx, r.Errors); return e },
	}
	for _, step := range steps {
		if err = step(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Bands reads token_bands; the migration's defaults stand in for a kind with no rows. A failed read returns the
// defaults and the error, so the caller can build live figures but must not store rows banded by them.
func Bands(ctx context.Context, db store.DBTX) (contextBands, resultBands []aggregate.Band, err error) {
	rows, err := store.New(db).ListTokenBands(ctx)
	for _, b := range rows {
		band := aggregate.Band{Min: b.MinTokens, Max: b.MaxTokens}
		if b.Kind == store.TokenBandKindContext {
			contextBands = append(contextBands, band)
		} else {
			resultBands = append(resultBands, band)
		}
	}
	if err != nil {
		contextBands, resultBands = nil, nil
	}
	if len(contextBands) == 0 {
		contextBands = aggregate.DefaultContextBands
	}
	if len(resultBands) == 0 {
		resultBands = aggregate.DefaultResultBands
	}
	return contextBands, resultBands, err
}
