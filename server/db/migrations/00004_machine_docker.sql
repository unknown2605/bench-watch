-- +goose Up
CREATE TYPE core_kind AS ENUM ('performance', 'efficiency');
CREATE TYPE memory_pressure AS ENUM ('normal', 'warning', 'critical');
CREATE TYPE volume_kind AS ENUM ('internal', 'external', 'network', 'disk_image', 'other');
CREATE TYPE container_state AS ENUM ('created', 'running', 'paused', 'restarting', 'removing', 'exited', 'dead');
CREATE TYPE container_health AS ENUM ('healthy', 'unhealthy', 'starting', 'none');
-- The collector's closed set of container event kinds (CM-00004 docker/events.go kindOf); exec events are not kept.
CREATE TYPE docker_event_kind AS ENUM ('create', 'start', 'restart', 'stop', 'die', 'kill', 'oom', 'pause', 'unpause',
    'destroy', 'rename', 'update', 'health_healthy', 'health_unhealthy', 'health_starting', 'health', 'other');

CREATE FUNCTION is_interface_label(v text) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN v ~ '^[A-Za-z0-9][A-Za-z0-9 ._()+-]{0,31}$';

-- The collector reads its intervals here at start; neither the schema nor the rollups assume a value.
CREATE TABLE collector_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    detail_interval_seconds integer NOT NULL CHECK (detail_interval_seconds BETWEEN 1 AND 60),
    disk_interval_seconds integer NOT NULL CHECK (disk_interval_seconds BETWEEN 10 AND 3600),
    top_apps_interval_seconds integer NOT NULL CHECK (top_apps_interval_seconds BETWEEN 1 AND 3600),
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO collector_settings (detail_interval_seconds, disk_interval_seconds, top_apps_interval_seconds)
VALUES (10, 300, 10);

-- Raw samples at collector_settings.detail_interval_seconds, one partition per UTC day. A part the collector
-- could not read is NULL, never 0; the num_nulls checks keep each part whole.
CREATE TABLE machine_samples (
    at timestamptz NOT NULL,
    cpu_busy_pct real CHECK (cpu_busy_pct BETWEEN 0 AND 100),
    load_1 real CHECK (load_1 >= 0),
    load_5 real CHECK (load_5 >= 0),
    load_15 real CHECK (load_15 >= 0),
    mem_total_bytes bigint CHECK (mem_total_bytes >= 0),
    mem_used_bytes bigint CHECK (mem_used_bytes >= 0),
    mem_app_bytes bigint CHECK (mem_app_bytes >= 0),
    mem_cached_bytes bigint CHECK (mem_cached_bytes >= 0),
    mem_compressed_bytes bigint CHECK (mem_compressed_bytes >= 0),
    swap_used_bytes bigint CHECK (swap_used_bytes >= 0),
    swap_total_bytes bigint CHECK (swap_total_bytes >= 0),
    mem_pressure memory_pressure,
    disk_read_bps bigint CHECK (disk_read_bps >= 0),
    disk_write_bps bigint CHECK (disk_write_bps >= 0),
    PRIMARY KEY (at),
    CHECK (num_nulls(load_1, load_5, load_15) IN (0, 3)),
    CHECK (num_nulls(mem_total_bytes, mem_used_bytes, mem_app_bytes, mem_cached_bytes, mem_compressed_bytes,
        swap_used_bytes, swap_total_bytes) IN (0, 7)),
    CHECK (num_nulls(disk_read_bps, disk_write_bps) IN (0, 2))
) PARTITION BY RANGE (at);

CREATE TABLE machine_core_samples (
    at timestamptz NOT NULL,
    core smallint NOT NULL CHECK (core BETWEEN 0 AND 1023),
    kind core_kind NOT NULL,
    busy_pct real NOT NULL CHECK (busy_pct BETWEEN 0 AND 100),
    PRIMARY KEY (at, core)
) PARTITION BY RANGE (at);

CREATE TABLE machine_interface_samples (
    at timestamptz NOT NULL,
    interface text NOT NULL CHECK (is_interface_label(interface)),
    rx_bps bigint NOT NULL CHECK (rx_bps >= 0),
    tx_bps bigint NOT NULL CHECK (tx_bps >= 0),
    merged_names integer NOT NULL DEFAULT 0 CHECK (merged_names = 0 OR interface = 'other' AND merged_names > 0),
    PRIMARY KEY (at, interface)
) PARTITION BY RANGE (at);

CREATE TABLE machine_volume_samples (
    at timestamptz NOT NULL,
    volume text NOT NULL CHECK (is_display_label(volume)),
    kind volume_kind,
    filesystem text CHECK (is_filesystem_label(filesystem)),
    total_bytes bigint NOT NULL CHECK (total_bytes >= 0),
    free_bytes bigint NOT NULL CHECK (free_bytes >= 0),
    merged_names integer NOT NULL DEFAULT 0 CHECK (merged_names = 0 OR volume = 'other' AND merged_names > 0),
    PRIMARY KEY (at, volume)
) PARTITION BY RANGE (at);

