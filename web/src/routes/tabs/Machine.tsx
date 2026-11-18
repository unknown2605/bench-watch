import { useMemo, useState } from 'react';
import type { Schemas } from '../../api';
import { useMetrics } from '../../app/queries';
import { useMetricsFilters } from '../../app/filters';
import { FlowLines } from '../../charts/FlowLines';
import { PairBars } from '../../charts/PairBars';
import { KPI, ListRow, Meter, Panel } from '../../components';
import * as f from '../../format/format';
import { appLabel, filesystemLabel, interfaceLabel, num, numOrNull, rows, volumeLabel } from '../../format/labels';
import { bucketMean, fraction, share, timeBuckets } from '../../format/scale';
import { LIVE_WINDOW_MS } from '../../app/liveCache';
import { medianGap } from '../../charts/useTimeScroll';
import type { PairTimes } from '../../charts/PairBars';
import { Fit } from '../../components/Fit';
import type_ from '../../styles/type.module.css';
import { MachinePiece } from '../../charts/pieces';
import type { MachineInput } from '../../charts/pieces/machine';
import { errorText, TabFrame, TabPiece } from './TabFrame';
import { MetricsFilter } from './MetricsFilter';
import tabs from './tabs.module.css';
import styles from './views.module.css';
import own from './Machine.module.css';
import { keyed } from '../../app/keys';
import { NetRows } from './NetRows';
import { defaultInterface, KIND_LABEL, netKind, selectedInterface, visibleInterfaces } from './netInterfaces';

type Data = Schemas['MachineResponse'];

const GIB = 1024;
const IO_BUCKETS = 34;
const IO_LIVE_BUCKET_MS = 10_000;
const APP_ROWS = 4;
const VOLUME_ROWS = 2;
const RANGE_WORDS: Record<Schemas['MetricsRange'], string> = { live: 'live', '5m': '5 min', '15m': '15 min',
  '1h': 'last hour', '24h': 'last 24 h', '7d': 'last 7 days' };
const AGO: Record<Schemas['MetricsRange'], string> = { live: '5 MIN AGO', '5m': '5 MIN AGO', '15m': '15 MIN AGO',
  '1h': '60 MIN AGO', '24h': '24 H AGO', '7d': '7 D AGO' };

