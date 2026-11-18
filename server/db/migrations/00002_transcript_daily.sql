-- +goose Up
CREATE TYPE token_scope AS ENUM ('main', 'sub');
CREATE TYPE compaction_trigger AS ENUM ('auto', 'manual');
CREATE TYPE error_class AS ENUM ('blocked by hook', 'denied', 'timeout', 'non-zero exit', 'not found', 'other');

-- Shaped after data.json `usage`: one row per local day, project, scope and model.
CREATE TABLE usage_daily (
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    scope token_scope NOT NULL,
    model text NOT NULL CHECK (is_model_label(model)),
    calls bigint NOT NULL CHECK (calls >= 0),
    input_tokens bigint NOT NULL CHECK (input_tokens >= 0),
    cache_write_tokens bigint NOT NULL CHECK (cache_write_tokens >= 0),
    cache_read_tokens bigint NOT NULL CHECK (cache_read_tokens >= 0),
    output_tokens bigint NOT NULL CHECK (output_tokens >= 0),
    context_tokens bigint NOT NULL CHECK (context_tokens >= 0),
    cost_input_usd numeric(18, 6) NOT NULL CHECK (cost_input_usd >= 0),
    cost_cache_write_usd numeric(18, 6) NOT NULL CHECK (cost_cache_write_usd >= 0),
    cost_cache_read_usd numeric(18, 6) NOT NULL CHECK (cost_cache_read_usd >= 0),
    cost_output_usd numeric(18, 6) NOT NULL CHECK (cost_output_usd >= 0),
    PRIMARY KEY (day, project, scope, model)
);

-- data.json `bandRows`, plus context tokens so the re-read share per band can be computed. A row holds the
-- bounds of the context band (token_bands) in force when it was written; max_tokens is exclusive, NULL is open.
CREATE TABLE context_band_daily (
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    scope token_scope NOT NULL,
    min_tokens bigint NOT NULL CHECK (min_tokens >= 0),
    max_tokens bigint CHECK (max_tokens IS NULL OR max_tokens > min_tokens),
    calls bigint NOT NULL CHECK (calls >= 0),
    context_tokens bigint NOT NULL CHECK (context_tokens >= 0),
    cache_read_tokens bigint NOT NULL CHECK (cache_read_tokens >= 0),
    PRIMARY KEY (day, project, scope, min_tokens)
);

CREATE TABLE compaction_daily (
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    scope token_scope NOT NULL,
    trigger compaction_trigger NOT NULL,
    compactions bigint NOT NULL CHECK (compactions >= 0),
    tokens_before bigint NOT NULL CHECK (tokens_before >= 0),
    PRIMARY KEY (day, project, scope, trigger)
);

-- One row per compact boundary, for "last compactions"; the key is a truncated hash of its record ID.
CREATE TABLE compaction_events (
    boundary text PRIMARY KEY CHECK (is_boundary_key(boundary)),
    at timestamptz NOT NULL,
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    session text NOT NULL CHECK (is_session_label(session)),
    scope token_scope NOT NULL,
    trigger compaction_trigger NOT NULL,
    tokens_before bigint NOT NULL CHECK (tokens_before >= 0),
    tokens_after bigint CHECK (tokens_after >= 0)
);
CREATE INDEX compaction_events_at ON compaction_events (at DESC);
CREATE INDEX compaction_events_session ON compaction_events (project, session, at DESC);

-- data.json `sessionRows`, plus the token mix per session; last_context_tokens is the context of the row's last call.
CREATE TABLE session_daily (
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    session text NOT NULL CHECK (is_session_label(session)),
    scope token_scope NOT NULL,
    model text NOT NULL CHECK (is_model_label(model)),
    calls bigint NOT NULL CHECK (calls >= 0),
    input_tokens bigint NOT NULL CHECK (input_tokens >= 0),
    cache_write_tokens bigint NOT NULL CHECK (cache_write_tokens >= 0),
    cache_read_tokens bigint NOT NULL CHECK (cache_read_tokens >= 0),
    output_tokens bigint NOT NULL CHECK (output_tokens >= 0),
    context_tokens bigint NOT NULL CHECK (context_tokens >= 0),
    peak_context_tokens bigint NOT NULL CHECK (peak_context_tokens >= 0),
    last_context_tokens bigint NOT NULL CHECK (last_context_tokens >= 0),
    cost_usd numeric(18, 6) NOT NULL CHECK (cost_usd >= 0),
    first_at timestamptz NOT NULL,
    last_at timestamptz NOT NULL CHECK (last_at >= first_at),
    PRIMARY KEY (day, project, session, scope, model)
);
CREATE INDEX session_daily_session ON session_daily (project, session, day);

-- data.json `agentRows`, split by session and model so the Sessions and Sub-agents tabs can drill down.
CREATE TABLE subagent_daily (
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    session text NOT NULL CHECK (is_session_label(session)),
    agent_type text NOT NULL CHECK (is_agent_type_label(agent_type)),
    model text NOT NULL CHECK (is_model_label(model)),
    calls bigint NOT NULL CHECK (calls >= 0),
    context_tokens bigint NOT NULL CHECK (context_tokens >= 0),
    cache_read_tokens bigint NOT NULL CHECK (cache_read_tokens >= 0),
    output_tokens bigint NOT NULL CHECK (output_tokens >= 0),
    cost_usd numeric(18, 6) NOT NULL CHECK (cost_usd >= 0),
    PRIMARY KEY (day, project, session, agent_type, model)
);