CREATE TABLE machine_app_samples (
    at timestamptz NOT NULL,
    app text NOT NULL CHECK (is_display_label(app)),
    cpu_pct real NOT NULL CHECK (cpu_pct >= 0),
    mem_bytes bigint NOT NULL CHECK (mem_bytes >= 0),
    PRIMARY KEY (at, app)
) PARTITION BY RANGE (at);

-- Disk figures are NULL in samples where `docker system df` was not measured (every disk_interval_seconds).
CREATE TABLE docker_vm_samples (
    at timestamptz NOT NULL,
    mem_total_bytes bigint CHECK (mem_total_bytes >= 0),
    mem_used_bytes bigint CHECK (mem_used_bytes >= 0),
    cpu_pct real CHECK (cpu_pct >= 0),
    swap_used_bytes bigint CHECK (swap_used_bytes >= 0),
    cpus smallint CHECK (cpus >= 0),
    containers integer NOT NULL CHECK (containers >= 0),
    running integer NOT NULL CHECK (running >= 0),
    vm_disk_bytes bigint CHECK (vm_disk_bytes >= 0),
    disk_used_bytes bigint CHECK (disk_used_bytes >= 0),
    disk_reclaimable_bytes bigint CHECK (disk_reclaimable_bytes >= 0),
    images_bytes bigint CHECK (images_bytes >= 0),
    images_count integer CHECK (images_count >= 0),
    images_unused integer CHECK (images_unused >= 0),
    images_reclaimable_bytes bigint CHECK (images_reclaimable_bytes >= 0),
    images_largest_bytes bigint CHECK (images_largest_bytes >= 0),
    volumes_bytes bigint CHECK (volumes_bytes >= 0),
    volumes_count integer CHECK (volumes_count >= 0),
    volumes_unused integer CHECK (volumes_unused >= 0),
    volumes_reclaimable_bytes bigint CHECK (volumes_reclaimable_bytes >= 0),
    build_cache_bytes bigint CHECK (build_cache_bytes >= 0),
    build_cache_reclaimable_bytes bigint CHECK (build_cache_reclaimable_bytes >= 0),
    PRIMARY KEY (at)
) PARTITION BY RANGE (at);

-- stack is `none` outside Compose (StackLabel); failed stats leave mem NULL, underivable rates leave cpu and net NULL.
CREATE TABLE docker_container_samples (
    at timestamptz NOT NULL,
    container text NOT NULL CHECK (is_container_label(container)),
    stack text NOT NULL CHECK (is_stack_label(stack)),
    state container_state NOT NULL,
    health container_health NOT NULL,
    cpu_pct real CHECK (cpu_pct >= 0),
    mem_bytes bigint CHECK (mem_bytes >= 0),
    mem_limit_bytes bigint CHECK (mem_limit_bytes >= 0),
    rx_bps bigint CHECK (rx_bps >= 0),
    tx_bps bigint CHECK (tx_bps >= 0),
    restarts integer NOT NULL CHECK (restarts >= 0),
    started_at timestamptz,
    merged_names integer NOT NULL DEFAULT 0 CHECK (merged_names = 0 OR container = 'other' AND merged_names > 0),
    PRIMARY KEY (at, container),
    CHECK (num_nulls(cpu_pct, rx_bps, tx_bps) IN (0, 3))
) PARTITION BY RANGE (at);

-- Rollups: averages and maxima per minute and per hour, one partition per UTC month. Each <part>_samples counts
-- the samples that measured the part, so averages skip missing parts and hours weight minutes by it.
CREATE TABLE machine_rollup_1m (
    bucket timestamptz NOT NULL,
    samples integer NOT NULL CHECK (samples > 0),
    cpu_samples integer NOT NULL CHECK (cpu_samples BETWEEN 0 AND samples),
    load_samples integer NOT NULL CHECK (load_samples BETWEEN 0 AND samples),
    mem_samples integer NOT NULL CHECK (mem_samples BETWEEN 0 AND samples),
    disk_samples integer NOT NULL CHECK (disk_samples BETWEEN 0 AND samples),
    cpu_busy_avg real CHECK (cpu_busy_avg BETWEEN 0 AND 100),
    cpu_busy_max real CHECK (cpu_busy_max BETWEEN 0 AND 100),
    load_1_avg real CHECK (load_1_avg >= 0),
    load_5_avg real CHECK (load_5_avg >= 0),
    load_15_avg real CHECK (load_15_avg >= 0),
    mem_total_bytes bigint CHECK (mem_total_bytes >= 0),
    mem_used_avg bigint CHECK (mem_used_avg >= 0),
    mem_used_max bigint CHECK (mem_used_max >= 0),
    swap_used_max bigint CHECK (swap_used_max >= 0),
    disk_read_avg bigint CHECK (disk_read_avg >= 0),
    disk_read_max bigint CHECK (disk_read_max >= 0),
    disk_write_avg bigint CHECK (disk_write_avg >= 0),
    disk_write_max bigint CHECK (disk_write_max >= 0),
    PRIMARY KEY (bucket)
) PARTITION BY RANGE (bucket);

