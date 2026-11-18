-- The scanner recomputes whole days. In one transaction it calls ClearTranscriptDays with the days it rebuilt, then
-- copies them; days it did not rebuild stay as stored, even when their transcripts are gone.

-- name: ClearTranscriptDays :one
SELECT clear_transcript_days(@rebuilt::date[])::bigint AS rows;

-- name: InsertUsageDaily :copyfrom
INSERT INTO usage_daily (
    day, project, scope, model, calls, input_tokens, cache_write_tokens, cache_read_tokens, output_tokens,
    context_tokens, cost_input_usd, cost_cache_write_usd, cost_cache_read_usd, cost_output_usd, client
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: InsertContextBandDaily :copyfrom
INSERT INTO context_band_daily (
    day, project, scope, min_tokens, max_tokens, calls, context_tokens, cache_read_tokens, client
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: InsertCompactionDaily :copyfrom
INSERT INTO compaction_daily (day, project, scope, trigger, compactions, tokens_before)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: InsertCompactionEvents :copyfrom
INSERT INTO compaction_events (boundary, at, day, project, session, scope, trigger, tokens_before, tokens_after)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: InsertSessionDaily :copyfrom
INSERT INTO session_daily (
    day, project, session, scope, model, calls, input_tokens, cache_write_tokens, cache_read_tokens, output_tokens,
    context_tokens, peak_context_tokens, last_context_tokens, cost_usd, first_at, last_at, client
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17);

-- name: InsertSubagentDaily :copyfrom
INSERT INTO subagent_daily (
    day, project, session, agent_type, model, calls, context_tokens, cache_read_tokens, output_tokens, cost_usd
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: InsertSubagentRunDaily :copyfrom
INSERT INTO subagent_run_daily (day, project, session, agent_type, runs, first_context_tokens, peak_context_tokens, calls)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: InsertToolResultDaily :copyfrom
INSERT INTO tool_result_daily (
    day, project, scope, tool, results, tokens, weighted_tokens, weighted_cost_usd, max_tokens, repeat_results,
    repeat_measured
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: InsertToolResultSizeDaily :copyfrom
INSERT INTO tool_result_size_daily (day, project, scope, tool, min_tokens, max_tokens, results, tokens)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: InsertToolResultTop :copyfrom
INSERT INTO tool_result_top (result, day, project, scope, tool, tokens, resends, weighted_tokens)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: InsertToolErrorDaily :copyfrom
INSERT INTO tool_error_daily (day, project, scope, tool, error_class, errors)
VALUES ($1, $2, $3, $4, $5, $6);
