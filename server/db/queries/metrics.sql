-- Samples and Docker events are written by store.WriteSamples (samples.go), which makes a retried batch safe.

-- name: LatestMachineSample :one
SELECT * FROM machine_samples ORDER BY at DESC LIMIT 1;

-- name: MachineCoresAt :many
SELECT core, kind, busy_pct FROM machine_core_samples WHERE at = @at ORDER BY core;

-- name: MachineInterfacesAt :many
SELECT interface, rx_bps, tx_bps, merged_names FROM machine_interface_samples WHERE at = @at ORDER BY interface;

-- name: LatestMachineVolumes :many
SELECT volume, kind, filesystem, total_bytes, free_bytes, merged_names
FROM machine_volume_samples
WHERE at = (SELECT max(at) FROM machine_volume_samples WHERE at > @since::timestamptz)
ORDER BY volume;

-- name: LatestMachineApps :many
SELECT app, cpu_pct, mem_bytes
FROM machine_app_samples
WHERE at = (SELECT max(at) FROM machine_app_samples WHERE at > @since::timestamptz)
ORDER BY cpu_pct DESC, app
LIMIT @max_rows::integer;

-- name: MachineSeriesRaw :many
SELECT at, cpu_busy_pct, load_1, mem_used_bytes, swap_used_bytes, disk_read_bps, disk_write_bps
FROM machine_samples
WHERE at >= @since::timestamptz AND at < @until::timestamptz
ORDER BY at;

-- name: MachineSeries1m :many
SELECT bucket, cpu_busy_avg, cpu_busy_max, load_1_avg, mem_used_avg, swap_used_max, disk_read_avg, disk_write_avg
FROM machine_rollup_1m
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
ORDER BY bucket;

-- name: MachineSeries1h :many
SELECT bucket, cpu_busy_avg, cpu_busy_max, load_1_avg, mem_used_avg, swap_used_max, disk_read_avg, disk_write_avg
FROM machine_rollup_1h
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
ORDER BY bucket;

-- name: CoreSeriesRaw :many
SELECT at, core, kind, busy_pct
FROM machine_core_samples
WHERE at >= @since::timestamptz AND at < @until::timestamptz
ORDER BY core, at;

-- name: CoreSeries1m :many
SELECT bucket, core, kind, busy_avg FROM machine_core_rollup_1m
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
ORDER BY core, bucket;

-- name: CoreSeries1h :many
SELECT bucket, core, kind, busy_avg FROM machine_core_rollup_1h
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
ORDER BY core, bucket;

-- name: InterfaceSeriesRaw :many
SELECT at, interface, rx_bps, tx_bps
FROM machine_interface_samples
WHERE at >= @since::timestamptz AND at < @until::timestamptz
ORDER BY interface, at;

-- name: InterfaceSeries1m :many
SELECT bucket, interface, rx_avg, rx_max, tx_avg FROM machine_interface_rollup_1m
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
ORDER BY interface, bucket;

-- name: InterfaceSeries1h :many
SELECT bucket, interface, rx_avg, rx_max, tx_avg FROM machine_interface_rollup_1h
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
ORDER BY interface, bucket;

-- name: LatestDockerVmSample :one
SELECT * FROM docker_vm_samples ORDER BY at DESC LIMIT 1;

-- name: LatestDockerDisk :one
SELECT * FROM docker_vm_samples
WHERE disk_used_bytes IS NOT NULL AND at > @since::timestamptz
ORDER BY at DESC
LIMIT 1;

-- name: DockerContainersAt :many
SELECT container, stack, state, health, cpu_pct, mem_bytes, mem_limit_bytes, rx_bps, tx_bps, restarts, started_at,
    merged_names
FROM docker_container_samples
WHERE at = @at AND (sqlc.narg(stack)::text IS NULL OR stack = sqlc.narg(stack)::text)
ORDER BY mem_bytes DESC NULLS LAST, container;

-- name: DockerVmSeriesRaw :many
SELECT at, cpu_pct, mem_used_bytes FROM docker_vm_samples
WHERE at >= @since::timestamptz AND at < @until::timestamptz
ORDER BY at;

-- name: DockerVmSeries1m :many
SELECT bucket, cpu_avg, mem_used_avg FROM docker_vm_rollup_1m
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
ORDER BY bucket;

-- name: DockerVmSeries1h :many
SELECT bucket, cpu_avg, mem_used_avg FROM docker_vm_rollup_1h
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
ORDER BY bucket;

-- name: ContainerSeriesRaw :many
SELECT at, container, stack, cpu_pct, mem_bytes, rx_bps, tx_bps
FROM docker_container_samples
WHERE at >= @since::timestamptz AND at < @until::timestamptz
    AND (sqlc.narg(stack)::text IS NULL OR stack = sqlc.narg(stack)::text)
ORDER BY container, at;

-- name: ContainerSeries1m :many
SELECT bucket, container, stack, cpu_avg, mem_avg, rx_avg, tx_avg
FROM docker_container_rollup_1m
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
    AND (sqlc.narg(stack)::text IS NULL OR stack = sqlc.narg(stack)::text)
ORDER BY container, bucket;

-- name: ContainerSeries1h :many
SELECT bucket, container, stack, cpu_avg, mem_avg, rx_avg, tx_avg
FROM docker_container_rollup_1h
WHERE bucket >= @since::timestamptz AND bucket < @until::timestamptz
    AND (sqlc.narg(stack)::text IS NULL OR stack = sqlc.narg(stack)::text)
ORDER BY container, bucket;

-- name: DockerEvents :many
-- Events carry no stack; a stack filter keeps the containers seen in that stack during the range.
SELECT at, container, kind
FROM docker_events
WHERE at >= @since::timestamptz AND at < @until::timestamptz
    AND (sqlc.narg(stack)::text IS NULL OR container IN (
        SELECT container FROM docker_container_rollup_1h
        WHERE stack = sqlc.narg(stack)::text AND bucket >= date_trunc('hour', @since::timestamptz, 'UTC')
        UNION
        SELECT container FROM docker_container_rollup_1m
        WHERE stack = sqlc.narg(stack)::text AND bucket >= date_trunc('minute', @since::timestamptz, 'UTC')
        UNION
        SELECT container FROM docker_container_samples
        WHERE stack = sqlc.narg(stack)::text AND at >= @since::timestamptz))
ORDER BY at DESC, container, kind
LIMIT @max_rows::integer;

-- name: ListStacks :many
-- Each tier covers only what the coarser one has not rolled up yet, so long ranges never scan raw rows.
SELECT stack FROM docker_container_rollup_1h WHERE bucket >= @since::timestamptz
UNION
SELECT stack FROM docker_container_rollup_1m
WHERE bucket >= greatest(@since::timestamptz,
    coalesce((SELECT max(bucket) FROM docker_container_rollup_1h), '-infinity'::timestamptz))
UNION
SELECT stack FROM docker_container_samples
WHERE at >= greatest(@since::timestamptz,
    coalesce((SELECT max(bucket) FROM docker_container_rollup_1m), '-infinity'::timestamptz))
ORDER BY 1;