CREATE TABLE machine_rollup_1h (
    bucket timestamptz NOT NULL,
    samples integer NOT NULL CHECK (samples > 0),
    cpu_samples integer NOT NULL CHECK (cpu_samples BETWEEN 0 AND samples),
    load_samples integer NOT NULL CHECK (load_samples BETWEEN 0 AND samples),
    mem_samples integer NOT NULL CHECK (mem_samples BETWEEN 0 AND samples),
    disk_samples integer NOT NULL CHECK (disk_samples BETWEEN 0 AND samples),
    cpu_busy_avg real CHECK (cpu_busy_avg BETWEEN 0 AND 100),
    cpu_busy_max real CHECK (cpu_busy_max BETWEEN 0 AND 100),
    load_1_avg real CHECK (load_1_avg >= 0),
    load_5_avg real CHECK (load_5_avg >= 0),
    load_15_avg real CHECK (load_15_avg >= 0),
    mem_total_bytes bigint CHECK (mem_total_bytes >= 0),
    mem_used_avg bigint CHECK (mem_used_avg >= 0),
    mem_used_max bigint CHECK (mem_used_max >= 0),
    swap_used_max bigint CHECK (swap_used_max >= 0),
    disk_read_avg bigint CHECK (disk_read_avg >= 0),
    disk_read_max bigint CHECK (disk_read_max >= 0),
    disk_write_avg bigint CHECK (disk_write_avg >= 0),
    disk_write_max bigint CHECK (disk_write_max >= 0),
    PRIMARY KEY (bucket)
) PARTITION BY RANGE (bucket);

CREATE TABLE machine_core_rollup_1m (
    bucket timestamptz NOT NULL,
    core smallint NOT NULL CHECK (core BETWEEN 0 AND 1023),
    kind core_kind NOT NULL,
    samples integer NOT NULL CHECK (samples > 0),
    busy_avg real NOT NULL CHECK (busy_avg BETWEEN 0 AND 100),
    busy_max real NOT NULL CHECK (busy_max BETWEEN 0 AND 100),
    PRIMARY KEY (bucket, core)
) PARTITION BY RANGE (bucket);

CREATE TABLE machine_core_rollup_1h (
    bucket timestamptz NOT NULL,
    core smallint NOT NULL CHECK (core BETWEEN 0 AND 1023),
    kind core_kind NOT NULL,
    samples integer NOT NULL CHECK (samples > 0),
    busy_avg real NOT NULL CHECK (busy_avg BETWEEN 0 AND 100),
    busy_max real NOT NULL CHECK (busy_max BETWEEN 0 AND 100),
    PRIMARY KEY (bucket, core)
) PARTITION BY RANGE (bucket);

CREATE TABLE machine_interface_rollup_1m (
    bucket timestamptz NOT NULL,
    interface text NOT NULL CHECK (is_interface_label(interface)),
    samples integer NOT NULL CHECK (samples > 0),
    rx_avg bigint NOT NULL CHECK (rx_avg >= 0),
    rx_max bigint NOT NULL CHECK (rx_max >= 0),
    tx_avg bigint NOT NULL CHECK (tx_avg >= 0),
    tx_max bigint NOT NULL CHECK (tx_max >= 0),
    merged_names_max integer NOT NULL CHECK (merged_names_max >= 0),
    PRIMARY KEY (bucket, interface)
) PARTITION BY RANGE (bucket);

CREATE TABLE machine_interface_rollup_1h (
    bucket timestamptz NOT NULL,
    interface text NOT NULL CHECK (is_interface_label(interface)),
    samples integer NOT NULL CHECK (samples > 0),
    rx_avg bigint NOT NULL CHECK (rx_avg >= 0),
    rx_max bigint NOT NULL CHECK (rx_max >= 0),
    tx_avg bigint NOT NULL CHECK (tx_avg >= 0),
    tx_max bigint NOT NULL CHECK (tx_max >= 0),
    merged_names_max integer NOT NULL CHECK (merged_names_max >= 0),
    PRIMARY KEY (bucket, interface)
) PARTITION BY RANGE (bucket);

CREATE TABLE docker_vm_rollup_1m (
    bucket timestamptz NOT NULL,
    samples integer NOT NULL CHECK (samples > 0),
    cpu_samples integer NOT NULL CHECK (cpu_samples BETWEEN 0 AND samples),
    mem_samples integer NOT NULL CHECK (mem_samples BETWEEN 0 AND samples),
    cpu_avg real CHECK (cpu_avg >= 0),
    cpu_max real CHECK (cpu_max >= 0),
    mem_total_bytes bigint CHECK (mem_total_bytes >= 0),
    mem_used_avg bigint CHECK (mem_used_avg >= 0),
    mem_used_max bigint CHECK (mem_used_max >= 0),
    PRIMARY KEY (bucket)
) PARTITION BY RANGE (bucket);