export function machineView(d: Data, selected: string | null = null) {
  const now = d.now;
  const series = d.series;
  const cpu = numOrNull(now?.cpuBusyPct ?? null);
  const load = now?.load ?? null;
  const mem = now?.memory ?? null;
  const all = now?.volumes ?? [];
  const boot = all.find((v) => v.kind === 'internal') ?? all.find((v) => v.volume === 'root') ?? null;
  const volumes = [...all].sort((a, b) => Number(b === boot) - Number(a === boot)
    || num(b.totalBytes) - num(a.totalBytes));
  const main = boot ?? volumes[0] ?? null;
  const listed = visibleInterfaces((now?.interfaces ?? [])
    .map((i) => ({ ...i, rxBps: num(i.rxBps), txBps: num(i.txBps) })));
  const chosen = selectedInterface(selected, listed.map((i) => i.interface),
    defaultInterface(listed, d.interfaceSeries));
  const current = listed.find((i) => i.interface === chosen) ?? null;
  const flow = d.interfaceSeries.find((s) => s.interface === chosen) ?? null;
  const times = (flow?.points ?? []).map((p) => Date.parse(p.at));
  const down = (flow?.points ?? []).map((p) => numOrNull(p.rxBps));
  const up = (flow?.points ?? []).map((p) => numOrNull(p.txBps));
  const apps = [...(now?.apps ?? [])].sort((a, b) => num(b.cpuPct) - num(a.cpuPct)
    || num(b.memBytes) - num(a.memBytes)).slice(0, APP_ROWS);
  const appMax = Math.max(0, ...apps.map((a) => num(a.cpuPct)));
  const totalMem = num(mem?.totalBytes);
  const memRows = mem
    ? [
        { name: 'apps', meta: 'memory in use by apps', bytes: num(mem.appBytes), tone: 'gold' },
        { name: 'cached files', meta: 'freed when apps need it', bytes: num(mem.cachedBytes), tone: 'session' },
        { name: 'compressed', meta: 'squeezed to save space', bytes: num(mem.compressedBytes), tone: 'session' },
        {
          name: 'swap used',
          meta: `written to disk · ${f.size(num(mem.swapUsedBytes), GIB)} of ${f.size(num(mem.swapTotalBytes), GIB)}`,
          bytes: num(mem.swapUsedBytes),
          tone: 'gold',
        },
      ]
    : [];
  const memMax = Math.max(0, ...memRows.map((r) => r.bytes));
  const io = now?.diskIo ?? null;
  const live = d.range === 'live';
  const at = series.map((p) => Date.parse(p.at));
  const latest = at[at.length - 1] ?? 0;
  const ioLive = (pick: (p: (typeof series)[number]) => number | null) =>
    timeBuckets(at, series.map(pick), IO_LIVE_BUCKET_MS, latest - LIVE_WINDOW_MS);
  const readLive = live ? ioLive((p) => numOrNull(p.diskReadBps)) : null;
  const writeLive = live ? ioLive((p) => numOrNull(p.diskWriteBps)) : null;
  const read = readLive ? readLive.means : bucketMean(series.map((p) => numOrNull(p.diskReadBps)), IO_BUCKETS);
  const write = writeLive ? writeLive.means : bucketMean(series.map((p) => numOrNull(p.diskWriteBps)), IO_BUCKETS);
  const ioTimes: PairTimes | undefined = readLive && at.length
    ? { starts: readLive.starts, bucket: IO_LIVE_BUCKET_MS, span: LIVE_WINDOW_MS, latest, every: medianGap(at, 1_000) }
    : undefined;
  const avg = (n: number) => (n >= 1000 ? f.tokens(n) : n.toFixed(2));
  return {
    hasNow: now !== null,
    piece: {
      cores: rows(now?.cores).map((c) => ({ core: num(c.core), kind: c.kind === 'performance' ? 'performance'
        : 'efficiency', busyPct: num(c.busyPct) })),
      cpuBusyPct: cpu,
      load: load ? { one: num(load.one), five: num(load.five), fifteen: num(load.fifteen) } : null,
    } satisfies MachineInput,
    cpu: { value: cpu === null ? '—' : f.pctPoints(cpu), trend: series.map((p) => num(p.cpuBusyPct)) },
    loadText: load ? `load ${avg(load.one)} · ${avg(load.five)} · ${avg(load.fifteen)}` : 'load —',
    mem: {
      value: mem ? f.size(num(mem.usedBytes), GIB) : '—',
      delta: mem ? `of ${f.size(totalMem, GIB)} · swap ${f.size(num(mem.swapUsedBytes), GIB)} used` : '',
      trend: series.map((p) => num(p.memUsedBytes)),
      rows: memRows.map((r) => ({
        name: r.name,
        meta: r.meta,
        value: f.size(r.bytes, GIB),
        fill: fraction(r.bytes, memMax),
        tone: r.tone as 'gold' | 'session',
      })),
      title: `Memory · ${f.size(totalMem, GIB)}`,
    },
    disk: main
      ? {
          value: f.size(num(main.freeBytes)),
          delta: `of ${f.size(num(main.totalBytes))} · ${volumeLabel(main.volume)}`,
        }
      : { value: '—', delta: '' },
    volumes: volumes.slice(0, VOLUME_ROWS).map((v, i) => {
      const used = num(v.totalBytes) - num(v.freeBytes);
      return {
        name: volumeLabel(v.volume),
        free: `${f.size(num(v.freeBytes))} free`,
        meta: [v.kind ?? 'volume', v.filesystem ? filesystemLabel(v.filesystem).toUpperCase() : null,
          `${f.size(used)} of ${f.size(num(v.totalBytes))} used`].filter(Boolean).join(' · '),
        fill: share(used, num(v.totalBytes)),
        main: v === main,
        tone: i === 0 ? ('gold' as const) : ('session' as const),
      };
    }),
    io: { read: io ? f.rate(num(io.readBps)) : '—', write: io ? f.rate(num(io.writeBps)) : '—', readBars: read,
      writeBars: write, times: ioTimes },
    network: {
      name: chosen ? interfaceLabel(chosen) : null,
      down: current ? f.rate(current.rxBps) : '—',
      up: current ? f.rate(current.txBps) : '—',
      rows: listed.map((i) => {
        const points = d.interfaceSeries.find((s) => s.interface === i.interface)?.points ?? [];
        return {
          id: i.interface,
          name: interfaceLabel(i.interface),
          kind: KIND_LABEL[netKind(i.interface)],
          down: f.rate(i.rxBps),
          up: f.rate(i.txBps),
          sparkDown: points.map((p) => num(p.rxBps)),
          sparkUp: points.map((p) => num(p.txBps)),
        };
      }),
      selected: chosen,
      peak: flow && num(flow.peakRxBps) > 0 && flow.peakRxAt
        ? `PEAK ↓ ${f.rate(num(flow.peakRxBps))} · ${f.clock(flow.peakRxAt)}` : '',
      times,
      span: live ? LIVE_WINDOW_MS : undefined,
      down_: down,
      up_: up,
      ago: AGO[d.range],
    },
    apps: apps.map((a) => ({
      name: appLabel(a.app),
      meta: `CPU ${f.pctPoints(num(a.cpuPct))} · ${f.size(num(a.memBytes), GIB)}`,
      value: f.pctPoints(num(a.cpuPct)),
      fill: fraction(num(a.cpuPct), appMax),
    })),
    updated: now ? f.clockSeconds(now.at) : null,
    word: RANGE_WORDS[d.range],
  };
}

