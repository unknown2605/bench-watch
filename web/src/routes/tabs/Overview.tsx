import { Link } from '@tanstack/react-router';
import { useId, useMemo, type CSSProperties } from 'react';
import type { Schemas } from '../../api';
import { useSections, useToolErrorsToday, useTranscript, useTrend } from '../../app/queries';
import { ChartTable } from '../../charts/ChartTable';
import { OverviewPiece } from '../../charts/pieces';
import { projectShort } from '../../charts/pieces/geometry';
import type { OverviewInput } from '../../charts/pieces/overview';
import { KPI, ListRow, Panel } from '../../components';
import * as f from '../../format/format';
import { modelLabel, num, numOrNull, projectLabel, rows, sessionLabel } from '../../format/labels';
import { fillDays, fraction } from '../../format/scale';
import { AnimatedNumber } from '../../motion/AnimatedNumber';
import type_ from '../../styles/type.module.css';
import { NOT_FOR_CLIENT } from '../../format/clients';
import { ClientSplit, UnpricedNote } from './ClientSplit';
import { pieceRange } from './parts';
import { TabFrame, TabPiece } from './TabFrame';
import styles from './Overview.module.css';
import a11y from '../../styles/a11y.module.css';
import tabs from './tabs.module.css';

type Data = Schemas['OverviewResponse'];

const MAP_DOTS = 12;
const BAND_FULL = 0.5;
type DotTone = 'p0' | 'p1' | 'p2' | 'more' | 'over' | 'idle';
type MixTone = 'read' | 'write' | 'input' | 'output';
type MixPart = { name: string; value: number; tone: MixTone };

const PROJECT_TONES = ['p0', 'p1', 'p2'] as const;
const LEGEND_ROWS = PROJECT_TONES.length;

// The legend has room for LEGEND_ROWS lines; the "+N more" line takes one of them.
const legendNames = (projects: number): number => (projects > LEGEND_ROWS ? LEGEND_ROWS - 1 : projects);

const projectTone = (i: number, projects: number): DotTone =>
  (i < legendNames(projects) ? PROJECT_TONES[i] : undefined) ?? 'more';

function sortedSessions(list: readonly Schemas['LiveSession'][]) {
  return [...list].sort(
    (a, b) =>
      Number(b.state === 'live') - Number(a.state === 'live') || b.lastActivityAt.localeCompare(a.lastActivityAt),
  );
}