CREATE TABLE docker_vm_rollup_1h (
    bucket timestamptz NOT NULL,
    samples integer NOT NULL CHECK (samples > 0),
    cpu_samples integer NOT NULL CHECK (cpu_samples BETWEEN 0 AND samples),
    mem_samples integer NOT NULL CHECK (mem_samples BETWEEN 0 AND samples),
    cpu_avg real CHECK (cpu_avg >= 0),
    cpu_max real CHECK (cpu_max >= 0),
    mem_total_bytes bigint CHECK (mem_total_bytes >= 0),
    mem_used_avg bigint CHECK (mem_used_avg >= 0),
    mem_used_max bigint CHECK (mem_used_max >= 0),
    PRIMARY KEY (bucket)
) PARTITION BY RANGE (bucket);

CREATE TABLE docker_container_rollup_1m (
    bucket timestamptz NOT NULL,
    container text NOT NULL CHECK (is_container_label(container)),
    stack text NOT NULL CHECK (is_stack_label(stack)),
    samples integer NOT NULL CHECK (samples > 0),
    rate_samples integer NOT NULL CHECK (rate_samples BETWEEN 0 AND samples),
    mem_samples integer NOT NULL CHECK (mem_samples BETWEEN 0 AND samples),
    cpu_avg real CHECK (cpu_avg >= 0),
    cpu_max real CHECK (cpu_max >= 0),
    mem_avg bigint CHECK (mem_avg >= 0),
    mem_max bigint CHECK (mem_max >= 0),
    rx_avg bigint CHECK (rx_avg >= 0),
    rx_max bigint CHECK (rx_max >= 0),
    tx_avg bigint CHECK (tx_avg >= 0),
    tx_max bigint CHECK (tx_max >= 0),
    restarts_max integer NOT NULL CHECK (restarts_max >= 0),
    merged_names_max integer NOT NULL CHECK (merged_names_max >= 0),
    PRIMARY KEY (bucket, container)
) PARTITION BY RANGE (bucket);

CREATE TABLE docker_container_rollup_1h (
    bucket timestamptz NOT NULL,
    container text NOT NULL CHECK (is_container_label(container)),
    stack text NOT NULL CHECK (is_stack_label(stack)),
    samples integer NOT NULL CHECK (samples > 0),
    rate_samples integer NOT NULL CHECK (rate_samples BETWEEN 0 AND samples),
    mem_samples integer NOT NULL CHECK (mem_samples BETWEEN 0 AND samples),
    cpu_avg real CHECK (cpu_avg >= 0),
    cpu_max real CHECK (cpu_max >= 0),
    mem_avg bigint CHECK (mem_avg >= 0),
    mem_max bigint CHECK (mem_max >= 0),
    rx_avg bigint CHECK (rx_avg >= 0),
    rx_max bigint CHECK (rx_max >= 0),
    tx_avg bigint CHECK (tx_avg >= 0),
    tx_max bigint CHECK (tx_max >= 0),
    restarts_max integer NOT NULL CHECK (restarts_max >= 0),
    merged_names_max integer NOT NULL CHECK (merged_names_max >= 0),
    PRIMARY KEY (bucket, container)
) PARTITION BY RANGE (bucket);

-- Keyed like the collector's event (time, name, kind), so a replayed event stream inserts nothing twice.
CREATE TABLE docker_events (
    at timestamptz NOT NULL,
    container text NOT NULL CHECK (is_container_label(container)),
    kind docker_event_kind NOT NULL,
    PRIMARY KEY (at, container, kind)
) PARTITION BY RANGE (at);

CREATE INDEX docker_events_container ON docker_events (container, at DESC);

-- Rollups widen the window to whole UTC buckets and recompute them, so a rerun or an unaligned window is safe;
-- a stored bucket is never replaced by one built from fewer samples.
-- +goose StatementBegin
CREATE FUNCTION rollup_minutes(since timestamptz, until timestamptz)
RETURNS bigint
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    lo timestamptz := date_trunc('minute', since, 'UTC');
    hi timestamptz := date_trunc('minute', until + interval '1 minute' - interval '1 microsecond', 'UTC');
    n bigint;
    total bigint := 0;
