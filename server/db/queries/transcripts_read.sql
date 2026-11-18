-- name: UsageTotals :one
SELECT
    coalesce(sum(calls), 0)::bigint AS calls,
    coalesce(sum(input_tokens), 0)::bigint AS input_tokens,
    coalesce(sum(cache_write_tokens), 0)::bigint AS cache_write_tokens,
    coalesce(sum(cache_read_tokens), 0)::bigint AS cache_read_tokens,
    coalesce(sum(output_tokens), 0)::bigint AS output_tokens,
    coalesce(sum(context_tokens), 0)::bigint AS context_tokens,
    coalesce(sum(cost_input_usd), 0)::numeric AS cost_input_usd,
    coalesce(sum(cost_cache_write_usd), 0)::numeric AS cost_cache_write_usd,
    coalesce(sum(cost_cache_read_usd), 0)::numeric AS cost_cache_read_usd,
    coalesce(sum(cost_output_usd), 0)::numeric AS cost_output_usd,
    coalesce(sum(context_tokens + output_tokens) FILTER (WHERE scope = 'main'), 0)::bigint AS main_tokens,
    coalesce(sum(context_tokens + output_tokens) FILTER (WHERE scope = 'sub'), 0)::bigint AS sub_tokens,
    coalesce(sum(cost_input_usd + cost_cache_write_usd + cost_cache_read_usd + cost_output_usd)
        FILTER (WHERE scope = 'sub'), 0)::numeric AS sub_cost_usd
FROM usage_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text);

-- name: UsageDaily :many
SELECT
    day,
    coalesce(sum(context_tokens + output_tokens) FILTER (WHERE scope = 'main'), 0)::bigint AS main_tokens,
    coalesce(sum(context_tokens + output_tokens) FILTER (WHERE scope = 'sub'), 0)::bigint AS sub_tokens,
    sum(calls)::bigint AS calls,
    sum(cost_input_usd + cost_cache_write_usd + cost_cache_read_usd + cost_output_usd)::numeric AS cost_usd,
    coalesce(sum(cost_input_usd + cost_cache_write_usd + cost_cache_read_usd + cost_output_usd)
        FILTER (WHERE scope = 'sub'), 0)::numeric AS sub_cost_usd,
    sum(cache_read_tokens)::bigint AS cache_read_tokens,
    sum(context_tokens)::bigint AS context_tokens
FROM usage_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
GROUP BY day
ORDER BY day;

-- name: UsageByModel :many
SELECT
    model,
    sum(context_tokens + output_tokens)::bigint AS tokens,
    sum(cost_input_usd + cost_cache_write_usd + cost_cache_read_usd + cost_output_usd)::numeric AS cost_usd
FROM usage_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
GROUP BY model
ORDER BY cost_usd DESC, model;

-- name: UsageByClient :many
SELECT
    client::text AS client,
    sum(context_tokens + output_tokens)::bigint AS tokens,
    sum(cost_input_usd + cost_cache_write_usd + cost_cache_read_usd + cost_output_usd)::numeric AS cost_usd
FROM usage_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
GROUP BY client
ORDER BY tokens DESC, client;

-- name: UsageTopDays :many
WITH per_model AS (
    SELECT day, model,
        sum(context_tokens + output_tokens) AS tokens,
        sum(cost_input_usd + cost_cache_write_usd + cost_cache_read_usd + cost_output_usd) AS cost_usd
    FROM usage_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
        AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
    GROUP BY day, model
)
SELECT
    day,
    sum(tokens)::bigint AS tokens,
    sum(cost_usd)::numeric AS cost_usd,
    (array_agg(model ORDER BY cost_usd DESC, model))[1]::text AS top_model
FROM per_model
GROUP BY day
ORDER BY cost_usd DESC, day DESC
LIMIT @max_rows::integer;

-- name: ContextBands :many
SELECT
    min_tokens,
    max_tokens,
    coalesce(sum(calls) FILTER (WHERE scope = 'main'), 0)::bigint AS main_calls,
    coalesce(sum(calls) FILTER (WHERE scope = 'sub'), 0)::bigint AS sub_calls,
    sum(context_tokens)::bigint AS context_tokens,
    sum(cache_read_tokens)::bigint AS cache_read_tokens
FROM context_band_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
GROUP BY min_tokens, max_tokens
ORDER BY min_tokens, max_tokens NULLS LAST;

-- name: PeakContext :one
SELECT project, day, model, client::text AS client, peak_context_tokens
FROM session_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
ORDER BY peak_context_tokens DESC, day DESC
LIMIT 1;

