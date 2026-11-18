import { useMemo, useState } from 'react';
import type { Schemas } from '../../api';
import { useMeta, useTranscript, useTrend } from '../../app/queries';
import { Bars, type BarDatum } from '../../charts/Bars';
import { FillsPiece } from '../../charts/pieces';
import { projectShort } from '../../charts/pieces/geometry';
import { FILL_TOP, type FillsInput } from '../../charts/pieces/fills';
import { KPI, ListRow, Meter, Panel } from '../../components';
import * as f from '../../format/format';
import { num, numOrNull, projectLabel, rows, toolLabel } from '../../format/labels';
import { daysBetween, fraction, maxOf, share } from '../../format/scale';
import { Fit } from '../../components/Fit';
import type_ from '../../styles/type.module.css';
import { ClippedName, Heading, pieceRange, StatValue } from './parts';
import { TabFrame, TabPiece } from './TabFrame';
import tabs from './tabs.module.css';
import styles from './views.module.css';
import own from './Fills.module.css';
import { keyed } from '../../app/keys';

type Data = Schemas['FillsResponse'];
export type Group = Schemas['FillsGroup'];

const RING_SLICES = 3;
const OTHER = 'other';
const RUN_DAYS = 14;
const TABLE_ROWS = 5;

type Item = { name: string; results: number; tokens: number; weighted: number };

function groupItems(d: Data): Item[] {
  if (d.group === 'tool') {
    const by = new Map<string, Item>();
    for (const t of d.tools) {
      const name = f.toolFamily(toolLabel(t.tool));
      const row = by.get(name) ?? { name, results: 0, tokens: 0, weighted: 0 };
      row.results += num(t.results);
      row.tokens += num(t.tokens);
      row.weighted += num(t.weightedTokens);
      by.set(name, row);
    }
    return [...by.values()];
  }
  return d.groups.map((g) => ({
    name: g.project !== undefined ? projectLabel(g.project) : g.sizeBand
      ? f.bandLabel({ minTokens: num(g.sizeBand.minTokens), maxTokens: numOrNull(g.sizeBand.maxTokens) }) : OTHER,
    results: num(g.results),
    tokens: num(g.tokens),
    weighted: num(g.weightedTokens),
  }));
}