BEGIN
    IF lo IS NULL OR hi IS NULL OR hi <= lo THEN
        RETURN 0;
    END IF;

    INSERT INTO machine_rollup_1m AS t (
        bucket, samples, cpu_samples, load_samples, mem_samples, disk_samples, cpu_busy_avg, cpu_busy_max,
        load_1_avg, load_5_avg, load_15_avg, mem_total_bytes, mem_used_avg, mem_used_max, swap_used_max,
        disk_read_avg, disk_read_max, disk_write_avg, disk_write_max
    )
    SELECT date_trunc('minute', at, 'UTC'), count(*), count(cpu_busy_pct), count(load_1), count(mem_used_bytes),
        count(disk_read_bps), avg(cpu_busy_pct), max(cpu_busy_pct), avg(load_1), avg(load_5),
        avg(load_15), max(mem_total_bytes), avg(mem_used_bytes)::bigint, max(mem_used_bytes), max(swap_used_bytes),
        avg(disk_read_bps)::bigint, max(disk_read_bps), avg(disk_write_bps)::bigint, max(disk_write_bps)
    FROM machine_samples
    WHERE at >= lo AND at < hi
    GROUP BY 1
    ON CONFLICT (bucket) DO UPDATE SET
        samples = excluded.samples, cpu_samples = excluded.cpu_samples, load_samples = excluded.load_samples,
        mem_samples = excluded.mem_samples, disk_samples = excluded.disk_samples, cpu_busy_avg = excluded.cpu_busy_avg,
        cpu_busy_max = excluded.cpu_busy_max,
        load_1_avg = excluded.load_1_avg, load_5_avg = excluded.load_5_avg, load_15_avg = excluded.load_15_avg,
        mem_total_bytes = excluded.mem_total_bytes, mem_used_avg = excluded.mem_used_avg,
        mem_used_max = excluded.mem_used_max, swap_used_max = excluded.swap_used_max,
        disk_read_avg = excluded.disk_read_avg, disk_read_max = excluded.disk_read_max,
        disk_write_avg = excluded.disk_write_avg, disk_write_max = excluded.disk_write_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    INSERT INTO machine_core_rollup_1m AS t (bucket, core, kind, samples, busy_avg, busy_max)
    SELECT date_trunc('minute', at, 'UTC'), core, (array_agg(kind ORDER BY at DESC))[1], count(*), avg(busy_pct), max(busy_pct)
    FROM machine_core_samples
    WHERE at >= lo AND at < hi
    GROUP BY 1, core
    ON CONFLICT (bucket, core) DO UPDATE SET
        kind = excluded.kind, samples = excluded.samples, busy_avg = excluded.busy_avg, busy_max = excluded.busy_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    INSERT INTO machine_interface_rollup_1m AS t (
        bucket, interface, samples, rx_avg, rx_max, tx_avg, tx_max, merged_names_max
    )
    SELECT date_trunc('minute', at, 'UTC'), interface, count(*), avg(rx_bps)::bigint, max(rx_bps), avg(tx_bps)::bigint, max(tx_bps),
        max(merged_names)
    FROM machine_interface_samples
    WHERE at >= lo AND at < hi
    GROUP BY 1, interface
    ON CONFLICT (bucket, interface) DO UPDATE SET
        samples = excluded.samples, rx_avg = excluded.rx_avg, rx_max = excluded.rx_max,
        tx_avg = excluded.tx_avg, tx_max = excluded.tx_max, merged_names_max = excluded.merged_names_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    INSERT INTO docker_vm_rollup_1m AS t (
        bucket, samples, cpu_samples, mem_samples, cpu_avg, cpu_max, mem_total_bytes, mem_used_avg, mem_used_max
    )
    SELECT date_trunc('minute', at, 'UTC'), count(*), count(cpu_pct), count(mem_used_bytes), avg(cpu_pct), max(cpu_pct),
        max(mem_total_bytes),
        avg(mem_used_bytes)::bigint, max(mem_used_bytes)
    FROM docker_vm_samples
    WHERE at >= lo AND at < hi
    GROUP BY 1
    ON CONFLICT (bucket) DO UPDATE SET
        samples = excluded.samples, cpu_samples = excluded.cpu_samples, mem_samples = excluded.mem_samples,
        cpu_avg = excluded.cpu_avg, cpu_max = excluded.cpu_max,
        mem_total_bytes = excluded.mem_total_bytes, mem_used_avg = excluded.mem_used_avg, mem_used_max = excluded.mem_used_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    INSERT INTO docker_container_rollup_1m AS t (
        bucket, container, stack, samples, rate_samples, mem_samples, cpu_avg, cpu_max, mem_avg, mem_max, rx_avg, rx_max,
        tx_avg, tx_max, restarts_max, merged_names_max
    )
    SELECT date_trunc('minute', at, 'UTC'), container, (array_agg(stack ORDER BY at DESC))[1], count(*), count(cpu_pct),
        count(mem_bytes), avg(cpu_pct), max(cpu_pct), avg(mem_bytes)::bigint, max(mem_bytes), avg(rx_bps)::bigint,
        max(rx_bps), avg(tx_bps)::bigint, max(tx_bps), max(restarts), max(merged_names)
    FROM docker_container_samples
    WHERE at >= lo AND at < hi
    GROUP BY 1, container
    ON CONFLICT (bucket, container) DO UPDATE SET
        stack = excluded.stack, samples = excluded.samples, rate_samples = excluded.rate_samples,
        mem_samples = excluded.mem_samples, cpu_avg = excluded.cpu_avg, cpu_max = excluded.cpu_max,
        mem_avg = excluded.mem_avg, mem_max = excluded.mem_max, rx_avg = excluded.rx_avg, rx_max = excluded.rx_max,
        tx_avg = excluded.tx_avg, tx_max = excluded.tx_max, restarts_max = excluded.restarts_max,
        merged_names_max = excluded.merged_names_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    RETURN total;