export function overviewView(d: Data, now: Date, trend: Data = d) {
  const days = num(d.period.days);
  const emptyDay = (day: string) => ({ day, mainTokens: 0, subTokens: 0, costUsd: 0, calls: 0, rereadShare: 0 });
  const daily = fillDays(trend.period.from, trend.period.to, trend.daily, emptyDay);
  const target = num(d.rereadTarget);
  const live = sortedSessions(d.liveSessions)
    .slice(0, 5)
    .map((s) => {
      const ctx = num(s.contextTokens);
      const limit = num(s.contextLimitTokens);
      const over = limit > 0 && ctx > limit;
      return {
        key: `${projectLabel(s.project)}/${sessionLabel(s.session)}`,
        session: sessionLabel(s.session),
        project: projectLabel(s.project),
        name: projectLabel(s.project),
        meta: [s.state === 'live' ? 'LIVE' : 'IDLE', f.modelShort(modelLabel(s.model)), f.since(s.startedAt, now)]
          .join(' · '),
        value: limit > 0 ? `${f.tokens(ctx)} / ${f.tokens(limit)}` : f.tokens(ctx),
        fill: limit > 0 ? fraction(ctx, limit) : null,
        live: s.state === 'live',
        over,
      };
    });
  const projects = [...new Set(d.liveSessions.map((s) => projectLabel(s.project)))];
  const dots = sortedSessions(d.liveSessions).slice(0, MAP_DOTS).map((s, i) => {
    const project = projectLabel(s.project);
    const limit = num(s.contextLimitTokens);
    const over = limit > 0 && num(s.contextTokens) > limit;
    const live = s.state === 'live';
    const tone: DotTone = !live ? 'idle' : over ? 'over' : projectTone(projects.indexOf(project), projects.length);
    return { n: i + 1, name: `${project}/${sessionLabel(s.session)}`, tone, live, over };
  });
  const bandCalls = rows(d.bands).map((b) => ({ min: num(b.band.minTokens),
    calls: num(b.mainCalls) + num(b.subCalls) }));
  const allCalls = bandCalls.reduce((a, b) => a + b.calls, 0);
  const overCalls = bandCalls.filter((b) => b.min >= 200_000).reduce((a, b) => a + b.calls, 0);
  const piece: OverviewInput = {
    tokens: num(d.tokens.value),
    previous: numOrNull(d.tokens.previous),
    days,
    mainShare: numOrNull(d.scopeShare.main),
    reread: numOrNull(d.rereadShare.value),
    sub: numOrNull(d.scopeShare.sub),
    over: allCalls > 0 ? overCalls / allCalls : null,
    trend: daily.map((r) => ({ day: r.day, tokens: num(r.mainTokens) + num(r.subTokens) })),
    today: trend.period.to,
    rangeLabel: pieceRange(days),
  };
  const mix: MixPart[] = [
    { name: 'cache read', value: num(d.tokenMix.cacheRead), tone: 'read' },
    { name: 'cache write', value: num(d.tokenMix.cacheWrite), tone: 'write' },
    { name: 'input', value: num(d.tokenMix.input), tone: 'input' },
    { name: 'output', value: num(d.tokenMix.output), tone: 'output' },
  ];
  const bandDefs = rows(d.contextBands).length ? rows(d.contextBands) : rows(d.bands).map((b) => b.band);
  return {
    empty: num(d.calls.value) === 0 && num(d.tokens.value) === 0,
    days,
    claudeOnly: d.claudeOnly?.available !== false,
    byClient: rows(d.byClient),
    unpriced: rows(d.unpriced),
    cost: { value: f.usd(num(d.costUsd.value)), delta: f.deltaPct(num(d.costUsd.value), numOrNull(d.costUsd.previous),
      days), trend: daily.map((r) => num(r.costUsd)) },
    calls: { value: f.int(num(d.calls.value)), delta: f.deltaPct(num(d.calls.value), numOrNull(d.calls.previous),
      days), trend: daily.map((r) => num(r.calls)) },
    reread: {
      value: f.pct(num(d.rereadShare.value)),
      delta: [f.deltaPts(num(d.rereadShare.value), numOrNull(d.rereadShare.previous)),
        `target ${f.pct(target)}`].filter(Boolean).join(' · '),
      over: num(d.rereadShare.value) > target,
      trend: daily.map((r) => num(r.rereadShare)),
    },
    piece,
    live,
    map: {
      sessions: num(d.sessionMap.sessions),
      live: num(d.sessionMap.live),
      dots,
      legend: projects.slice(0, legendNames(projects.length)),
      more: projects.length - legendNames(projects.length),
    },
    bands: bandDefs.map((def) => {
      const min = num(def.minTokens);
      const calls = bandCalls.filter((b) => b.min === min).reduce((a, b) => a + b.calls, 0);
      const callShare = allCalls > 0 ? calls / allCalls : 0;
      return {
        label: f.bandLabel({ minTokens: min, maxTokens: numOrNull(def.maxTokens) }),
        calls,
        share: callShare,
        fill: Math.min(1, callShare / BAND_FULL),
      };
    }),
    mix,
  };
}