/** Everything the page shows, for the group chosen and the ring slice picked (null: nothing picked). */
export function fillsView(d: Data, picked: string | null, trend: Data = d) {
  const days = num(d.period.days);
  const items = groupItems(d).sort((a, b) => b.weighted - a.weighted);
  const total = items.reduce((s, i) => s + i.weighted, 0);
  const head = items.slice(0, RING_SLICES);
  const rest = items.slice(RING_SLICES);
  const ring: Item[] = rest.length
    ? [...head, { name: OTHER, results: rest.reduce((s, i) => s + i.results, 0),
      tokens: rest.reduce((s, i) => s + i.tokens, 0), weighted: rest.reduce((s, i) => s + i.weighted, 0) }]
    : head;
  const isTool = d.group === 'tool';
  const filtered = isTool && picked !== null && picked !== OTHER;
  const current = ring.find((i) => i.name === picked) ?? items.find((i) => i.name === picked) ?? ring[0] ?? null;
  const heaviest = rows(d.tools)
    .filter((t) => num(t.weightedTokens) > 0)
    .sort((a, b) => num(b.weightedTokens) - num(a.weightedTokens))
    .slice(0, FILL_TOP)
    .map((t) => toolLabel(t.tool));
  const inFamily = (tool: string) => current !== null && f.toolFamily(toolLabel(tool)) === current.name;
  const stats = isTool && current ? d.tools.filter((t) => inFamily(t.tool)) : [];
  const p95 = Math.max(0, ...stats.map((t) => num(t.p95Tokens)));
  const cost = stats.reduce((s, t) => s + num(t.costUsd), 0);
  const runDays = daysBetween(trend.period.from, trend.period.to).slice(-RUN_DAYS);
  const bars: BarDatum[] = runDays.map((day) => {
    const v = trend.daily.reduce((s, r) => (r.day === day && inFamily(r.tool) ? s + num(r.tokens) : s), 0);
    return { key: day, label: f.dayLabel(day), value: v, display: f.tokens(v) };
  });
  const hasTools = d.byProject.some((p) => (p.tools?.length ?? 0) > 0);
  const byFamily = filtered && hasTools;
  const projects = d.byProject
    .map((p) => {
      const families = new Map<string, number>();
      for (const t of p.tools ?? []) {
        const name = f.toolFamily(toolLabel(t.tool));
        families.set(name, (families.get(name) ?? 0) + num(t.weightedTokens));
      }
      const whole = num(p.weightedTokens);
      const own = families.get(current?.name ?? '') ?? 0;
      const mine: [string, number][] = byFamily ? [[current?.name ?? '', own]]
        : [...families].sort((x, y) => y[1] - x[1]).slice(0, 3);
      return {
        name: projectLabel(p.project),
        weighted: byFamily ? own : whole,
        meta: mine.map(([name, w]) => `${name} ${f.pct(share(w, whole))}`).join(' · ') || '—',
      };
    })
    .filter((p) => p.weighted > 0)
    .sort((a, b) => b.weighted - a.weighted)
    .slice(0, 4);
  const projectMax = maxOf(projects, (p) => p.weighted);
  const bash = d.tools
    .filter((t) => toolLabel(t.tool).startsWith('Bash: '))
    .sort((a, b) => num(b.weightedTokens) - num(a.weightedTokens));
  const bashTotal = bash.reduce((s, t) => s + num(t.weightedTokens), 0);
  const daily = daysBetween(trend.period.from, trend.period.to).map((day) =>
    trend.daily.reduce((s, r) => (r.day === day ? s + num(r.results) : s), 0),
  );
  const largest = d.largest;
  return {
    empty: num(d.results.value) === 0 && num(d.tokens) === 0,
    days,
    isTool,
    filtered,
    ringName: current?.name ?? null,
    pressedTool: isTool && current && current.name !== OTHER ? (heaviest.find((t) => f.toolFamily(t) === current.name)
      ?? null) : null,
    results: { value: f.tokens(num(d.results.value)), delta: f.deltaPct(num(d.results.value),
      numOrNull(d.results.previous), days), trend: daily },
    context: f.pct(num(d.contextShare)),
    largest: largest
      ? { value: f.tokens(num(largest.tokens)), delta: [toolLabel(largest.tool), projectLabel(largest.project),
        f.dayLabel(largest.day)].join(' · ') }
      : { value: '—', delta: '' },
    piece: {
      tools: rows(d.tools).map((t) => ({ tool: toolLabel(t.tool), tokens: num(t.tokens),
        weightedTokens: num(t.weightedTokens), results: num(t.results) })),
      contextShare: numOrNull(d.contextShare),
      resultTokens: num(d.tokens),
      rangeLabel: pieceRange(days),
    } satisfies FillsInput,
    selected: current && {
      name: current.name,
      line: `${f.tokens(current.results)} results · ${days === 1 ? 'today' : `${days} d`}`,
      avg: f.tokens(share(current.tokens, current.results)),
      p95: p95 > 0 ? `≤${f.tokens(p95)}` : '—',
      share: f.pct(share(current.weighted, total)),
      cost: f.usd(cost),
      costShown: isTool,
      repeat: repeatShare(stats),
      repeatGap: repeatGap(stats),
      repeatNote: repeatNote(stats),
      bars,
    },
    bash: bash.slice(0, 4).map((t) => ({
      name: `Bash: ${f.toolDetail(toolLabel(t.tool))}`,
      meta: `${f.tokens(num(t.results))} results · avg ${f.tokens(share(num(t.tokens), num(t.results)))}`,
      value: f.pct(share(num(t.weightedTokens), bashTotal)),
      fill: share(num(t.weightedTokens), num(bash[0]?.weightedTokens)),
    })),
    projects: projects.map((p) => ({ ...p, value: f.tokens(p.weighted), fill: fraction(p.weighted, projectMax) })),
    top: d.top.slice(0, TABLE_ROWS).map((r, i) => {
      const tool = toolLabel(r.tool);
      return {
        key: `${r.day}-${i}`,
        tool: f.toolFamily(tool),
        kind: f.toolDetail(tool) || '—',
        project: projectLabel(r.project),
        tokens: f.tokens(num(r.tokens)),
        resends: `×${f.int(num(r.resends))}`,
        weighted: f.tokens(num(r.weightedTokens)),
        day: f.dayLabel(r.day),
        match: current !== null && isTool && f.toolFamily(tool) === current.name,
      };
    }),
  };
}

