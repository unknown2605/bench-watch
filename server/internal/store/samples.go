package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type SampleBatch struct {
	Machine    []MachineSample
	Cores      []MachineCoreSample
	Interfaces []MachineInterfaceSample
	Volumes    []MachineVolumeSample
	Apps       []MachineAppSample
	DockerVM   []DockerVmSample
	Containers []DockerContainerSample
	Events     []DockerEvent
}

type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type sampleTable struct {
	name string
	// Staging column types; enums are staged as text so COPY needs no registered enum codec.
	cols  []string
	types []string
	casts map[string]string
	key   []string
}

var sampleTables = []sampleTable{
	{
		name: "machine_samples",
		cols: []string{"at", "cpu_busy_pct", "load_1", "load_5", "load_15", "mem_total_bytes", "mem_used_bytes",
			"mem_app_bytes", "mem_cached_bytes", "mem_compressed_bytes", "swap_used_bytes", "swap_total_bytes",
			"mem_pressure", "disk_read_bps", "disk_write_bps"},
		types: []string{"timestamptz", "real", "real", "real", "real", "bigint", "bigint", "bigint", "bigint", "bigint",
			"bigint", "bigint", "text", "bigint", "bigint"},
		casts: map[string]string{"mem_pressure": "public.memory_pressure"},
		key:   []string{"at"},
	},
	{
		name:  "machine_core_samples",
		cols:  []string{"at", "core", "kind", "busy_pct"},
		types: []string{"timestamptz", "smallint", "text", "real"},
		casts: map[string]string{"kind": "public.core_kind"},
		key:   []string{"at", "core"},
	},
	{
		name:  "machine_interface_samples",
		cols:  []string{"at", "interface", "rx_bps", "tx_bps", "merged_names"},
		types: []string{"timestamptz", "text", "bigint", "bigint", "integer"},
		key:   []string{"at", "interface"},
	},
	{
		name:  "machine_volume_samples",
		cols:  []string{"at", "volume", "kind", "filesystem", "total_bytes", "free_bytes", "merged_names"},
		types: []string{"timestamptz", "text", "text", "text", "bigint", "bigint", "integer"},
		casts: map[string]string{"kind": "public.volume_kind"},
		key:   []string{"at", "volume"},
	},
	{
		name:  "machine_app_samples",
		cols:  []string{"at", "app", "cpu_pct", "mem_bytes"},
		types: []string{"timestamptz", "text", "real", "bigint"},
		key:   []string{"at", "app"},
	},
	{
		name: "docker_vm_samples",
		cols: []string{"at", "mem_total_bytes", "mem_used_bytes", "cpu_pct", "swap_used_bytes", "cpus", "containers",
			"running", "vm_disk_bytes", "disk_used_bytes", "disk_reclaimable_bytes", "images_bytes", "images_count",
			"images_unused", "images_reclaimable_bytes", "images_largest_bytes", "volumes_bytes", "volumes_count",
			"volumes_unused", "volumes_reclaimable_bytes", "build_cache_bytes", "build_cache_reclaimable_bytes"},
		types: []string{"timestamptz", "bigint", "bigint", "real", "bigint", "smallint", "integer", "integer", "bigint",
			"bigint", "bigint", "bigint", "integer", "integer", "bigint", "bigint", "bigint", "integer", "integer",
			"bigint", "bigint", "bigint"},
		key: []string{"at"},
	},
	{
		name: "docker_container_samples",
		cols: []string{"at", "container", "stack", "state", "health", "cpu_pct", "mem_bytes", "mem_limit_bytes",
			"rx_bps", "tx_bps", "restarts", "started_at", "merged_names"},
		types: []string{"timestamptz", "text", "text", "text", "text", "real", "bigint", "bigint", "bigint", "bigint",
			"integer", "timestamptz", "integer"},
		casts: map[string]string{"state": "public.container_state", "health": "public.container_health"},
		key:   []string{"at", "container"},
	},
	{
		name:  "docker_events",
		cols:  []string{"at", "container", "kind"},
		types: []string{"timestamptz", "text", "text"},
		casts: map[string]string{"kind": "public.docker_event_kind"},
		key:   []string{"at", "container", "kind"},
	},
}

func (t sampleTable) stageName() string { return "stage_" + t.name }

func (t sampleTable) createStage() string {
	defs := make([]string, 0, len(t.cols)+1)
	defs = append(defs, "ord integer NOT NULL")
	for i, c := range t.cols {
		defs = append(defs, c+" "+t.types[i])
	}
	return fmt.Sprintf("CREATE TEMP TABLE IF NOT EXISTS %s (%s) ON COMMIT DELETE ROWS",
		t.stageName(), strings.Join(defs, ", "))
}

// The first row of a key wins inside a batch; rows already stored are kept, so a retried batch inserts nothing twice.
func (t sampleTable) insertFromStage() string {
	sel := make([]string, len(t.cols))
	for i, c := range t.cols {
		if cast, ok := t.casts[c]; ok {
			sel[i] = c + "::" + cast
		} else {
			sel[i] = c
		}
	}
	key := strings.Join(t.key, ", ")
	return fmt.Sprintf("INSERT INTO public.%s (%s) SELECT %s FROM (SELECT DISTINCT ON (%s) * FROM pg_temp.%s "+
		"ORDER BY %s, ord) s ON CONFLICT (%s) DO NOTHING",
		t.name, strings.Join(t.cols, ", "), strings.Join(sel, ", "), key, t.stageName(), key, key)
}