-- name: PeakContextByProject :many
WITH s AS (
    SELECT project,
        count(DISTINCT session)::integer AS sessions,
        max(peak_context_tokens)::bigint AS peak_context_tokens,
        (array_agg(model ORDER BY peak_context_tokens DESC))[1]::text AS peak_model,
        (array_agg(client::text ORDER BY peak_context_tokens DESC))[1]::text AS peak_client
    FROM session_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
        AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
    GROUP BY project
), c AS (
    SELECT project, sum(compactions)::integer AS compactions
    FROM compaction_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(client)::text IS NULL OR sqlc.narg(client)::text = 'claude-code')
    GROUP BY project
)
SELECT s.project, s.sessions, s.peak_context_tokens, s.peak_model, s.peak_client,
       coalesce(c.compactions, 0)::integer AS compactions
FROM s
LEFT JOIN c ON c.project = s.project
ORDER BY s.peak_context_tokens DESC, s.project;

-- name: DailyPeakContext :many
WITH s AS (
    SELECT day, max(peak_context_tokens)::bigint AS peak_context_tokens
    FROM session_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
        AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
    GROUP BY day
), c AS (
    SELECT day, sum(compactions)::integer AS compactions
    FROM compaction_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
        AND (sqlc.narg(client)::text IS NULL OR sqlc.narg(client)::text = 'claude-code')
    GROUP BY day
), u AS (
    SELECT day, sum(cache_read_tokens)::bigint AS cache_read_tokens, sum(context_tokens)::bigint AS context_tokens,
        sum(calls)::bigint AS calls
    FROM usage_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
        AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
    GROUP BY day
), d AS (
    SELECT day FROM s UNION SELECT day FROM c UNION SELECT day FROM u
)
SELECT d.day::date AS day,
    coalesce(s.peak_context_tokens, 0)::bigint AS peak_context_tokens,
    coalesce(c.compactions, 0)::integer AS compactions,
    coalesce(u.cache_read_tokens, 0)::bigint AS cache_read_tokens,
    coalesce(u.context_tokens, 0)::bigint AS context_tokens,
    coalesce(u.calls, 0)::bigint AS calls
FROM d
LEFT JOIN s ON s.day = d.day
LEFT JOIN c ON c.day = d.day
LEFT JOIN u ON u.day = d.day
ORDER BY d.day;

-- name: CompactionTotals :one
SELECT
    count(*)::bigint AS compactions,
    coalesce(avg(tokens_before - tokens_after), 0)::bigint AS avg_freed_tokens
FROM compaction_events
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text);

-- name: CompactionDays :many
SELECT day, count(*)::integer AS compactions
FROM compaction_events
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
GROUP BY day
ORDER BY day;

-- name: RecentCompactions :many
SELECT at, project, session, scope, trigger, tokens_before, tokens_after
FROM compaction_events
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
ORDER BY at DESC
LIMIT @max_rows::integer;

-- name: SessionsInPeriod :many
-- Sub-agent runs and compactions are Claude Code records, so only a claude-code session joins them.
WITH per_model AS (
    SELECT project, session, client, model, sum(calls) AS calls
    FROM session_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
        AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
    GROUP BY project, session, client, model
), top_model AS (
    SELECT DISTINCT ON (project, session, client) project, session, client, model
    FROM per_model
    ORDER BY project, session, client, calls DESC, model
), totals AS (
    SELECT project, session, client,
        min(first_at)::timestamptz AS first_at,
        max(last_at)::timestamptz AS last_at,
        sum(calls)::bigint AS calls,
        sum(context_tokens + output_tokens)::bigint AS tokens,
        max(peak_context_tokens)::bigint AS peak_context_tokens,
        sum(cost_usd)::numeric AS cost_usd
    FROM session_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
        AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
    GROUP BY project, session, client
), runs AS (
    SELECT project, session, sum(runs)::integer AS runs
    FROM subagent_run_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
    GROUP BY project, session
), comps AS (
    SELECT project, session, count(*)::integer AS compactions
    FROM compaction_events
    WHERE day BETWEEN @day_from::date AND @day_to::date
    GROUP BY project, session
)
SELECT t.project, t.session, t.client::text AS client, m.model, t.first_at, t.last_at, t.calls, t.tokens,
    t.peak_context_tokens, t.cost_usd,
    coalesce(r.runs, 0)::integer AS subagent_runs,
    coalesce(c.compactions, 0)::integer AS compactions