type RepeatCounts = Partial<Pick<Schemas['ToolStats'], 'results' | 'repeatResults' | 'repeatMeasuredResults'>>;

const sumOf = (stats: readonly RepeatCounts[], field: keyof RepeatCounts) =>
  stats.reduce((s, t) => s + num(t[field]), 0);

/** Share of the measured results that returned what their transcript already held; null when none was measured. */
export function repeatShare(stats: readonly RepeatCounts[]): number | null {
  const measured = sumOf(stats, 'repeatMeasuredResults');
  return measured > 0 ? Math.min(1, sumOf(stats, 'repeatResults') / measured) : null;
}

/** What stands in for a missing share: no result at all, or none from a day that measured repeats. */
export function repeatGap(stats: readonly RepeatCounts[]): string {
  return sumOf(stats, 'results') > 0 ? 'not available for this period' : 'no results';
}

/** Says the share covers only part of the results, when some come from days that did not measure repeats. */
export function repeatNote(stats: readonly RepeatCounts[]): string | null {
  const measured = sumOf(stats, 'repeatMeasuredResults');
  const results = sumOf(stats, 'results');
  return measured > 0 && measured < results
    ? `measured days only · ${f.int(measured)} of ${f.int(results)} results` : null;
}

export function Fills() {
  const q = useTranscript('/api/fills', 'fills', { group: 'tool' });
  const t = useTrend('/api/fills', 'fills', { group: 'tool' });
  const meta = useMeta();
  const target = num(meta.data?.rereadTarget ?? 0.15);
  const [picked, setPicked] = useState<string | null>(null);
  const tool = picked !== null && rows(q.data?.tools).some((x) => toolLabel(x.tool) === picked) ? picked : null;
  const family = tool && f.toolFamily(tool);
  const v = useMemo(() => (q.data ? fillsView(q.data, family, t.data) : null), [q.data, family, t.data]);
  const pick = (key: string) => setPicked(key === tool ? null : key);
  const period = q.data ? { from: q.data.period.from, to: q.data.period.to, partial: q.data.period.partial } : null;
  const sel = v?.selected ?? null;
  const slots = v && {
    left: (
      <>
        <KPI
          label={`Tool results · ${v.days === 1 ? 'today' : `${v.days} d`}`}
          value={v.results.value}
          delta={v.results.delta ?? ''}
          trend={v.results.trend}
          trendToday
          tone="gold"
        />
        <KPI label="Context from tools" value={v.context} delta="rest is prompts and replies" tone="default" />
        <KPI label="Largest result" value={v.largest.value} delta={v.largest.delta} tone="warn" />
        <Panel index="04" title="Bash · by command">
          <div className={styles.rowsTight}>
            {keyed(v.bash, (b) => b.name).map(([key, b]) => (
              <ListRow key={key} className={own.lavender} name={b.name} meta={b.meta} value={b.value} fill={b.fill} />
            ))}
          </div>
          {v.bash.length ? null : <p className={`${type_.mono} ${tabs.none}`}>No Bash results</p>}
        </Panel>
      </>
    ),
    centre: (
      <TabPiece tab="fills" placeholder={q.isPlaceholderData}>
        <FillsPiece data={v.piece} selected={tool ?? v.pressedTool} onSelect={pick} />
      </TabPiece>
    ),
    bottom: (
      <Panel index="03" title="Top tool results · weighted size" className={styles.bottomPanel}>
        <div className={styles.tableWrap}>
        <table className={`${type_.monoS} ${styles.table} ${styles.tableLeft} ${own.table}`}>
          <thead>
            <tr>{['Tool', 'Kind', 'Project', 'Result', 'Re-sent', 'Weighted', 'Day'].map((h) => <th key={h}
              scope="col">{h}</th>)}</tr>
          </thead>
          <tbody>
            {keyed(v.top, (r) => r.key).map(([key, r]) => (
              <tr key={key} data-selected={r.match ? '' : undefined}>
                <td>
                  <span
                    className={own.mark}
                    style={{ background: r.match ? 'var(--sh-accent-gold)' : 'var(--sh-data-session)' }}
                    aria-hidden="true"
                  />
                  {r.tool}
                </td>
                <td>{r.kind}</td>
                <td><ClippedName name={projectShort(r.project)} full={r.project} /></td>
                <td>{r.tokens}</td>
                <td>{r.resends}</td>
                <td className={own.weighted}>{r.weighted}</td>
                <td>{r.day}</td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      </Panel>
    ),
    right: (
      <>
        <Panel index="02" title={sel ? `Selected · ${sel.name}` : 'Selected'} className={styles.rightTop}>
          {sel ? (
            <>
              <p className={`${styles.typeName} ${own.median}`} title={sel.name}>{sel.name}</p>
              <p className={`${type_.monoS} ${tabs.muted}`}>{sel.line}</p>
              <dl className={`${styles.stats} ${styles.statsTwo} ${own.statsAbove}`}>
                <div>
                  <dt className={type_.monoS}>avg result</dt>
                  <dd><StatValue text={String(sel.avg)} scope={sel.name} /></dd>
                </div>
                {v.isTool ?(
                  <div>
                    <dt className={type_.monoS}>p95 result</dt>
                    <dd><StatValue text={String(sel.p95)} scope={sel.name} /></dd>
                  </div>
                ) : null}
                <div>
                  <dt className={type_.monoS}>share</dt>
                  <dd><StatValue text={String(sel.share)} scope={sel.name} /></dd>
                </div>
                {sel.costShown ? (
                  <div>
                    <dt className={type_.monoS}>est. cost</dt>
                    <dd className={own.cost}><StatValue text={sel.cost} animate={false} /></dd>
                  </div>
                ) : null}
              </dl>
              {v.isTool ? (
                <>
                  <div className={styles.meterLine}>
                    <span className={`${type_.monoS} ${own.resent}`}>
                      Same result re-read · target {f.pct(target)}
                    </span>
                    {sel.repeat === null ? (
                      <span className={`${type_.monoS} ${styles.meterValue}`}>{sel.repeatGap}</span>
                    ) : (
                      <>
                        <Meter
                          value={sel.repeat}
                          tone={sel.repeat > target ? 'warn' : 'gold'}
                          label={`${sel.name} same result re-read share${sel.repeatNote ? `, ${sel.repeatNote}` : ''}`}
                        />
                        <span
                          className={`${type_.monoS} ${sel.repeat > target ? styles.meterWarn : styles.meterValue}`}
                        >
                          {f.pct(sel.repeat)}
                        </span>
                        {sel.repeatNote ? (
                          <span className={`${type_.monoS} ${styles.meterNote}`}>{sel.repeatNote}</span>
                        ) : null}
                      </>
                    )}
                  </div>
                  <Heading>Result tokens · last {Math.min(RUN_DAYS, sel.bars.length)} d</Heading>
                  <Fit fallback={[264, 56]} height={56}>
                    {(w) => (
                      <Bars
                        data={sel.bars}
                        width={w}
                        height={56}
                        tone="gold"
                        today
                        label={`${sel.name} result tokens per day`}
                      />
                    )}
                  </Fit>
                </>
              ) : null}
            </>
          ) : (
            <p className={`${type_.mono} ${tabs.none}`}>No tool results</p>
          )}
        </Panel>
        <Panel index="05" title={v.filtered ? `By project · ${v.ringName}` : 'By project · from tools'}
          className={styles.rightLow}>
          <div className={styles.rowsTight}>
            {keyed(v.projects, (p) => p.name).map(([key, p]) => <ListRow key={key} name={p.name} meta={p.meta}
              value={p.value} fill={p.fill} />)}
          </div>
          {v.projects.length ? null : <p className={`${type_.mono} ${tabs.none}`}>No projects</p>}
          <p className={`${type_.label} ${tabs.foot} ${own.projectsFoot}`}>
            Labels only · no output, paths or commands
          </p>
        </Panel>
      </>
    ),
  };
  return <TabFrame tab="fills" title="Fills" period={period} slots={slots} empty={v?.empty ?? false} error={q.error}
    fetching={q.isFetching} placeholder={q.isPlaceholderData} emptyText="No tool results in this period" />;
}
