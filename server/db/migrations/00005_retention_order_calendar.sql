-- +goose Up
-- drop_expired_partitions subtracts each keep from a timestamp, where a month is 28 to 31 days and a year 365 or 366,
-- so the order is checked the same way, from every UTC day of the next 100 years (14 ms), not with 30-day months.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION check_retention_order()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    raw_keep interval := coalesce((SELECT keep FROM retention_settings WHERE tier = 'raw'), 'infinity');
    minute_keep interval := coalesce((SELECT keep FROM retention_settings WHERE tier = 'rollup_1m'), 'infinity');
    hour_keep interval := coalesce((SELECT keep FROM retention_settings WHERE tier = 'rollup_1h'), 'infinity');
    first_day timestamp := date_trunc('day', now() AT TIME ZONE 'UTC');
BEGIN
    IF EXISTS (
        SELECT FROM generate_series(first_day, first_day + interval '100 years', interval '1 day') AS d (at)
        WHERE d.at - raw_keep < d.at - minute_keep OR d.at - minute_keep < d.at - hour_keep
    ) THEN
        RAISE EXCEPTION 'retention must keep raw <= rollup_1m <= rollup_1h' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- A timestamptz minus a keep counts days and months in the session time zone; pin the zone this check uses.
ALTER FUNCTION drop_expired_partitions(timestamptz) SET timezone = 'UTC';

-- +goose Down
ALTER FUNCTION drop_expired_partitions(timestamptz) RESET timezone;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION check_retention_order()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    raw_keep interval := coalesce((SELECT keep FROM retention_settings WHERE tier = 'raw'), 'infinity');
    minute_keep interval := coalesce((SELECT keep FROM retention_settings WHERE tier = 'rollup_1m'), 'infinity');
    hour_keep interval := coalesce((SELECT keep FROM retention_settings WHERE tier = 'rollup_1h'), 'infinity');
BEGIN
    IF NOT (raw_keep <= minute_keep AND minute_keep <= hour_keep) THEN
        RAISE EXCEPTION 'retention must keep raw <= rollup_1m <= rollup_1h' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd
