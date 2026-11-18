import { useMemo } from 'react';
import type { Schemas } from '../../api';
import { useTranscript, useTrend } from '../../app/queries';
import { LineArea, type LineMarker } from '../../charts/LineArea';
import { ContextPiece } from '../../charts/pieces';
import { projectShort } from '../../charts/pieces/geometry';
import { CONTEXT_LIMIT, type ContextInput } from '../../charts/pieces/context';
import { MarkerShape } from '../../components/Marker';
import { KPI, ListRow, Panel } from '../../components';
import * as f from '../../format/format';
import { num, numOrNull, projectLabel, rows } from '../../format/labels';
import { fillDays, fraction, share } from '../../format/scale';
import { Fit } from '../../components/Fit';
import type_ from '../../styles/type.module.css';
import { LineRow, pieceRange } from './parts';
import { TabFrame, TabPiece } from './TabFrame';
import tabs from './tabs.module.css';
import styles from './views.module.css';
import own from './Context.module.css';
import a11y from '../../styles/a11y.module.css';
import { keyed } from '../../app/keys';

type Data = Schemas['ContextResponse'];

export function contextView(d: Data, now: Date, trend: Data = d) {
  const days = num(d.period.days);
  const target = num(d.rereadTarget);
  const reread = num(d.rereadShare.value);
  const peak = d.peak
    ? {
        tokens: num(d.peak.contextTokens),
        limit: num(d.peak.contextLimitTokens),
        project: projectLabel(d.peak.project),
        day: d.peak.day,
      }
    : null;
  const bands = d.bands.map((b) => {
    const calls = num(b.calls);
    return { calls, min: num(b.band.minTokens) };
  });
  const allCalls = bands.reduce((a, b) => a + b.calls, 0);
  const defs = rows(d.contextBands).length ? rows(d.contextBands) : rows(d.bands).map((b) => b.band);
  const bandRows = [...defs].sort((a, b) => num(b.minTokens) - num(a.minTokens)).map((def) => {
    const min = num(def.minTokens);
    const max = numOrNull(def.maxTokens);
    const hit = d.bands.filter((b) => num(b.band.minTokens) === min);
    const calls = hit.reduce((a, b) => a + num(b.calls), 0);
    const context = hit.reduce((a, b) => a + num(b.contextTokens), 0);
    const read = hit.reduce((a, b) => a + num(b.cacheReadTokens), 0);
    return {
      label: f.bandLabel({ minTokens: min, maxTokens: max }),
      calls,
      callShare: allCalls > 0 ? calls / allCalls : 0,
      core: context > 0 ? share(read, context) : null,
      over: max === null || max > CONTEXT_LIMIT,
    };
  });
  const projects = [...d.byProject].sort((a, b) => num(b.peakContextTokens) - num(a.peakContextTokens)).slice(0, 4);
  const emptyDay = (day: string) => ({
    day, peakContextTokens: 0, compactions: 0, rereadShare: 0, avgContextTokens: 0,
  });
  const daily = fillDays(trend.period.from, trend.period.to, trend.daily, emptyDay);
  const markers: LineMarker[] = daily.flatMap((r, x) =>
    num(r.compactions) > 0
      ? [{
          x,
          kind: 'compaction' as const,
          name: `${f.dayLabel(r.day)}: ${f.counted(num(r.compactions), 'compaction')}`,
        }]
      : [],
  );
  const avgFreed = numOrNull(d.compactions?.avgFreedTokens);
  return {
    empty: allCalls === 0,
    days,
    peak: peak
      ? {
          value: f.tokens(peak.tokens),
          delta: [
            peak.day === f.localDay(now) ? 'today' : f.dayLabel(peak.day),
            peak.limit > 0 ? `${f.pct(peak.tokens / peak.limit)} of ${f.tokens(peak.limit)}` : '',
            peak.project,
          ].filter(Boolean).join(' · '),
          over: peak.limit > 0 && peak.tokens >= peak.limit * 0.8,
        }
      : { value: '—', delta: '', over: false },
    avg: {
      value: f.tokens(num(d.avgContextPerCall.value)),
      delta: f.deltaPct(num(d.avgContextPerCall.value), numOrNull(d.avgContextPerCall.previous), days),
      trend: daily.map((r) => num(r.avgContextTokens)),
    },
    reread: {
      value: f.pct(reread),
      delta: [f.deltaPts(reread, numOrNull(d.rereadShare.previous)), `target ${f.pct(target)}`]
        .filter(Boolean)
        .join(' · '),
      over: reread > target,
      trend: daily.map((r) => num(r.rereadShare)),
    },
    projects: projects.map((p) => {
      const fill = fraction(num(p.peakContextTokens), num(p.contextLimitTokens));
      return {
        name: projectLabel(p.project),
        meta: `${f.counted(num(p.sessions), 'session')} · ${f.counted(num(p.compactions), 'compaction')}`,
        value: `${f.tokens(num(p.peakContextTokens))} / ${f.tokens(num(p.contextLimitTokens))}`,
        fill,
        over: num(p.contextLimitTokens) > 0 && num(p.peakContextTokens) > num(p.contextLimitTokens),
      };
    }),
    piece: {
      defs: rows(d.contextBands).map((b) => ({ minTokens: num(b.minTokens), maxTokens: numOrNull(b.maxTokens) })),
      bands: rows(d.bands).map((b) => ({
        minTokens: num(b.band.minTokens),
        calls: num(b.calls),
        contextTokens: num(b.contextTokens),
        cacheReadTokens: num(b.cacheReadTokens),
      })),
      avgPerCall: numOrNull(d.avgContextPerCall.value),
      compactions: num(d.compactions?.count),
      rangeLabel: pieceRange(days),
    } satisfies ContextInput,
    bands: bandRows.map((b) => ({
      label: b.label,
      meta: `${f.counted(b.calls, 'call')} · ${f.pct1(b.callShare)}`,
      value: b.core === null ? '—' : f.pct1(b.core),
      fill: b.core ?? 0,
      over: b.over,
    })),
    target,
    recent: rows(d.compactions?.recent).slice(0, 5).map((c, i) => ({
      key: `${c.at}-${i}`,
      cells: [
        f.whenLabel(c.at, now),
        projectShort(projectLabel(c.project)),
        `${f.tokens(num(c.tokensBefore))} → ${c.tokensAfter === null ? '?' : f.tokens(num(c.tokensAfter))}`,
      ] as const,
    })),
    compactionFoot:
      f.counted(num(d.compactions?.count), 'compaction') +
      (avgFreed === null ? '' : ` · avg freed ${f.tokens(avgFreed)}`),
    flow: daily.map((r, x) => ({ x, y: num(r.peakContextTokens) })),
    flowDays: daily.map((r) => f.dayLabel(r.day)),
    peakTrend: daily.map((r) => num(r.peakContextTokens)),
    markers,
    firstDay: f.dayLabel(trend.period.from),
    lastDay: trend.period.to === f.localDay(now) ? 'Today' : f.dayLabel(trend.period.to),
    spanLabel: `${daily.length} d ago`,
  };
}

export function Context() {
  const q = useTranscript('/api/context', 'context');
  const t = useTrend('/api/context', 'context');
  const v = useMemo(() => (q.data ? contextView(q.data, new Date(), t.data) : null), [q.data, t.data]);
  const period = q.data ? { from: q.data.period.from, to: q.data.period.to, partial: q.data.period.partial } : null;
  const slots = v && {
    left: (
      <>
        <KPI
          label="Peak context"
          value={v.peak.value}
          delta={v.peak.delta}
          trend={v.peakTrend}
          trendToday
          tone={v.peak.over ? 'warn' : 'gold'}
        />
        <KPI
          label="Avg context / call"
          value={v.avg.value}
          delta={v.avg.delta ?? ''}
          trend={v.avg.trend}
          trendToday
          tone="gold"
        />
        <KPI
          label="Re-read share"
          value={v.reread.value}
          delta={v.reread.delta}
          trend={v.reread.trend}
          trendToday
          tone={v.reread.over ? 'warn' : 'live'}
        />
        <Panel index="04" title="Peak context by project">
          <div className={styles.rowsTight}>
            {keyed(v.projects, (p) => p.name).map(([key, p]) => (
              <div key={key} className={own.rowWrap}>
                <ListRow
                  name={p.name}
                  meta={p.meta}
                  value={p.value}
                  fill={p.fill}
                  className={`${own.row} ${p.over ? own.over : ''}`}
                />
                {p.over ? <span className={a11y.hidden}>Over the context limit</span> : null}
              </div>
            ))}
          </div>
        </Panel>
      </>
    ),
    centre: (
      <TabPiece tab="context" placeholder={q.isPlaceholderData}>
        <ContextPiece data={v.piece} />
      </TabPiece>
    ),
    bottom: (
      <Panel index="03" title="Peak context · last 14 d" className={styles.bottomPanel}>
        <Fit fallback={[644, 76]} height={76}>
          {(w) => (
            <LineArea
              data={v.flow}
              markers={v.markers}
              tone="gold"
              width={w}
              height={76}
              yTicks={0}
              limit={{ value: CONTEXT_LIMIT, label: f.tokens(CONTEXT_LIMIT) }}
              markerLift={14}
              today
              todayLabel={false}
              formatY={f.tokens}
              xLabels={v.flowDays}
              label="Peak context per day"
            />
          )}
        </Fit>
        <p className={`${type_.monoS} ${own.axis}`} aria-hidden="true">
          <span>{v.spanLabel}</span>
          <span className={own.key}>
            <svg width="10" height="10" viewBox="0 0 16 16"><MarkerShape kind="compaction" /></svg> Compaction
          </span>
          <span>{v.lastDay}</span>
        </p>
      </Panel>
    ),
    right: (
      <>
        <Panel index="02" title="Re-read by band" className={styles.rightTop}>
          <div className={styles.rowsTight}>
            {v.bands.map((b, i) => (
              <ListRow
                key={i}
                name={b.label}
                meta={b.meta}
                value={b.value}
                fill={b.fill}
                className={`${own.row} ${own.bandRow} ${b.over ? own.overBand : ''}`}
              />
            ))}
          </div>
        </Panel>
        <Panel index="05" title="Last compactions" tone="warn" className={styles.rightLow}>
          {v.recent.length ? (
            v.recent.map((c) => <LineRow key={c.key} cells={c.cells} tone="warn" />)
          ) : (
            <p className={`${type_.mono} ${tabs.none}`}>No compactions</p>
          )}
          <p className={`${type_.label} ${tabs.footWarn}`}>{v.compactionFoot}</p>
        </Panel>
      </>
    ),
  };
  return (
    <TabFrame
      tab="context"
      title="Context"
      period={period}
      slots={slots}
      empty={v?.empty ?? false}
      error={q.error}
      fetching={q.isFetching}
      placeholder={q.isPlaceholderData}
    />
  );
}