func enumText[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

func (b SampleBatch) rows() [][][]any {
	out := make([][][]any, len(sampleTables))
	for i, r := range b.Machine {
		out[0] = append(out[0], []any{i, r.At, r.CpuBusyPct, r.Load1, r.Load5, r.Load15, r.MemTotalBytes,
			r.MemUsedBytes, r.MemAppBytes, r.MemCachedBytes, r.MemCompressedBytes, r.SwapUsedBytes, r.SwapTotalBytes,
			enumText(r.MemPressure), r.DiskReadBps, r.DiskWriteBps})
	}
	for i, r := range b.Cores {
		out[1] = append(out[1], []any{i, r.At, r.Core, string(r.Kind), r.BusyPct})
	}
	for i, r := range b.Interfaces {
		out[2] = append(out[2], []any{i, r.At, r.Interface, r.RxBps, r.TxBps, r.MergedNames})
	}
	for i, r := range b.Volumes {
		out[3] = append(out[3], []any{i, r.At, r.Volume, enumText(r.Kind), r.Filesystem, r.TotalBytes, r.FreeBytes,
			r.MergedNames})
	}
	for i, r := range b.Apps {
		out[4] = append(out[4], []any{i, r.At, r.App, r.CpuPct, r.MemBytes})
	}
	for i, r := range b.DockerVM {
		out[5] = append(out[5], []any{i, r.At, r.MemTotalBytes, r.MemUsedBytes, r.CpuPct, r.SwapUsedBytes, r.Cpus,
			r.Containers, r.Running, r.VmDiskBytes, r.DiskUsedBytes, r.DiskReclaimableBytes, r.ImagesBytes,
			r.ImagesCount, r.ImagesUnused, r.ImagesReclaimableBytes, r.ImagesLargestBytes, r.VolumesBytes,
			r.VolumesCount, r.VolumesUnused, r.VolumesReclaimableBytes, r.BuildCacheBytes,
			r.BuildCacheReclaimableBytes})
	}
	for i, r := range b.Containers {
		out[6] = append(out[6], []any{i, r.At, r.Container, r.Stack, string(r.State), string(r.Health), r.CpuPct,
			r.MemBytes, r.MemLimitBytes, r.RxBps, r.TxBps, r.Restarts, r.StartedAt, r.MergedNames})
	}
	for i, r := range b.Events {
		out[7] = append(out[7], []any{i, r.At, r.Container, string(r.Kind)})
	}
	return out
}

func (b SampleBatch) partitionDays() []time.Time {
	seen := map[time.Time]bool{}
	var days []time.Time
	add := func(ts pgtype.Timestamptz) {
		if !ts.Valid {
			return
		}
		d := ts.Time.UTC().Truncate(24 * time.Hour)
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	for _, r := range b.Machine {
		add(r.At)
	}
	for _, r := range b.Cores {
		add(r.At)
	}
	for _, r := range b.Interfaces {
		add(r.At)
	}
	for _, r := range b.Volumes {
		add(r.At)
	}
	for _, r := range b.Apps {
		add(r.At)
	}
	for _, r := range b.DockerVM {
		add(r.At)
	}
	for _, r := range b.Containers {
		add(r.At)
	}
	for _, r := range b.Events {
		add(r.At)
	}
	return days
}

// WriteSamples stores a batch all or nothing and returns the rows inserted. A retried or partly stored batch
// inserts only what is missing, so the caller may retry any error for which IsPermanent is false.
func WriteSamples(ctx context.Context, db TxBeginner, b SampleBatch) (inserted int64, err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	for _, d := range b.partitionDays() {
		if _, err = tx.Exec(ctx, "SELECT public.ensure_partitions($1, 0)", d); err != nil {
			return 0, err
		}
	}
	for i, rows := range b.rows() {
		if len(rows) == 0 {
			continue
		}
		t := sampleTables[i]
		if _, err = tx.Exec(ctx, t.createStage()); err != nil {
			return 0, err
		}
		cols := append([]string{"ord"}, t.cols...)
		if _, err = tx.CopyFrom(ctx, pgx.Identifier{"pg_temp", t.stageName()}, cols, pgx.CopyFromRows(rows)); err != nil {
			return 0, err
		}
		tag, execErr := tx.Exec(ctx, t.insertFromStage())
		if execErr != nil {
			err = execErr
			return 0, err
		}
		inserted += tag.RowsAffected()
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return inserted, nil
}

// IsPermanent reports whether retrying the same batch can never succeed: SQLSTATE class 22 (data exception) or
// 23 (integrity constraint). Connection, timeout, serialization and every other error count as transient.
func IsPermanent(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || len(pgErr.Code) != 5 {
		return false
	}
	class := pgErr.Code[:2]
	return class == "22" || class == "23"
}