-- data.json `agentRuns`: context sums, so averages are first_context_tokens / runs.
CREATE TABLE subagent_run_daily (
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    session text NOT NULL CHECK (is_session_label(session)),
    agent_type text NOT NULL CHECK (is_agent_type_label(agent_type)),
    runs bigint NOT NULL CHECK (runs >= 0),
    first_context_tokens bigint NOT NULL CHECK (first_context_tokens >= 0),
    peak_context_tokens bigint NOT NULL CHECK (peak_context_tokens >= 0),
    calls bigint NOT NULL CHECK (calls >= 0),
    PRIMARY KEY (day, project, session, agent_type)
);

-- data.json `fills`, plus the largest result and repeats of the same target hash within a session.
-- weighted_cost_usd prices each re-sent token at the cache-read rate of the call that re-sent it.
CREATE TABLE tool_result_daily (
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    scope token_scope NOT NULL,
    tool text NOT NULL CHECK (is_tool_label(tool)),
    results bigint NOT NULL CHECK (results >= 0),
    tokens bigint NOT NULL CHECK (tokens >= 0),
    weighted_tokens bigint NOT NULL CHECK (weighted_tokens >= 0),
    weighted_cost_usd numeric(18, 6) NOT NULL CHECK (weighted_cost_usd >= 0),
    max_tokens bigint NOT NULL CHECK (max_tokens >= 0),
    repeat_results bigint NOT NULL CHECK (repeat_results >= 0),
    PRIMARY KEY (day, project, scope, tool)
);

-- Results per result-size band; like context_band_daily, each row holds the band's own bounds.
CREATE TABLE tool_result_size_daily (
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    scope token_scope NOT NULL,
    tool text NOT NULL CHECK (is_tool_label(tool)),
    min_tokens bigint NOT NULL CHECK (min_tokens >= 0),
    max_tokens bigint CHECK (max_tokens IS NULL OR max_tokens > min_tokens),
    results bigint NOT NULL CHECK (results >= 0),
    tokens bigint NOT NULL CHECK (tokens >= 0),
    PRIMARY KEY (day, project, scope, tool, min_tokens)
);

-- The scanner keeps only the largest results per day here; the key is a truncated hash of the tool-use ID.
CREATE TABLE tool_result_top (
    result text PRIMARY KEY CHECK (is_hash16(result)),
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    scope token_scope NOT NULL,
    tool text NOT NULL CHECK (is_tool_label(tool)),
    tokens bigint NOT NULL CHECK (tokens >= 0),
    resends integer NOT NULL CHECK (resends >= 0),
    weighted_tokens bigint NOT NULL CHECK (weighted_tokens >= 0)
);
CREATE INDEX tool_result_top_day ON tool_result_top (day, weighted_tokens DESC);

-- data.json `errors`.
CREATE TABLE tool_error_daily (
    day date NOT NULL,
    project text NOT NULL CHECK (is_project_label(project)),
    scope token_scope NOT NULL,
    tool text NOT NULL CHECK (is_tool_label(tool)),
    error_class error_class NOT NULL,
    errors bigint NOT NULL CHECK (errors >= 0),
    PRIMARY KEY (day, project, scope, tool, error_class)
);

-- Transcript figures are kept forever (user decision 2026-11-08): only the days the scanner rebuilt are cleared,
-- and a day whose transcripts Claude Code deleted keeps its rows. The scanner then copies the rebuilt days.
-- +goose StatementBegin
CREATE FUNCTION clear_transcript_days(rebuilt date[])
RETURNS bigint
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    t text;
    n bigint;
    total bigint := 0;
BEGIN
    IF rebuilt IS NULL OR array_position(rebuilt, NULL) IS NOT NULL THEN
        RAISE EXCEPTION 'rebuilt must be an array without NULL';
    END IF;
    FOREACH t IN ARRAY ARRAY['usage_daily', 'context_band_daily', 'compaction_daily', 'compaction_events',
        'session_daily', 'subagent_daily', 'subagent_run_daily', 'tool_result_daily', 'tool_result_size_daily',
        'tool_result_top', 'tool_error_daily'] LOOP
        EXECUTE format('DELETE FROM public.%I WHERE day = ANY($1)', t) USING rebuilt;
        GET DIAGNOSTICS n = ROW_COUNT;
        total := total + n;
    END LOOP;
    RETURN total;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION clear_transcript_days(date[]);
DROP TABLE tool_error_daily;
DROP TABLE tool_result_top;
DROP TABLE tool_result_size_daily;
DROP TABLE tool_result_daily;
DROP TABLE subagent_run_daily;
DROP TABLE subagent_daily;
DROP TABLE session_daily;
DROP TABLE compaction_events;
DROP TABLE compaction_daily;
DROP TABLE context_band_daily;
DROP TABLE usage_daily;
DROP TYPE error_class;
DROP TYPE compaction_trigger;
DROP TYPE token_scope;