export function Machine() {
  const q = useMetrics('/api/machine', 'machine');
  const { range } = useMetricsFilters('machine');
  const [picked, setPicked] = useState<string | null>(null);
  const v = useMemo(() => (q.data ? machineView(q.data, picked) : null), [q.data, picked]);
  // A pick that is no longer charted is dropped for good, so a returning interface does not take the chart back.
  if (v && picked !== null && v.network.selected !== picked) setPicked(null);
  const paused = q.data?.historyPaused === true && (range === '24h' || range === '7d');
  const note = q.data && q.error ? `Update failed · ${errorText(q.error)}` : paused
    ? 'History paused · showing the last hour' : null;
  const filter = <MetricsFilter tab="machine" scope="host" updated={v?.updated ?? null} note={note} />;
  const slots = v && {
    left: (
      <>
        <KPI label="CPU · now" value={v.cpu.value} delta={v.loadText} trend={v.cpu.trend} tone="gold" />
        <KPI label="Memory used" value={v.mem.value} delta={v.mem.delta} trend={v.mem.trend} tone="default" />
        <KPI label="Disk free" value={v.disk.value} delta={v.disk.delta} tone="live" />
        <Panel index="04" title={v.mem.title}>
          <div className={styles.rowsTight}>
            {v.mem.rows.map((r) => (
              <ListRow key={r.name} name={r.name} meta={r.meta} value={r.value} fill={r.fill} className={own[r.tone]} />
            ))}
          </div>
        </Panel>
      </>
    ),
    centre: (
      <TabPiece tab="machine" placeholder={q.isPlaceholderData}>
        <MachinePiece data={v.piece} />
      </TabPiece>
    ),
    bottom: (
      <>
        <Panel index="03" title={`Network · ${v.word}${v.network.name ? ` · ${v.network.name}` : ''}`}
          className={styles.bottomPanel}>
          <div className={`${styles.netHead} ${own.netHead}`}>
            <span className={`${type_.mono} ${styles.netDown}`}>↓ {v.network.down}</span>
            <span className={`${type_.mono} ${styles.netUp}`}>↑ {v.network.up}</span>
          </div>
          <p className={`${type_.monoS} ${styles.netPeak} ${own.netPeak}`}>{v.network.peak || '\u00a0'}</p>
          <Fit fallback={[644, 38]} height={38}>
            {(w) => (
              <FlowLines
                times={v.network.times}
                series={[{ name: 'Down', tone: 'live', values: v.network.down_ },
                  { name: 'Up', tone: 'gold', values: v.network.up_ }]}
                width={w}
                height={38}
                span={v.network.span}
                format={f.rate}
                label={`Network throughput, download and upload${v.network.name ? `, ${v.network.name}` : ''}`}
              />
            )}
          </Fit>
          <div className={`${type_.monoS} ${styles.netAxis} ${own.netAxis}`}>
            <span>{v.network.ago}</span>
            <span>
              <i className={`${styles.swatchTeal} ${own.square}`} /> DOWN{' '}
              <i className={`${styles.swatchGold} ${own.square}`} /> UP
            </span>
            <span>NOW</span>
          </div>
          <NetRows rows={v.network.rows} selected={v.network.selected} onSelect={setPicked} />
        </Panel>
      </>
    ),
    right: (
      <>
        <Panel index="02" title="Disks" className={styles.rightTop}>
          <div className={styles.rowsTight}>
            {keyed(v.volumes, (d) => d.name).map(([key, d]) => (
              <div key={key} className={`${styles.disk} ${own.disk} ${own[d.tone]}`}>
                <span className={`${type_.body} ${styles.diskName}`}>{d.name}</span>
                <span className={`${type_.mono} ${styles.diskFree} ${own.diskFree}`}>{d.free}</span>
                <span className={`${type_.monoS} ${styles.diskMeta}`}>{d.meta}</span>
                <Meter
                  className={`${styles.diskMeter} ${own.diskMeter}`}
                  value={d.fill}
                  tone={d.tone}
                  label={`${d.name} used`}
                />
              </div>
            ))}
          </div>
          <p className={`${type_.label} ${styles.ioHead} ${own.ioHead}`}>
            <span className={own.ioTitle}>Disk I/O</span>
            <span className={`${type_.monoS} ${styles.netDown}`}>R {v.io.read}</span>
            <span className={`${type_.monoS} ${styles.netUp}`}>W {v.io.write}</span>
          </p>
          <Fit fallback={[264, 48]} height={48}>
            {(w) => (
              <PairBars
                a={{ name: 'Read', tone: 'live', values: v.io.readBars }}
                b={{ name: 'Write', tone: 'gold', values: v.io.writeBars }}
                width={w}
                height={48}
                times={v.io.times}
                format={f.rate}
                label="Disk read and write rate"
              />
            )}
          </Fit>
          <p className={`${type_.monoS} ${tabs.foot}`}>Docker VM disk: see Docker tab</p>
        </Panel>
        <Panel index="05" title="Top apps · CPU and memory" className={styles.rightLow}>
          <div className={styles.rowsTight}>
            {v.apps.map((a, i) => <ListRow key={i} name={a.name} meta={a.meta} value={a.value} fill={a.fill} />)}
          </div>
          {v.apps.length ? null : <p className={`${type_.mono} ${tabs.none}`}>No app figures yet</p>}
          <p className={`${type_.label} ${tabs.foot}`}>App names only · never command lines</p>
        </Panel>
      </>
    ),
  };
  return <TabFrame tab="machine" title="Machine" period={null} filter={filter} slots={slots} error={q.error}
    fetching={q.isFetching} placeholder={q.isPlaceholderData} />;
}
