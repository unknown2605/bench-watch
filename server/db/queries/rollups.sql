-- Both functions widen the window to whole UTC buckets (migration 00004).

-- name: RollupMinutes :one
SELECT rollup_minutes(@since::timestamptz, @until::timestamptz)::bigint AS rows;

-- name: RollupHours :one
SELECT rollup_hours(@since::timestamptz, @until::timestamptz)::bigint AS rows;

-- name: EnsurePartitions :one
SELECT ensure_partitions(@at::timestamptz, @ahead::integer)::integer AS created;

-- name: DropExpiredPartitions :many
SELECT dropped::text FROM drop_expired_partitions(@at::timestamptz) AS dropped;

-- name: ListPartitions :many
SELECT parent::text AS parent, child::text AS child, bounds::text AS bounds FROM list_partitions();

-- name: RetentionSettings :many
-- months and days are NULL for a tier kept forever.
SELECT tier,
    (extract(year FROM keep) * 12 + extract(month FROM keep))::integer AS months,
    extract(day FROM keep)::integer AS days
FROM retention_settings
ORDER BY tier;

-- name: SetRetention :exec
UPDATE retention_settings
SET keep = CASE WHEN @forever::boolean THEN NULL
        ELSE make_interval(months => @months::integer, days => @days::integer) END,
    updated_at = now()
WHERE tier = @tier;

-- name: CollectorSettings :one
SELECT detail_interval_seconds, disk_interval_seconds, top_apps_interval_seconds FROM collector_settings;

-- name: SetCollectorSettings :exec
UPDATE collector_settings
SET detail_interval_seconds = @detail_interval_seconds::integer,
    disk_interval_seconds = @disk_interval_seconds::integer,
    top_apps_interval_seconds = @top_apps_interval_seconds::integer, updated_at = now();