FROM totals t
JOIN top_model m ON m.project = t.project AND m.session = t.session AND m.client = t.client
LEFT JOIN runs r ON r.project = t.project AND r.session = t.session AND t.client = 'claude-code'
LEFT JOIN comps c ON c.project = t.project AND c.session = t.session AND t.client = 'claude-code'
ORDER BY t.last_at DESC
LIMIT @max_rows::integer;

-- name: SessionSummaryByKey :one
-- The whole history of one session, whatever period the list was showing. HAVING turns the one all-NULL row an
-- aggregate gives for an unknown session into no row (pgx.ErrNoRows), which the handler answers with 404.
WITH rows AS (
    SELECT * FROM session_daily WHERE project = @project::text AND session = @session::text
        AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text)
)
SELECT
    min(first_at)::timestamptz AS first_at,
    max(last_at)::timestamptz AS last_at,
    sum(calls)::bigint AS calls,
    sum(context_tokens + output_tokens)::bigint AS tokens,
    max(peak_context_tokens)::bigint AS peak_context_tokens,
    sum(cost_usd)::numeric AS cost_usd,
    (SELECT model FROM rows GROUP BY model ORDER BY sum(calls) DESC, model LIMIT 1)::text AS model,
    (SELECT client FROM rows GROUP BY client ORDER BY sum(calls) DESC, client LIMIT 1)::text AS client,
    coalesce((SELECT last_context_tokens FROM rows WHERE scope = 'main' ORDER BY last_at DESC LIMIT 1), 0)::bigint
        AS last_context_tokens,
    coalesce((SELECT sum(runs) FROM subagent_run_daily
        WHERE project = @project::text AND session = @session::text), 0)::integer AS subagent_runs,
    (SELECT count(*) FROM compaction_events
        WHERE project = @project::text AND session = @session::text)::integer AS compactions
FROM rows
HAVING count(*) > 0;

-- name: SessionTokenMix :one
SELECT
    coalesce(sum(input_tokens), 0)::bigint AS input_tokens,
    coalesce(sum(cache_write_tokens), 0)::bigint AS cache_write_tokens,
    coalesce(sum(cache_read_tokens), 0)::bigint AS cache_read_tokens,
    coalesce(sum(output_tokens), 0)::bigint AS output_tokens,
    count(*)::bigint AS rows_found
FROM session_daily
WHERE project = @project::text AND session = @session::text
    AND (sqlc.narg(client)::text IS NULL OR client::text = sqlc.narg(client)::text);

-- name: SessionSubagents :many
SELECT r.agent_type,
    sum(r.runs)::integer AS runs,
    coalesce((SELECT sum(d.context_tokens + d.output_tokens) FROM subagent_daily d
        WHERE d.project = r.project AND d.session = r.session AND d.agent_type = r.agent_type), 0)::bigint AS tokens
FROM subagent_run_daily r
WHERE r.project = @project::text AND r.session = @session::text
GROUP BY r.project, r.session, r.agent_type
ORDER BY tokens DESC, r.agent_type;

-- name: SessionCompactions :many
SELECT at, project, session, scope, trigger, tokens_before, tokens_after
FROM compaction_events
WHERE project = @project::text AND session = @session::text
ORDER BY at DESC
LIMIT @max_rows::integer;

-- name: SubagentTypes :many
WITH usage AS (
    SELECT agent_type, model,
        sum(calls) AS calls,
        sum(context_tokens) AS context_tokens,
        sum(cache_read_tokens) AS cache_read_tokens,
        sum(context_tokens + output_tokens) AS tokens,
        sum(cost_usd) AS cost_usd
    FROM subagent_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    GROUP BY agent_type, model
), by_type AS (
    SELECT agent_type,
        (array_agg(model ORDER BY calls DESC, model))[1]::text AS top_model,
        sum(calls)::bigint AS calls,
        sum(context_tokens)::bigint AS context_tokens,
        sum(cache_read_tokens)::bigint AS cache_read_tokens,
        sum(tokens)::bigint AS tokens,
        sum(cost_usd)::numeric AS cost_usd
    FROM usage
    GROUP BY agent_type
), runs AS (
    SELECT agent_type,
        sum(runs)::bigint AS runs,
        sum(first_context_tokens)::bigint AS first_context_tokens,
        sum(peak_context_tokens)::bigint AS peak_context_tokens
    FROM subagent_run_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    GROUP BY agent_type
)
SELECT b.agent_type, b.top_model, b.calls, b.context_tokens, b.cache_read_tokens, b.tokens, b.cost_usd,
    coalesce(r.runs, 0)::bigint AS runs,
    coalesce(r.first_context_tokens, 0)::bigint AS first_context_tokens,
    coalesce(r.peak_context_tokens, 0)::bigint AS peak_context_tokens