END
$$;
-- +goose StatementEnd

-- The hourly rollups read the minute rollups, weighting each minute's average by the samples that measured it.
-- +goose StatementBegin
CREATE FUNCTION rollup_hours(since timestamptz, until timestamptz)
RETURNS bigint
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    lo timestamptz := date_trunc('hour', since, 'UTC');
    hi timestamptz := date_trunc('hour', until + interval '1 hour' - interval '1 microsecond', 'UTC');
    n bigint;
    total bigint := 0;
BEGIN
    IF lo IS NULL OR hi IS NULL OR hi <= lo THEN
        RETURN 0;
    END IF;

    INSERT INTO machine_rollup_1h AS t (
        bucket, samples, cpu_samples, load_samples, mem_samples, disk_samples, cpu_busy_avg, cpu_busy_max,
        load_1_avg, load_5_avg, load_15_avg, mem_total_bytes, mem_used_avg, mem_used_max, swap_used_max,
        disk_read_avg, disk_read_max, disk_write_avg, disk_write_max
    )
    SELECT date_trunc('hour', bucket, 'UTC'), sum(samples), sum(cpu_samples), sum(load_samples), sum(mem_samples),
        sum(disk_samples), sum(cpu_busy_avg * cpu_samples) / nullif(sum(cpu_samples), 0), max(cpu_busy_max),
        sum(load_1_avg * load_samples) / nullif(sum(load_samples), 0),
        sum(load_5_avg * load_samples) / nullif(sum(load_samples), 0),
        sum(load_15_avg * load_samples) / nullif(sum(load_samples), 0), max(mem_total_bytes),
        (sum(mem_used_avg * mem_samples) / nullif(sum(mem_samples), 0))::bigint, max(mem_used_max), max(swap_used_max),
        (sum(disk_read_avg * disk_samples) / nullif(sum(disk_samples), 0))::bigint, max(disk_read_max),
        (sum(disk_write_avg * disk_samples) / nullif(sum(disk_samples), 0))::bigint, max(disk_write_max)
    FROM machine_rollup_1m
    WHERE bucket >= lo AND bucket < hi
    GROUP BY 1
    ON CONFLICT (bucket) DO UPDATE SET
        samples = excluded.samples, cpu_samples = excluded.cpu_samples, load_samples = excluded.load_samples,
        mem_samples = excluded.mem_samples, disk_samples = excluded.disk_samples, cpu_busy_avg = excluded.cpu_busy_avg,
        cpu_busy_max = excluded.cpu_busy_max,
        load_1_avg = excluded.load_1_avg, load_5_avg = excluded.load_5_avg, load_15_avg = excluded.load_15_avg,
        mem_total_bytes = excluded.mem_total_bytes, mem_used_avg = excluded.mem_used_avg,
        mem_used_max = excluded.mem_used_max, swap_used_max = excluded.swap_used_max,
        disk_read_avg = excluded.disk_read_avg, disk_read_max = excluded.disk_read_max,
        disk_write_avg = excluded.disk_write_avg, disk_write_max = excluded.disk_write_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    INSERT INTO machine_core_rollup_1h AS t (bucket, core, kind, samples, busy_avg, busy_max)
    SELECT date_trunc('hour', bucket, 'UTC'), core, (array_agg(kind ORDER BY bucket DESC))[1], sum(samples),
        sum(busy_avg * samples) / sum(samples), max(busy_max)
    FROM machine_core_rollup_1m
    WHERE bucket >= lo AND bucket < hi
    GROUP BY 1, core
    ON CONFLICT (bucket, core) DO UPDATE SET
        kind = excluded.kind, samples = excluded.samples, busy_avg = excluded.busy_avg, busy_max = excluded.busy_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    INSERT INTO machine_interface_rollup_1h AS t (
        bucket, interface, samples, rx_avg, rx_max, tx_avg, tx_max, merged_names_max
    )
    SELECT date_trunc('hour', bucket, 'UTC'), interface, sum(samples), (sum(rx_avg * samples) / sum(samples))::bigint,
        max(rx_max), (sum(tx_avg * samples) / sum(samples))::bigint, max(tx_max), max(merged_names_max)
    FROM machine_interface_rollup_1m
    WHERE bucket >= lo AND bucket < hi
    GROUP BY 1, interface
    ON CONFLICT (bucket, interface) DO UPDATE SET
        samples = excluded.samples, rx_avg = excluded.rx_avg, rx_max = excluded.rx_max,
        tx_avg = excluded.tx_avg, tx_max = excluded.tx_max, merged_names_max = excluded.merged_names_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    INSERT INTO docker_vm_rollup_1h AS t (
        bucket, samples, cpu_samples, mem_samples, cpu_avg, cpu_max, mem_total_bytes, mem_used_avg, mem_used_max
    )
    SELECT date_trunc('hour', bucket, 'UTC'), sum(samples), sum(cpu_samples), sum(mem_samples),
        sum(cpu_avg * cpu_samples) / nullif(sum(cpu_samples), 0), max(cpu_max), max(mem_total_bytes),
        (sum(mem_used_avg * mem_samples) / nullif(sum(mem_samples), 0))::bigint, max(mem_used_max)
    FROM docker_vm_rollup_1m
    WHERE bucket >= lo AND bucket < hi
    GROUP BY 1
    ON CONFLICT (bucket) DO UPDATE SET
        samples = excluded.samples, cpu_samples = excluded.cpu_samples, mem_samples = excluded.mem_samples,
        cpu_avg = excluded.cpu_avg, cpu_max = excluded.cpu_max,
        mem_total_bytes = excluded.mem_total_bytes, mem_used_avg = excluded.mem_used_avg, mem_used_max = excluded.mem_used_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    INSERT INTO docker_container_rollup_1h AS t (
        bucket, container, stack, samples, rate_samples, mem_samples, cpu_avg, cpu_max, mem_avg, mem_max, rx_avg, rx_max,
        tx_avg, tx_max, restarts_max, merged_names_max
    )
    SELECT date_trunc('hour', bucket, 'UTC'), container, (array_agg(stack ORDER BY bucket DESC))[1], sum(samples),
        sum(rate_samples), sum(mem_samples), sum(cpu_avg * rate_samples) / nullif(sum(rate_samples), 0), max(cpu_max),
        (sum(mem_avg * mem_samples) / nullif(sum(mem_samples), 0))::bigint, max(mem_max),
        (sum(rx_avg * rate_samples) / nullif(sum(rate_samples), 0))::bigint, max(rx_max),
        (sum(tx_avg * rate_samples) / nullif(sum(rate_samples), 0))::bigint, max(tx_max), max(restarts_max),
        max(merged_names_max)
    FROM docker_container_rollup_1m
    WHERE bucket >= lo AND bucket < hi
    GROUP BY 1, container
    ON CONFLICT (bucket, container) DO UPDATE SET
        stack = excluded.stack, samples = excluded.samples, rate_samples = excluded.rate_samples,
        mem_samples = excluded.mem_samples, cpu_avg = excluded.cpu_avg, cpu_max = excluded.cpu_max,
        mem_avg = excluded.mem_avg, mem_max = excluded.mem_max, rx_avg = excluded.rx_avg, rx_max = excluded.rx_max,
        tx_avg = excluded.tx_avg, tx_max = excluded.tx_max, restarts_max = excluded.restarts_max,
        merged_names_max = excluded.merged_names_max
        WHERE excluded.samples >= t.samples;
    GET DIAGNOSTICS n = ROW_COUNT;
    total := total + n;

    RETURN total;
