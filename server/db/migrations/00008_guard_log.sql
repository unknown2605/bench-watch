-- +goose Up
CREATE TYPE guard_tool AS ENUM ('bash', 'file');
CREATE TYPE guard_decision AS ENUM ('allow', 'ask', 'deny');

CREATE FUNCTION is_guard_rule_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^[a-z0-9][a-z0-9._-]{0,47}$';

-- Calls from the per-call guard log (CM-00021) per local day and hour; every write adds the lines of one pass.
CREATE TABLE guard_call_hourly (
    day date NOT NULL,
    hour smallint NOT NULL CHECK (hour BETWEEN 0 AND 23),
    tool guard_tool NOT NULL,
    decision guard_decision NOT NULL,
    sub boolean NOT NULL,
    calls bigint NOT NULL CHECK (calls > 0),
    time_caps bigint NOT NULL CHECK (time_caps >= 0 AND time_caps <= calls),
    fail_closed bigint NOT NULL CHECK (fail_closed >= 0 AND fail_closed <= calls),
    sub_deny bigint NOT NULL CHECK (sub_deny >= 0 AND sub_deny <= calls),
    PRIMARY KEY (day, hour, tool, decision, sub)
);

-- Decision time per day and tool, counted into the guard_latency_buckets bounds; [lo_ms, hi_ms), NULL is open.
CREATE TABLE guard_call_latency_daily (
    day date NOT NULL,
    tool guard_tool NOT NULL,
    lo_ms numeric(12, 3) NOT NULL CHECK (lo_ms >= 0),
    hi_ms numeric(12, 3) CHECK (hi_ms IS NULL OR hi_ms > lo_ms),
    calls bigint NOT NULL CHECK (calls > 0),
    PRIMARY KEY (day, tool, lo_ms)
);

CREATE TABLE guard_rule_daily (
    day date NOT NULL,
    tool guard_tool NOT NULL,
    decision guard_decision NOT NULL CHECK (decision <> 'allow'),
    rule text NOT NULL CHECK (is_guard_rule_label(rule)),
    calls bigint NOT NULL CHECK (calls > 0),
    PRIMARY KEY (day, tool, decision, rule)
);

-- The reader's positions (device, inode, first-line hash, offset per file) and its skip counters. not_regular is
-- the number of files the latest pass refused; the other counters add up over all passes.
CREATE TABLE guard_log_state (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    files jsonb NOT NULL CHECK (jsonb_typeof(files) = 'array'),
    malformed bigint NOT NULL CHECK (malformed >= 0),
    oversized bigint NOT NULL CHECK (oversized >= 0),
    bad_version bigint NOT NULL CHECK (bad_version >= 0),
    bad_rule bigint NOT NULL CHECK (bad_rule >= 0),
    not_regular bigint NOT NULL CHECK (not_regular >= 0),
    truncated bigint NOT NULL CHECK (truncated >= 0),
    read_at timestamptz NOT NULL,
    last_deny_at timestamptz,
    last_deny_tool guard_tool,
    last_deny_rule text CHECK (last_deny_rule IS NULL OR is_guard_rule_label(last_deny_rule)),
    CHECK ((last_deny_at IS NULL) = (last_deny_tool IS NULL)),
    CHECK (last_deny_at IS NOT NULL OR last_deny_rule IS NULL)
);

-- +goose Down
DROP TABLE guard_log_state;
DROP TABLE guard_rule_daily;
DROP TABLE guard_call_latency_daily;
DROP TABLE guard_call_hourly;
DROP FUNCTION is_guard_rule_label(text);
DROP TYPE guard_decision;
DROP TYPE guard_tool;