FROM by_type b
LEFT JOIN runs r ON r.agent_type = b.agent_type
ORDER BY runs DESC, b.agent_type;

-- name: SubagentRunsTotal :one
SELECT coalesce(sum(runs), 0)::bigint AS runs
FROM subagent_run_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text);

-- name: SubagentRunsDaily :many
SELECT day, agent_type, sum(runs)::integer AS runs
FROM subagent_run_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
GROUP BY day, agent_type
ORDER BY day, agent_type;

-- name: ToolStats :many
SELECT tool,
    sum(results)::bigint AS results,
    sum(tokens)::bigint AS tokens,
    sum(weighted_tokens)::bigint AS weighted_tokens,
    sum(weighted_cost_usd)::numeric AS weighted_cost_usd,
    max(max_tokens)::bigint AS max_tokens,
    coalesce(sum(repeat_results) FILTER (WHERE repeat_measured), 0)::bigint AS repeat_results,
    coalesce(sum(results) FILTER (WHERE repeat_measured), 0)::bigint AS repeat_measured_results
FROM tool_result_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
GROUP BY tool
ORDER BY weighted_tokens DESC, tool;

-- name: ToolLargestResult :one
SELECT day, project, tool, max_tokens
FROM tool_result_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
ORDER BY max_tokens DESC, day DESC
LIMIT 1;

-- name: ToolSizeBands :many
SELECT tool, min_tokens, max_tokens, sum(results)::bigint AS results, sum(tokens)::bigint AS tokens
FROM tool_result_size_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
GROUP BY tool, min_tokens, max_tokens
ORDER BY tool, min_tokens, max_tokens NULLS LAST;

-- name: ToolByProject :many
SELECT project, tool, sum(results)::bigint AS results, sum(tokens)::bigint AS tokens,
    sum(weighted_tokens)::bigint AS weighted_tokens
FROM tool_result_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
GROUP BY project, tool
ORDER BY project, weighted_tokens DESC;

-- name: ToolDaily :many
SELECT day, tool, sum(results)::bigint AS results, sum(tokens)::bigint AS tokens
FROM tool_result_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
GROUP BY day, tool
ORDER BY day, tool;

-- name: TopToolResults :many
SELECT result, day, project, tool, tokens, resends, weighted_tokens
FROM tool_result_top
WHERE day BETWEEN @day_from::date AND @day_to::date
    AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
ORDER BY weighted_tokens DESC, result
LIMIT @max_rows::integer;

-- name: ToolErrors :many
WITH e AS (
    SELECT tool, error_class, sum(errors)::bigint AS errors
    FROM tool_error_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    GROUP BY tool, error_class
), r AS (
    SELECT tool, sum(results)::bigint AS results
    FROM tool_result_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    GROUP BY tool
)
SELECT e.tool, e.error_class, e.errors, coalesce(r.results, 0)::bigint AS results
FROM e
LEFT JOIN r ON r.tool = e.tool
ORDER BY e.errors DESC, e.tool;

-- name: ListProjects :many
SELECT DISTINCT project FROM usage_daily ORDER BY project;

-- name: ListClients :many
SELECT client::text AS client FROM usage_daily
UNION SELECT client::text FROM session_daily
UNION SELECT client::text FROM context_band_daily
ORDER BY 1;

-- name: ListModels :many
SELECT DISTINCT model FROM usage_daily ORDER BY model;

-- name: ListAgentTypes :many
SELECT DISTINCT agent_type FROM subagent_run_daily ORDER BY agent_type;

-- name: ListTools :many
SELECT DISTINCT tool FROM tool_result_daily ORDER BY tool;

-- name: ListTokenBands :many
SELECT kind, min_tokens, max_tokens FROM token_bands ORDER BY kind, min_tokens;

-- name: RereadTarget :one
SELECT value FROM display_settings WHERE name = 'reread_target_share';

-- name: GuardBlockedTrend :many
WITH r AS (
    SELECT day, sum(results)::bigint AS results
    FROM tool_result_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    GROUP BY day
), b AS (
    SELECT day, sum(errors)::bigint AS blocked
    FROM tool_error_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date AND error_class = 'blocked by hook'
        AND (sqlc.narg(project)::text IS NULL OR project = sqlc.narg(project)::text)
    GROUP BY day
)
SELECT coalesce(r.day, b.day)::date AS day, coalesce(r.results, 0)::bigint AS results,
    coalesce(b.blocked, 0)::bigint AS blocked
FROM r
FULL JOIN b ON b.day = r.day
ORDER BY 1;