END
$$;
-- +goose StatementEnd

-- Raw days are rolled into minutes and hours, and minute months into hours, before their partitions are dropped,
-- so a long stop loses no unrolled tail. A time later than now() is treated as now().
-- +goose StatementBegin
CREATE FUNCTION drop_expired_partitions(at timestamptz DEFAULT now())
RETURNS SETOF text
LANGUAGE plpgsql
SET search_path = public, pg_temp
AS $$
DECLARE
    r record;
    suffix text;
    lo timestamp;
    hi timestamp;
    child text;
    expired text[] := '{}';
    raw_days timestamp[] := '{}';
    minute_months timestamp[] := '{}';
BEGIN
    at := least(coalesce(at, now()), now());
    PERFORM pg_advisory_xact_lock(hashtext('benchwatch.partitions'));
    FOR r IN
        SELECT c.relname::text AS child, pc.relname::text AS parent, pp.period, pp.tier, rs.keep
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_class pc ON pc.oid = i.inhparent
        JOIN partition_policy pp ON pp.parent = pc.relname
        JOIN retention_settings rs ON rs.tier = pp.tier
        WHERE pc.relnamespace = 'public'::regnamespace AND c.relnamespace = 'public'::regnamespace
            AND c.relkind = 'r' AND rs.keep IS NOT NULL
        ORDER BY c.relname
    LOOP
        suffix := substring(r.child FROM '^' || r.parent || '_p([0-9]{6}|[0-9]{8})$');
        CONTINUE WHEN suffix IS NULL OR length(suffix) <> CASE r.period WHEN 'day' THEN 8 ELSE 6 END;
        IF r.period = 'day' THEN
            lo := to_date(suffix, 'YYYYMMDD')::timestamp;
            hi := lo + interval '1 day';
        ELSE
            lo := to_date(suffix || '01', 'YYYYMMDD')::timestamp;
            hi := lo + interval '1 month';
        END IF;
        CONTINUE WHEN hi AT TIME ZONE 'UTC' > at - r.keep;
        expired := expired || r.child;
        IF r.tier = 'raw' AND NOT lo = ANY (raw_days) THEN
            raw_days := raw_days || lo;
        ELSIF r.tier = 'rollup_1m' AND NOT lo = ANY (minute_months) THEN
            minute_months := minute_months || lo;
        END IF;
    END LOOP;

    FOREACH lo IN ARRAY raw_days LOOP
        PERFORM ensure_partitions(lo AT TIME ZONE 'UTC', 0);
        PERFORM rollup_minutes(lo AT TIME ZONE 'UTC', (lo + interval '1 day') AT TIME ZONE 'UTC');
        PERFORM rollup_hours(lo AT TIME ZONE 'UTC', (lo + interval '1 day') AT TIME ZONE 'UTC');
    END LOOP;
    FOREACH lo IN ARRAY minute_months LOOP
        PERFORM rollup_hours(lo AT TIME ZONE 'UTC', (lo + interval '1 month') AT TIME ZONE 'UTC');
    END LOOP;
    FOREACH child IN ARRAY expired LOOP
        EXECUTE format('DROP TABLE public.%I', child);
        RETURN NEXT child;
    END LOOP;
