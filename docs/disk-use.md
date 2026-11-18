# Disk use per day

Measured 2026-11-09 on this Mac with the server installed as `com.benchwatch.server` and the default settings:
`detail_interval_seconds` and `top_apps_interval_seconds` 10, retention raw 14 days, 1-minute rollups 2 years,
1-hour rollups kept, events 2 years. 21 containers, 10 CPU cores and about 13 top apps per sample.

## Method

- Rows per day per table come from the rows written over 21 minutes of real collection (raw samples every 10 s,
  1-minute buckets), scaled to 86,400 s.
- Bytes per row are the mean row size (`pg_column_size`) times 1.58, the on-disk factor measured on
  `docker_container_samples`, the largest history table (heap, line pointers and primary-key index; partition and
  index page minimums removed). Every query ran in a read-only transaction.
- Transcript tables are rebuilt from the transcripts, so their size is the measured on-disk total over the 115 days
  of transcripts held.
- WAL, `VACUUM` slack and PostgreSQL's own catalogs are not counted.

## Result

| Tier | Written per day | Held at steady state |
|---|---|---|
| Raw samples | 65.2 MB | about 0.9 GB (14 days) |
| 1-minute rollups | 9.3 MB | about 6.8 GB after 2 years |
| 1-hour rollups | 0.15 MB | about 55 MB per year, no limit |
| Transcript tables | 52 KB | about 19 MB per year |
| Docker events | none seen in the 21 minutes | not measured |

Raw tables per day: `docker_container_samples` 38.6 MB, `machine_app_samples` 11.3 MB, `machine_core_samples`
6.0 MB, `machine_volume_samples` 3.5 MB, `machine_interface_samples` 2.5 MB, `machine_samples` 1.7 MB,
`docker_vm_samples` 1.6 MB. Container rows are 59% of raw and 76% of 1-minute rollup bytes, so the totals grow
with the number of containers.

The largest lever is the 1-minute rollup retention: one year instead of two halves its 6.8 GB. The raw tier stays
under 1 GB unless the detail interval drops below 10 s.
