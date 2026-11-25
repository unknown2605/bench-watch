import { useMemo } from 'react';
import type { Schemas } from '../../api';
import { useMetricsFilters } from '../../app/filters';
import { useMetrics } from '../../app/queries';
import { DockerPiece } from '../../charts/pieces';
import type { DockerInput } from '../../charts/pieces/docker';
import { ClippedName } from './parts';
import { badEvents } from './dockerEvents';
import { healthOf, pieceHealth } from './dockerHealth';
import { KPI, ListRow, Panel } from '../../components';
import * as f from '../../format/format';
import { containerLabel, num, numOrNull, rows, stackLabel } from '../../format/labels';
import { fraction, share } from '../../format/scale';
import a11y from '../../styles/a11y.module.css';
import type_ from '../../styles/type.module.css';
import { MetricsFilter } from './MetricsFilter';
import { TabFrame, TabPiece, errorText } from './TabFrame';
import tabs from './tabs.module.css';
import styles from './views.module.css';
import own from './Docker.module.css';
import { keyed } from '../../app/keys';

type Data = Schemas['DockerResponse'];
type Container = Schemas['ContainerNow'];

const GIB = 1024;
const TABLE_ROWS = 5;
const NET_ROWS = 4;
const EVENT_ROWS = 6;
const EVENT_MIN_ROWS = 4;

const EVENT_TEXT: Record<Schemas['DockerEventKind'], string> = {
  create: 'created', start: 'started', restart: 'restarted', stop: 'stopped', die: 'exited', kill: 'killed',
  oom: 'out of memory',
  pause: 'paused', unpause: 'resumed', destroy: 'removed', rename: 'renamed', update: 'updated',
  health_healthy: 'healthy',
  health_unhealthy: 'unhealthy', health_starting: 'starting', health: 'health check', other: 'other',
};
const EVENT_LOUD = new Set<Schemas['DockerEventKind']>(['die', 'kill', 'oom', 'restart', 'health_unhealthy', 'destroy',
  'stop']);

function badNote(n: number): string {
  if (n === 0) return '';
  return n === 1 ? ' · 1 event needs a look' : ` · ${f.int(n)} events need a look`;
}

type DiskPart = { name: string; value: number; tone: 'gold' | 'session' | 'ink' | 'dim' };

/** "2 / 9 KB/s" when both rates share a unit, else each with its own. */
function ratePair(rx: number, tx: number): string {
  const a = f.rate(rx);
  const b = f.rate(tx);
  const unit = (t: string) => t.slice(t.indexOf(' ') + 1);
  return unit(a) === unit(b) ? `${a.slice(0, a.indexOf(' '))} / ${b}` : `${a} / ${b}`;
}

/** Running shows its age; any other state says itself, with the exit code when one is known. */
function uptimeText(c: Container, nowMs: number): string {
  if (c.state === 'running') {
    if (!c.startedAt || Number.isNaN(nowMs)) return '—';
    return f.duration(Math.max(0, (nowMs - Date.parse(c.startedAt)) / 1000));
  }
  if (healthOf(c) === 'finished') return 'finished';
  if (c.state !== 'exited') return c.state;
  return typeof c.exitCode === 'number' ? `exited ${c.exitCode}` : 'exited';
}

/** Containers with a point at each sample time, oldest first. */
export function containerTrend(series: Data['containerSeries']): number[] {
  const per = new Map<string, number>();
  for (const c of series) for (const p of c.points) per.set(p.at, (per.get(p.at) ?? 0) + 1);
  return [...per.entries()].sort((a, b) => Date.parse(a[0]) - Date.parse(b[0])).map(([, n]) => n);
}