export function Overview() {
  const q = useTranscript('/api/overview', 'overview');
  const t = useTrend('/api/overview', 'overview');
  const toolErrors = useToolErrorsToday();
  const { guard: hasGuard } = useSections();
  const v = useMemo(() => (q.data ? overviewView(q.data, new Date(), t.data) : null), [q.data, t.data]);
  const period = q.data ? { from: q.data.period.from, to: q.data.period.to, partial: q.data.period.partial } : null;
  const projectTotal = v ? v.map.legend.length + v.map.more : 0;
  const slots = v && {
    left: (
      <>
        <KPI
          label="Est. cost"
          value={v.cost.value}
          delta={v.cost.delta ?? ''}
          trend={v.cost.trend}
          trendToday
          tone="live"
        />
        <KPI
          label="API calls"
          value={v.calls.value}
          delta={v.calls.delta ?? ''}
          trend={v.calls.trend}
          trendToday
          tone="gold"
        />
        <KPI
          label="Re-read share"
          value={v.reread.value}
          delta={v.reread.delta}
          trend={v.reread.trend}
          trendToday
          tone={v.reread.over ? 'warn' : 'gold'}
        />
        <Panel index="04" title="Session map">
          <div className={styles.map}>
            <SessionRing dots={v.map.dots} label="Live and idle sessions" />
            <dl className={styles.mapStats}>
              <dt className={a11yLabel}>Sessions</dt>
              <dd className={styles.mapBig}><AnimatedNumber text={String(f.int(v.map.sessions))} /></dd>
              <dd className={`${type_.monoS} ${styles.mapCaption}`}>sessions</dd>
              <dt className={a11yLabel}>Live</dt>
              <dd className={styles.mapSmall}>
                <span className={styles.mapLiveNum}>{f.int(v.map.live)}</span>{' '}
                <span className={`${type_.monoS} ${styles.live}`}>live</span>
              </dd>
              <dt className={a11yLabel}>Projects</dt>
              {v.map.legend.map((p, i) => (
                <dd key={p} className={`${type_.monoS} ${styles.mapLegend}`}>
                  <span
                    className={styles.legendDot}
                    style={{ background: DOT_COLOR[projectTone(i, projectTotal)] }}
                  />
                  <span className={styles.legendName} title={p}>{projectShort(p)}</span>
                </dd>
              ))}
              {v.map.more ? (
                <dd className={`${type_.monoS} ${styles.mapLegend}`}>
                  <span className={styles.legendDot} style={{ background: DOT_COLOR.more }} />
                  <span className={styles.legendName}>+{v.map.more} more</span>
                </dd>
              ) : null}
            </dl>
          </div>
          <Link to="/sessions" className={`${type_.heading} ${styles.open}`}>Open map →</Link>
          {toolErrors === null || !hasGuard ? null : (
            <Link to="/guard" className={`${type_.monoS} ${styles.toolErrors}`}>
              <span className={toolErrors > 0 ? styles.toolErrorsHot : undefined}>{f.int(toolErrors)}</span>
              {` tool ${toolErrors === 1 ? 'error' : 'errors'} today →`}
            </Link>
          )}
        </Panel>
      </>
    ),
    centre: (
      <TabPiece tab="overview" placeholder={q.isPlaceholderData}>
        <OverviewPiece data={v.piece} />
      </TabPiece>
    ),
    bottom: (
      <Panel index="03" title="Token mix" className={styles.mixPanel}>
        <TokenMix parts={v.mix} label="Token mix" />
        <UnpricedNote unpriced={v.unpriced} className={styles.mixNote} />
      </Panel>
    ),
    right: (
      <>
        <Panel index="02" title="Live sessions" className={styles.livePanel}>
          {v.live.length ? (
            <div className={styles.liveList}>
              {v.live.map((s) => (
                <Link
                  key={s.key}
                  to="/sessions"
                  search={{ session: s.session, project: s.project }}
                  className={styles.liveLink}
                  aria-label={`Open session detail: ${s.name}, ${s.live ? 'live' : 'idle'}`}
                >
                  <ListRow
                    name={s.name}
                    meta={s.meta}
                    value={s.value}
                    fill={s.fill}
                    className={[styles.liveRow, s.live && !s.over ? '' : styles.liveAmber, s.over ? styles.over : '']
                      .filter(Boolean)
                      .join(' ')}
                  />
                  {s.over ? <span className={a11yLabel}>Over the context limit</span> : null}
                </Link>
              ))}
            </div>
          ) : (
            <p className={`${type_.mono} ${tabs.none}`}>No live or idle sessions</p>
          )}
        </Panel>
        <Panel index="05" title="Context bands" className={styles.bandsPanel}>
          {v.claudeOnly ? <div className={styles.bands}>
            {v.bands.map((b, i) => (
              <div
                key={i}
                className={styles.band}
                role="img"
                aria-label={`${b.label}: ${f.int(b.calls)} calls, ${f.pct1(b.share)} of calls`}
              >
                <span className={`${type_.monoS} ${styles.bandLabel}`}>{b.label}</span>
                <span className={styles.bandTrack}>
                  <span className={styles.bandMain} style={{ width: `${b.fill * 100}%` }} />
                </span>
              </div>
            ))}
          </div> : <p className={`${type_.mono} ${tabs.none}`}>{NOT_FOR_CLIENT}</p>}
          {v.claudeOnly ? (
            <p className={`${type_.label} ${styles.bandLegend}`} aria-hidden="true">
              <span className={styles.keyMain} /> Share of calls · full bar = {f.pct(BAND_FULL)}
            </p>
          ) : null}
        </Panel>
        <ClientSplit tab="overview" index="06" split={v.byClient} className={styles.split} />
      </>
    ),
  };
  return (
    <TabFrame tab="overview" title="Overview" period={period} slots={slots} empty={v?.empty ?? false} error={q.error}
      fetching={q.isFetching} placeholder={q.isPlaceholderData} variant="overview" />
  );
}

const a11yLabel = a11y.hidden;

