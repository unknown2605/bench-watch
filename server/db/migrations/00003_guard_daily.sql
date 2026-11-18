-- +goose Up
CREATE TYPE guard_mismatch_kind AS ENUM ('node_error', 'go_error', 'time_cap', 'go_time_cap', 'decision', 'message');
CREATE TYPE guard_check_kind AS ENUM ('calendar_days', 'working_days', 'calls', 'counted_mismatches');
CREATE TYPE guard_engine AS ENUM ('node', 'go');

-- One row per day of the guard shadow report. Medians come from the report's day lines; maxima and decisions
-- stay NULL until the collector has per-call figures for that day.
CREATE TABLE guard_daily (
    day date PRIMARY KEY,
    calls bigint NOT NULL CHECK (calls >= 0),
    mismatches bigint NOT NULL CHECK (mismatches >= 0),
    time_caps bigint NOT NULL CHECK (time_caps >= 0),
    node_ms_median numeric(12, 3) CHECK (node_ms_median >= 0),
    go_ms_median numeric(12, 3) CHECK (go_ms_median >= 0),
    node_ms_max numeric(12, 3) CHECK (node_ms_max >= 0),
    go_ms_max numeric(12, 3) CHECK (go_ms_max >= 0),
    decisions_allow bigint CHECK (decisions_allow >= 0),
    decisions_ask bigint CHECK (decisions_ask >= 0),
    decisions_deny bigint CHECK (decisions_deny >= 0),
    decisions_hook_error bigint CHECK (decisions_hook_error >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- The latency buckets the collector counts into now; [lo_ms, hi_ms), NULL hi_ms is the open top bucket.
CREATE TABLE guard_latency_buckets (
    lo_ms numeric(12, 3) PRIMARY KEY CHECK (lo_ms >= 0),
    hi_ms numeric(12, 3) UNIQUE NULLS NOT DISTINCT CHECK (hi_ms IS NULL OR hi_ms > lo_ms)
);
INSERT INTO guard_latency_buckets (lo_ms, hi_ms) VALUES
    (0, 1), (1, 2), (2, 3), (3, 5), (5, 7), (7, 10), (10, 15), (15, 20), (20, 30), (30, 50), (50, 70), (70, 100),
    (100, 150), (150, 200), (200, 300), (300, 500), (500, 700), (700, 1000), (1000, 1500), (1500, 2000),
    (2000, 3000), (3000, 5000), (5000, 7000), (7000, 10000), (10000, 15000), (15000, 20000), (20000, 30000),
    (30000, NULL);

-- Calls per latency bucket and day, each row with its own bounds. Period percentiles are computed from the
-- summed buckets (GuardLatencyPercentiles), never from daily percentiles.
CREATE TABLE guard_latency_daily (
    day date NOT NULL,
    engine guard_engine NOT NULL,
    lo_ms numeric(12, 3) NOT NULL CHECK (lo_ms >= 0),
    hi_ms numeric(12, 3) CHECK (hi_ms IS NULL OR hi_ms > lo_ms),
    calls bigint NOT NULL CHECK (calls > 0),
    PRIMARY KEY (day, engine, lo_ms)
);

CREATE TABLE guard_mismatch_daily (
    day date NOT NULL,
    kind guard_mismatch_kind NOT NULL,
    counted boolean NOT NULL,
    mismatches bigint NOT NULL CHECK (mismatches >= 0),
    PRIMARY KEY (day, kind, counted)
);

-- The latest window check only; the report recomputes it from the whole shadow log each run.
CREATE TABLE guard_window_checks (
    kind guard_check_kind PRIMARY KEY,
    ok boolean NOT NULL,
    value bigint NOT NULL CHECK (value >= 0),
    needed bigint CHECK (needed >= 0),
    checked_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE guard_window_checks;
DROP TABLE guard_mismatch_daily;
DROP TABLE guard_latency_daily;
DROP TABLE guard_latency_buckets;
DROP TABLE guard_daily;
DROP TYPE guard_engine;
DROP TYPE guard_check_kind;
DROP TYPE guard_mismatch_kind;