export function dockerView(d: Data, nowMs: number) {
  const now = d.now;
  const containers = [...(now?.containers ?? [])];
  const counts = now?.counts ?? null;
  const vm = now?.vm ?? null;
  const disk = now?.disk ?? null;
  const byMemory = [...containers].sort((a, b) => num(b.memBytes) - num(a.memBytes));
  const byTraffic = [...containers].sort((a, b) => num(b.rxBps) + num(b.txBps) - num(a.rxBps) - num(a.txBps)).slice(0,
    NET_ROWS);
  const netMax = Math.max(0, ...byTraffic.map((c) => num(c.rxBps) + num(c.txBps)));
  const health = containers.map(healthOf);
  const warning = health.filter((h) => h === 'warn' || h === 'down').length;
  const finished = health.filter((h) => h === 'finished').length;
  const noCheck = containers.filter((c) => healthOf(c) === 'healthy' && c.health !== 'healthy').length;
  const known = disk ? num(disk.images.bytes) + num(disk.volumes.bytes) + num(disk.buildCache.bytes) : 0;
  const other = disk ? Math.max(0, num(disk.usedBytes) - known) : 0;
  const largest = numOrNull(disk?.images.largestBytes);
  const parts: DiskPart[] = disk
    ? [
        { name: 'images', value: num(disk.images.bytes), tone: 'gold' },
        { name: 'volumes', value: num(disk.volumes.bytes), tone: 'session' },
        { name: 'build cache', value: num(disk.buildCache.bytes), tone: 'ink' },
        { name: 'other', value: other, tone: 'dim' },
      ]
    : [];
  const partTotal = parts.reduce((a, p) => a + p.value, 0);
  const bad = badEvents(d.events, containers);
  const restarts = d.events.filter((e) => e.kind === 'restart').length;
  return {
    hasNow: now !== null && d.available,
    available: d.available,
    updated: now ? f.clockSeconds(now.at) : null,
    containers: {
      value: counts ? f.int(counts.containers) : '—',
      delta: counts
        ? `${f.int(counts.healthy)} healthy · ${f.int(warning || noCheck)} ${warning ? 'warning' : 'no check'}${
          finished ? ` · ${f.int(finished)} finished` : ''}`
        : '',
      trend: containerTrend(d.containerSeries),
    },
    networkTitle: now?.network ? `Network · ${now.network}` : 'Network · by container',
    vm: {
      value: vm?.memUsedBytes != null ? f.size(vm.memUsedBytes, GIB) : '—',
      delta: vm
        ? [vm.memTotalBytes != null ? `of ${f.size(vm.memTotalBytes, GIB)}` : null, vm.cpuPct != null
          ? `CPU ${f.pctPoints(vm.cpuPct)}` : null, vm.swapUsedBytes != null ? `swap ${f.size(vm.swapUsedBytes, GIB)}`
          : null].filter(Boolean).join(' · ')
        : '',
      trend: d.vmSeries.map((p) => num(p.memUsedBytes)),
    },
    diskKpi: { value: disk ? f.size(disk.usedBytes) : '—', delta: disk
      ? `${f.size(disk.reclaimableBytes)} could be freed` : '' },
    network: byTraffic.map((c) => ({
      name: f.containerShort(containerLabel(c.container)),
      meta: `↓ ${f.rate(num(c.rxBps))} · ↑ ${f.rate(num(c.txBps))}`,
      value: f.rate(num(c.rxBps) + num(c.txBps)),
      fill: fraction(num(c.rxBps) + num(c.txBps), netMax),
    })),
    piece: {
      containers: rows(now?.containers).map((c) => ({
        name: containerLabel(c.container),
        stack: stackLabel(c.stack),
        memBytes: num(c.memBytes),
        health: pieceHealth(c),
        restarts: num(c.restarts),
      })),
      stacks: rows(now?.stacks).map((s) => ({ stack: stackLabel(s.stack), memBytes: num(s.memBytes),
        rxBps: num(s.rxBps), txBps: num(s.txBps) })),
    } satisfies DockerInput,
    table: byMemory.slice(0, TABLE_ROWS).map((c) => ({
      key: containerLabel(c.container),
      name: f.containerShort(containerLabel(c.container)),
      full: containerLabel(c.container),
      stack: stackLabel(c.stack),
      cpu: c.cpuPct === null ? '—' : f.pctPoints(c.cpuPct),
      memory: c.memBytes === null ? '—' : f.size(c.memBytes, GIB),
      net: ratePair(num(c.rxBps), num(c.txBps)),
      health: healthOf(c),
      restarts: f.int(c.restarts),
      warn: c.restarts > 0 && healthOf(c) !== 'finished',
      uptime: uptimeText(c, nowMs),
    })),
    disk: disk
      ? {
          bar: parts.filter((p) => p.value > 0).map((p) => ({ ...p, share: share(p.value, partTotal) })),
          barLabel: `Docker disk by kind: ${parts
            .map((p) => `${p.name} ${f.pct(share(p.value, partTotal))}`)
            .join(', ')}`,
          other: other > 0 ? `Dim = other ${f.size(other)}` : null,
          total: f.size(disk.usedBytes),
          rows: [
            {
              name: 'images',
              tone: 'gold',
              value: f.size(disk.images.bytes),
              meta: `${f.int(disk.images.count)} images · ${f.size(disk.images.reclaimableBytes)} unused`,
            },
            {
              name: 'volumes',
              tone: 'session',
              value: f.size(disk.volumes.bytes),
              meta: `${f.int(disk.volumes.count)} volumes · ${f.int(disk.volumes.unused)} unused`,
            },
            {
              name: 'build cache',
              tone: 'ink',
              value: f.size(disk.buildCache.bytes),
              meta: `${f.size(disk.buildCache.reclaimableBytes)} can be freed`,
            },
          ] as const,
          freed: f.size(disk.reclaimableBytes),
          largest: `largest image · ${largest === null ? 'n/a' : f.size(largest)}`,
          stale: disk.stale === true ? `Stale · last read ${f.clock(disk.measuredAt)}` : null,
        }
      : null,
    events: d.events.slice(0, EVENT_ROWS).map((e) => ({
      key: `${e.at}-${e.container}-${e.kind}`,
      time: f.clock(e.at),
      name: f.containerShort(containerLabel(e.container)),
      kind: EVENT_TEXT[e.kind],
      bad: bad.has(e),
      loud: EVENT_LOUD.has(e.kind),
    })),
    eventFootWarn: bad.size > 0,
    eventFoot: `${f.int(d.events.length)} ${d.events.length === 1 ? 'event' : 'events'} · ${f.int(restarts)} ${
      restarts === 1 ? 'restart' : 'restarts'} in range${badNote(bad.size)}`,
    empty: now !== null && containers.length === 0,
  };
}