const DOT_COLOR: Record<DotTone, string> = {
  p0: 'var(--sh-ink-warm)',
  p1: 'var(--sh-data-session)',
  p2: 'var(--sh-accent-gold)',
  more: 'var(--sh-ink-dim)',
  over: 'var(--sh-status-warn)',
  idle: 'color-mix(in srgb, var(--sh-ink-dim) 45%, var(--sh-surface-navy))',
};
const MIX_COLOR: Record<MixTone, string> = {
  read: 'var(--sh-accent-gold)',
  write: 'color-mix(in srgb, var(--sh-ink-warm) 82%, var(--sh-ink-dim))',
  input: 'color-mix(in srgb, var(--sh-ink-dim) 42%, var(--sh-surface-navy))',
  output: 'var(--sh-status-warn)',
};

type Dot = { n: number; name: string; tone: DotTone; live: boolean; over: boolean };

/** One ring; dots run clockwise from the top in list order, live sessions first. */
function SessionRing({ dots, label }: { dots: readonly Dot[]; label: string }) {
  const tableId = useId();
  const size = 124;
  const c = size / 2;
  const r = 50;
  const step = Math.min(30, 360 / Math.max(dots.length, 1));
  return (
    <div className={styles.ring}>
      <svg
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        role="img"
        aria-label={label}
        aria-describedby={tableId}
      >
        <circle className={styles.ringLine} cx={c} cy={c} r={r} />
        {dots.map((d, i) => {
          const a = (i * step * Math.PI) / 180;
          const x = c + Math.sin(a) * r;
          const y = c - Math.cos(a) * r;
          return (
            <g key={d.n} className={styles.dot} style={{ '--c': DOT_COLOR[d.tone] } as CSSProperties}>
              <circle cx={x} cy={y} r={8} />
              <text x={x} y={y + 3} textAnchor="middle">{d.n}</text>
            </g>
          );
        })}
      </svg>
      <ChartTable
        id={tableId}
        caption={label}
        head={['#', 'Session', 'State', 'Over limit']}
        rows={dots.map((d) => [d.n, d.name, d.live ? 'live' : 'idle', d.over ? 'yes' : 'no'])}
      />
    </div>
  );
}

const MIX_MIN = 0.008;
const MIX_GAP = 0.004;

/** Stacked share bar; every non-zero part keeps a visible minimum width, the legend carries the exact shares. */
function TokenMix({ parts, label }: { parts: readonly MixPart[]; label: string }) {
  const { shares, segs } = mixSegments(parts);
  return (
    <div className={styles.mix}>
      <div
        className={styles.mixBar}
        role="img"
        aria-label={`${label}: ${parts.map((p, i) => `${p.name} ${mixPct(shares[i] ?? 0)}`).join(', ')}`}
      >
        {segs.map((s) => (
          <span
            key={s.p.name}
            className={styles.mixSeg}
            style={{ left: `${s.left * 100}%`, width: `${s.width * 100}%`, background: MIX_COLOR[s.p.tone] }}
          />
        ))}
      </div>
      <ul className={styles.mixLegend} aria-hidden="true">
        {parts.map((p, i) => (
          <li key={p.name} className={type_.label}>
            <span className={styles.swatch} style={{ background: MIX_COLOR[p.tone] }} />
            {p.name} {mixPct(shares[i] ?? 0)}
          </li>
        ))}
      </ul>
    </div>
  );
}

function mixSegments(parts: readonly MixPart[]) {
  const total = parts.reduce((a, p) => a + Math.max(0, p.value), 0);
  const shares = parts.map((p) => (total > 0 ? Math.max(0, p.value) / total : 0));
  const shown = shares.filter((x) => x > 0).length;
  const lifted = shares.map((x) => (x > 0 ? Math.max(x, MIX_MIN) : 0));
  const bigSum = lifted.reduce((a, x, i) => a + (x > (shares[i] ?? 0) ? 0 : x), 0);
  const smallSum = lifted.reduce((a, x, i) => a + (x > (shares[i] ?? 0) ? x : 0), 0);
  const room = Math.max(0, 1 - MIX_GAP * Math.max(shown - 1, 0) - smallSum);
  const segs: { p: MixPart; left: number; width: number }[] = [];
  let at = 0;
  parts.forEach((p, i) => {
    const raw = shares[i] ?? 0;
    const lift = lifted[i] ?? 0;
    if (raw <= 0) return;
    const width = lift > raw ? lift : bigSum > 0 ? (lift / bigSum) * room : 0;
    segs.push({ p, left: at, width });
    at += width + MIX_GAP;
  });
  return { shares, segs };
}

function mixPct(share: number): string {
  const v = share * 100;
  if (v <= 0) return '0%';
  if (v < 0.001) return '<0.001%';
  if (v >= 1) return `${v.toFixed(1)}%`;
  return `${Number(v.toPrecision(1))}%`;
}