END
$$;
-- +goose StatementEnd

INSERT INTO partition_policy (parent, period, tier) VALUES
    ('machine_samples', 'day', 'raw'),
    ('machine_core_samples', 'day', 'raw'),
    ('machine_interface_samples', 'day', 'raw'),
    ('machine_volume_samples', 'day', 'raw'),
    ('machine_app_samples', 'day', 'raw'),
    ('docker_vm_samples', 'day', 'raw'),
    ('docker_container_samples', 'day', 'raw'),
    ('machine_rollup_1m', 'month', 'rollup_1m'),
    ('machine_rollup_1h', 'month', 'rollup_1h'),
    ('machine_core_rollup_1m', 'month', 'rollup_1m'),
    ('machine_core_rollup_1h', 'month', 'rollup_1h'),
    ('machine_interface_rollup_1m', 'month', 'rollup_1m'),
    ('machine_interface_rollup_1h', 'month', 'rollup_1h'),
    ('docker_vm_rollup_1m', 'month', 'rollup_1m'),
    ('docker_vm_rollup_1h', 'month', 'rollup_1h'),
    ('docker_container_rollup_1m', 'month', 'rollup_1m'),
    ('docker_container_rollup_1h', 'month', 'rollup_1h'),
    ('docker_events', 'month', 'events');

SELECT ensure_partitions(now(), 1);

-- +goose Down
DELETE FROM partition_policy WHERE parent IN (
    'machine_samples',
    'machine_core_samples',
    'machine_interface_samples',
    'machine_volume_samples',
    'machine_app_samples',
    'docker_vm_samples',
    'docker_container_samples',
    'machine_rollup_1m',
    'machine_rollup_1h',
    'machine_core_rollup_1m',
    'machine_core_rollup_1h',
    'machine_interface_rollup_1m',
    'machine_interface_rollup_1h',
    'docker_vm_rollup_1m',
    'docker_vm_rollup_1h',
    'docker_container_rollup_1m',
    'docker_container_rollup_1h',
    'docker_events'
);
DROP FUNCTION drop_expired_partitions(timestamptz);
DROP FUNCTION rollup_hours(timestamptz, timestamptz);
DROP FUNCTION rollup_minutes(timestamptz, timestamptz);
DROP TABLE docker_events;
DROP TABLE docker_container_rollup_1h;
DROP TABLE docker_container_rollup_1m;
DROP TABLE docker_vm_rollup_1h;
DROP TABLE docker_vm_rollup_1m;
DROP TABLE machine_interface_rollup_1h;
DROP TABLE machine_interface_rollup_1m;
DROP TABLE machine_core_rollup_1h;
DROP TABLE machine_core_rollup_1m;
DROP TABLE machine_rollup_1h;
DROP TABLE machine_rollup_1m;
DROP TABLE docker_container_samples;
DROP TABLE docker_vm_samples;
DROP TABLE machine_app_samples;
DROP TABLE machine_volume_samples;
DROP TABLE machine_interface_samples;
DROP TABLE machine_core_samples;
DROP TABLE machine_samples;
DROP TABLE collector_settings;
DROP FUNCTION is_interface_label(text);
DROP TYPE docker_event_kind;
DROP TYPE container_health;
DROP TYPE container_state;
DROP TYPE volume_kind;
DROP TYPE memory_pressure;
DROP TYPE core_kind;