export function Docker() {
  const q = useMetrics('/api/docker', 'docker');
  const { range } = useMetricsFilters('docker');
  const v = useMemo(() => (q.data ? dockerView(q.data, Date.parse(q.data.now?.at ?? '')) : null), [q.data]);
  const paused = q.data?.historyPaused === true && (range === '24h' || range === '7d');
  const note = q.data && q.error ? `Update failed · ${errorText(q.error)}` : q.data && !q.data.available
    ? 'Docker not running' : paused ? 'History paused · showing the last hour' : null;
  const filter = <MetricsFilter tab="docker" scope="stack" updated={v?.updated ?? null} note={note} />;
  const slots = v && {
    left: (
      <>
        <KPI label="Containers" value={v.containers.value} delta={v.containers.delta} trend={v.containers.trend}
          tone="live" />
        <KPI label="Docker VM" value={v.vm.value} delta={v.vm.delta} trend={v.vm.trend} tone="default" />
        <KPI label="Docker disk" value={v.diskKpi.value} delta={v.diskKpi.delta} tone="gold" />
        <Panel index="04" title={v.networkTitle}>
          <div className={styles.rowsTight}>
            {v.network.map((n, i) => (
              <ListRow key={i} name={n.name} meta={n.meta} value={n.value} fill={n.fill} className={own.teal} />
            ))}
          </div>
          {v.network.length ? null : <p className={`${type_.mono} ${tabs.none}`}>No traffic yet</p>}
        </Panel>
      </>
    ),
    centre: (
      <TabPiece tab="docker" placeholder={q.isPlaceholderData}>
        <DockerPiece data={v.piece} />
      </TabPiece>
    ),
    bottom: (
      <Panel index="03" title="Containers · top 5 by memory" className={styles.bottomPanel}>
        <div className={styles.tableWrap}>
        <table className={`${type_.monoS} ${styles.table} ${styles.tableLeft} ${own.table}`}>
          <thead>
            <tr>
              {['Name', 'Stack', 'CPU', 'Memory', 'Net ↓ / ↑', 'Restarts', 'Uptime'].map((h) => (
                <th key={h} scope="col">{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {keyed(v.table, (r) => r.key).map(([key, r], i) => (
              <tr key={key} className={i === 0 ? own.top : undefined}>
                <td>
                  <span className={`${own.mark} ${own[r.health]}`} aria-hidden="true" />
                  <ClippedName name={r.name} full={r.full} />
                </td>
                <td className={own.stack}>{r.stack}</td>
                <td>{r.cpu}</td>
                <td className={own.memory}>{r.memory}</td>
                <td className={own.net}>{r.net}</td>
                <td className={r.warn ? styles.over : undefined}>{r.restarts}</td>
                <td>{r.uptime}</td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      </Panel>
    ),
    right: (
      <>
        <Panel index="02" title={v.disk ? `Disk used · ${v.disk.total}` : 'Disk used'} className={styles.rightTop}>
          {v.disk ? (
            <>
              <div className={own.diskBar} role="img" aria-label={v.disk.barLabel}>
                {v.disk.bar.map((p) => <span key={p.name} className={own[p.tone]} style={{ flexGrow: p.share }} />)}
              </div>
              {v.disk.stale ? <p className={`${type_.monoS} ${own.diskStale}`}>{v.disk.stale}</p> : null}
              {v.disk.other ? <p className={`${type_.monoS} ${own.diskOther}`}>{v.disk.other}</p> : null}
              <ul className={own.diskRows}>
                {v.disk.rows.map((r) => (
                  <li key={r.name} className={`${own.diskRow} ${own[r.tone]}`}>
                    <span className={`${type_.body} ${own.diskName}`}>{r.name}</span>
                    <span className={own.diskValue}>{r.value}</span>
                    <span className={`${type_.monoS} ${own.diskMeta}`}>{r.meta}</span>
                  </li>
                ))}
              </ul>
              <div className={own.freed}>
                <p className={`${type_.label} ${own.freedLabel}`}>Could be freed</p>
                <span className={own.freedValue}>{v.disk.freed}</span>
                <span className={`${type_.monoS} ${own.largest}`}>{v.disk.largest}</span>
              </div>
            </>
          ) : (
            <p className={`${type_.mono} ${tabs.none}`}>Disk use not measured yet</p>
          )}
        </Panel>
        <Panel index="05" title="Recent events" className={styles.rightLow}>
          <ul className={own.events}>
            {keyed(v.events, (e) => e.key).map(([key, e]) => (
              <li key={key} className={`${own.event} ${e.bad ? own.eventBad : ''}`}>
                <span className={own.eventMark} aria-hidden="true">{e.bad ? '×' : '·'}</span>
                <span className={`${type_.body} ${own.eventName}`}><ClippedName name={e.name} full={e.name} /></span>
                <span className={`${type_.monoS} ${e.loud ? styles.eventWarn : styles.eventKind}`}>
                  {e.kind} {e.time}
                </span>
                {e.bad ? <span className={a11y.hidden}>needs a look</span> : null}
              </li>
            ))}
            {Array.from({ length: Math.max(0, EVENT_MIN_ROWS - v.events.length) }, (_, i) => (
              <li key={`empty-${i}`} className={`${own.event} ${own.eventEmpty}`} aria-hidden="true">
                <span />
                <span />
                <span />
              </li>
            ))}
          </ul>
          {v.events.length ? null : <p className={`${type_.mono} ${tabs.none}`}>No events</p>}
          <p className={`${type_.label} ${v.eventFootWarn ? tabs.footWarn : tabs.foot}`}>{v.eventFoot}</p>
          <p className={`${type_.label} ${own.privacy}`}>Names and numbers only · no env or paths</p>
        </Panel>
      </>
    ),
  };
  return <TabFrame tab="docker" title="Docker" period={null} filter={filter} slots={slots} empty={v?.empty ?? false}
    emptyText="No containers running" error={q.error} fetching={q.isFetching} placeholder={q.isPlaceholderData} />;
}
