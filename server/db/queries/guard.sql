-- name: GuardLatencyBuckets :many
SELECT lo_ms, hi_ms FROM guard_latency_buckets ORDER BY lo_ms;

-- name: AddGuardCallHour :exec
INSERT INTO guard_call_hourly (day, hour, tool, decision, sub, calls, time_caps, fail_closed, sub_deny)
VALUES (@day, @hour, @tool, @decision, @sub, @calls, @time_caps, @fail_closed, @sub_deny)
ON CONFLICT (day, hour, tool, decision, sub) DO UPDATE SET
    calls = guard_call_hourly.calls + excluded.calls,
    time_caps = guard_call_hourly.time_caps + excluded.time_caps,
    fail_closed = guard_call_hourly.fail_closed + excluded.fail_closed,
    sub_deny = guard_call_hourly.sub_deny + excluded.sub_deny;

-- name: AddGuardCallLatency :exec
INSERT INTO guard_call_latency_daily (day, tool, lo_ms, hi_ms, calls) VALUES (@day, @tool, @lo_ms, @hi_ms, @calls)
ON CONFLICT (day, tool, lo_ms) DO UPDATE SET calls = guard_call_latency_daily.calls + excluded.calls;

-- name: AddGuardRule :exec
INSERT INTO guard_rule_daily (day, tool, decision, rule, calls) VALUES (@day, @tool, @decision, @rule, @calls)
ON CONFLICT (day, tool, decision, rule) DO UPDATE SET calls = guard_rule_daily.calls + excluded.calls;

-- name: GuardLogState :one
SELECT files, malformed, oversized, bad_version, bad_rule, not_regular, truncated, read_at, last_deny_at,
    last_deny_tool, last_deny_rule
FROM guard_log_state WHERE id;

-- name: GuardFiguresStored :one
SELECT EXISTS (SELECT 1 FROM guard_call_hourly)::boolean AS stored;

-- name: SaveGuardLogState :exec
-- Counters add the pass's skips; the last deny moves only to a later one, so a replayed pass cannot move it back.
INSERT INTO guard_log_state AS s (id, files, malformed, oversized, bad_version, bad_rule, not_regular, truncated,
    read_at, last_deny_at, last_deny_tool, last_deny_rule)
VALUES (true, @files, @malformed, @oversized, @bad_version, @bad_rule, @not_regular, @truncated, @read_at,
    sqlc.narg('last_deny_at')::timestamptz, sqlc.narg('last_deny_tool')::guard_tool, sqlc.narg('last_deny_rule')::text)
ON CONFLICT (id) DO UPDATE SET
    files = excluded.files,
    malformed = s.malformed + excluded.malformed,
    oversized = s.oversized + excluded.oversized,
    bad_version = s.bad_version + excluded.bad_version,
    bad_rule = s.bad_rule + excluded.bad_rule,
    not_regular = excluded.not_regular,
    truncated = s.truncated + excluded.truncated,
    read_at = excluded.read_at,
    last_deny_at = CASE WHEN excluded.last_deny_at > s.last_deny_at OR s.last_deny_at IS NULL
        AND excluded.last_deny_at IS NOT NULL THEN excluded.last_deny_at ELSE s.last_deny_at END,
    last_deny_tool = CASE WHEN excluded.last_deny_at > s.last_deny_at OR s.last_deny_at IS NULL
        AND excluded.last_deny_at IS NOT NULL THEN excluded.last_deny_tool ELSE s.last_deny_tool END,
    last_deny_rule = CASE WHEN excluded.last_deny_at > s.last_deny_at OR s.last_deny_at IS NULL
        AND excluded.last_deny_at IS NOT NULL THEN excluded.last_deny_rule ELSE s.last_deny_rule END;

-- name: GuardCallTotals :one
SELECT count(*)::bigint AS rows, coalesce(sum(calls), 0)::bigint AS calls,
    coalesce(sum(calls) FILTER (WHERE decision = 'allow'), 0)::bigint AS allow,
    coalesce(sum(calls) FILTER (WHERE decision = 'ask'), 0)::bigint AS ask,
    coalesce(sum(calls) FILTER (WHERE decision = 'deny'), 0)::bigint AS deny,
    coalesce(sum(fail_closed), 0)::bigint AS fail_closed, coalesce(sum(time_caps), 0)::bigint AS time_caps,
    coalesce(sum(sub_deny), 0)::bigint AS sub_deny, coalesce(sum(calls) FILTER (WHERE sub), 0)::bigint AS sub_calls
FROM guard_call_hourly
WHERE day BETWEEN @day_from::date AND @day_to::date;

-- name: GuardCallsByTool :many
SELECT tool, sum(calls)::bigint AS calls, coalesce(sum(calls) FILTER (WHERE decision = 'ask'), 0)::bigint AS ask,
    coalesce(sum(calls) FILTER (WHERE decision = 'deny'), 0)::bigint AS deny
FROM guard_call_hourly
WHERE day BETWEEN @day_from::date AND @day_to::date
GROUP BY tool ORDER BY tool;

-- name: GuardCallsByDay :many
SELECT day, sum(calls)::bigint AS calls, coalesce(sum(calls) FILTER (WHERE decision = 'ask'), 0)::bigint AS ask,
    coalesce(sum(calls) FILTER (WHERE decision = 'deny'), 0)::bigint AS deny
FROM guard_call_hourly
WHERE day BETWEEN @day_from::date AND @day_to::date
GROUP BY day ORDER BY day;

-- name: GuardCallHours :many
SELECT hour, sum(calls)::bigint AS calls, coalesce(sum(calls) FILTER (WHERE decision = 'deny'), 0)::bigint AS deny
FROM guard_call_hourly WHERE day = @day::date GROUP BY hour ORDER BY hour;

-- name: GuardTopRules :many
SELECT rule, tool, decision, sum(calls)::bigint AS calls
FROM guard_rule_daily
WHERE day BETWEEN @day_from::date AND @day_to::date
GROUP BY rule, tool, decision
ORDER BY 4 DESC, rule, tool, decision
LIMIT 10;

-- name: GuardCallLatencyPercentiles :many
-- Period percentiles per tool from the summed per-day histograms, interpolated linearly inside the bucket that holds
-- the quantile; the open top bucket reports its lower bound. The ROWS frame keeps buckets sharing a lo_ms apart.
WITH buckets AS (
    SELECT tool, lo_ms, hi_ms, sum(calls)::numeric AS calls
    FROM guard_call_latency_daily
    WHERE day BETWEEN @day_from::date AND @day_to::date
    GROUP BY tool, lo_ms, hi_ms
), cumulative AS (
    SELECT tool, lo_ms, hi_ms, calls,
        sum(calls) OVER (PARTITION BY tool ORDER BY lo_ms, hi_ms NULLS LAST ROWS UNBOUNDED PRECEDING) AS upto,
        sum(calls) OVER (PARTITION BY tool) AS total
    FROM buckets
)
SELECT c.tool, q.p::numeric AS quantile,
    (CASE WHEN c.hi_ms IS NULL THEN c.lo_ms
        ELSE c.lo_ms + (c.hi_ms - c.lo_ms) * (q.p * c.total - (c.upto - c.calls)) / c.calls END)::numeric AS ms
FROM cumulative c
CROSS JOIN (VALUES (0.5), (0.95), (0.99)) AS q (p)
WHERE c.upto - c.calls < q.p * c.total AND q.p * c.total <= c.upto
ORDER BY 1, 2;
