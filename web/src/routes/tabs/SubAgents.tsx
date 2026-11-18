import { useMemo, useState } from 'react';
import type { Schemas } from '../../api';
import { useTranscript, useTrend } from '../../app/queries';
import { Bars, type BarDatum } from '../../charts/Bars';
import { SubagentsPiece } from '../../charts/pieces';
import { SPOKES, type SubagentsInput } from '../../charts/pieces/subagents';
import { KPI, ListRow, Meter, Panel } from '../../components';
import * as f from '../../format/format';
import { agentLabel, modelLabel, num, numOrNull, rows } from '../../format/labels';
import { daysBetween, fillDays, fraction, share } from '../../format/scale';
import { Fit } from '../../components/Fit';
import type_ from '../../styles/type.module.css';
import { Heading, pieceRange, StatValue } from './parts';
import { TabFrame, TabPiece } from './TabFrame';
import tabs from './tabs.module.css';
import styles from './views.module.css';
import own from './SubAgents.module.css';

type Data = Schemas['SubagentsResponse'];

const TABLE_ROWS = 5;
const RUN_DAYS = 14;

/** Keys that do not move when the token ranking does: the label, numbered among equal labels by raw type name. */
function typeKeys(list: Data['types']): Map<Data['types'][number], string> {
  const seen = new Map<string, number>();
  const keys = new Map<Data['types'][number], string>();
  [...list]
    .sort((a, b) => String(a.agentType ?? '').localeCompare(String(b.agentType ?? '')))
    .forEach((t) => {
      const label = agentLabel(t.agentType);
      const n = seen.get(label) ?? 0;
      seen.set(label, n + 1);
      keys.set(t, n === 0 ? label : `${label}#${n}`);
    });
  return keys;
}

export function subagentsView(d: Data, trend: Data = d) {
  const days = num(d.period.days);
  const target = num(d.rereadTarget);
  const keyOf = typeKeys(d.types);
  const types = [...d.types]
    .sort((a, b) => num(b.tokens) - num(a.tokens))
    .map((t, i) => {
      const runs = num(t.runs);
      const reread = share(num(t.cacheReadTokens), num(t.contextTokens));
      return {
        key: keyOf.get(t) ?? `${i}`,
        name: agentLabel(t.agentType),
        model: f.modelShort(modelLabel(t.topModel)),
        runs,
        calls: num(t.calls),
        tokens: num(t.tokens),
        cost: num(t.costUsd),
        first: num(t.avgFirstContextTokens),
        peak: num(t.avgPeakContextTokens),
        perRun: runs > 0 ? num(t.calls) / runs : 0,
        reread,
        over: reread > target,
      };
    });
  const shown = new Set(
    [...types].filter((t) => t.runs > 0).sort((a, b) => b.runs - a.runs).slice(0, SPOKES).map((t) => t.key),
  );
  // The panel's default must be a spoke the piece draws, so it skips types ranked beyond its spokes.
  const defaultKey = (types.find((t) => shown.has(t.key)) ?? types[0])?.key ?? null;
  const firstMax = Math.max(0, ...types.map((t) => t.first));
  const runDays = daysBetween(trend.period.from, trend.period.to).slice(-RUN_DAYS);
  const runsFor = (label: string): BarDatum[] =>
    runDays.map((day) => {
      const same = (r: (typeof trend.daily)[number]) => r.day === day && agentLabel(r.agentType) === label;
      const value = trend.daily.reduce((s, r) => (same(r) ? s + num(r.runs) : s), 0);
      return { key: day, label: f.dayLabel(day), value, display: f.int(value) };
    });
  const emptyScope = (day: string) => ({ day, subShare: 0, subCostUsd: 0 });
  const scopeDaily = fillDays(trend.period.from, trend.period.to, rows(trend.scopeDaily), emptyScope);
  const cost = num(d.costUsd);
  const total = num(d.totalCostUsd);
  return {
    defaultKey,
    empty: types.length === 0 && num(d.runs.value) === 0,
    days,
    target,
    runs: {
      value: f.int(num(d.runs.value)),
      delta: f.deltaCount(num(d.runs.value), numOrNull(d.runs.previous), days, 'runs'),
    },
    scope: {
      value: f.pct(num(d.scopeShare.sub)),
      delta: `main session ${f.pct(num(d.scopeShare.main))}`,
      sub: num(d.scopeShare.sub),
      trend: scopeDaily.map((r) => num(r.subShare)),
    },
    cost: {
      value: f.usd(cost),
      delta: `${f.pct(share(cost, total))} of ${f.usd(total)} total`,
      trend: scopeDaily.map((r) => num(r.subCostUsd)),
    },
    types,
    piece: {
      types: rows(d.types).map((t, i) => ({
        name: agentLabel(t.agentType), runs: num(t.runs), key: keyOf.get(t) ?? `${i}`,
      })),
      mainShare: numOrNull(d.scopeShare.main),
      rangeLabel: pieceRange(days),
    } satisfies SubagentsInput,
    first: types.slice(0, 4).map((t) => ({
      key: t.key,
      name: t.name,
      meta: `avg peak ${f.tokens(t.peak)} · ${f.counted(t.runs, 'run')}`,
      value: f.tokens(t.first),
      fill: fraction(t.first, firstMax),
    })),
    reread: [...types].sort((a, b) => b.reread - a.reread).slice(0, 4),
    trend: daysBetween(trend.period.from, trend.period.to).map((day) =>
      trend.daily.reduce((s, r) => (r.day === day ? s + num(r.runs) : s), 0),
    ),
    runsFor,
    runDays: runDays.length,
  };
}

const markTone = (selected: boolean, over: boolean) =>
  selected ? 'var(--sh-accent-gold)' : over ? 'var(--sh-status-warn)' : 'var(--sh-data-session)';

export function SubAgents() {
  const q = useTranscript('/api/subagents', 'sub-agents');
  const t = useTrend('/api/subagents', 'sub-agents');
  const v = useMemo(() => (q.data ? subagentsView(q.data, t.data) : null), [q.data, t.data]);
  const [picked, setPicked] = useState<string | null>(null);
  const period = q.data ? { from: q.data.period.from, to: q.data.period.to, partial: q.data.period.partial } : null;
  const current = v?.types.find((t) => t.key === picked) ?? v?.types.find((t) => t.key === v.defaultKey) ?? null;
  const runBars = useMemo(() => (v && current ? v.runsFor(current.name) : []), [v, current]);
  const slots = v && {
    left: (
      <>
        <KPI
          label="Sub-agent runs"
          value={v.runs.value}
          delta={v.runs.delta ?? ''}
          trend={v.trend}
          trendToday
          tone="gold"
        />
        <KPI
          label="Share of tokens"
          value={v.scope.value}
          delta={v.scope.delta}
          trend={v.scope.trend}
          trendToday
          tone="live"
        />
        <KPI
          label="Est. cost"
          value={v.cost.value}
          delta={v.cost.delta}
          trend={v.cost.trend}
          trendToday
          tone="default"
        />
        <Panel index="04" title="Context at first call">
          <div className={styles.rowsTight}>
            {v.first.map((t) => (
              <ListRow key={t.key} className={own.lavender} name={t.name} meta={t.meta} value={t.value} fill={t.fill} />
            ))}
          </div>
        </Panel>
      </>
    ),
    centre: (
      <TabPiece tab="sub-agents" placeholder={q.isPlaceholderData}>
        <SubagentsPiece data={v.piece} selected={current?.key ?? null} onSelect={setPicked} />
      </TabPiece>
    ),
    bottom: (
      <Panel
        index="03"
        title={`Agent types · top ${Math.min(TABLE_ROWS, v.types.length)} of ${v.types.length}`}
        className={styles.bottomPanel}
      >
        <div className={styles.tableWrap}>
        <table className={`${type_.monoS} ${styles.table} ${own.table}`}>
          <thead>
            <tr>
              {['Type', 'Runs', 'Calls / run', 'First call', 'Avg peak', 'Re-read', 'Cost'].map((h) => (
                <th key={h} scope="col">{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {v.types.slice(0, TABLE_ROWS).map((t) => (
              <tr key={t.key} data-selected={t.key === current?.key ? '' : undefined}>
                <td>
                  <span
                    className={own.mark}
                    style={{ background: markTone(t.key === current?.key, t.over) }}
                    aria-hidden="true"
                  />
                  <button
                    type="button"
                    className={styles.rowButton}
                    aria-pressed={t.key === current?.key}
                    onClick={() => setPicked(t.key)}
                  >
                    {t.name}
                  </button>
                </td>
                <td>{f.int(t.runs)}</td>
                <td>{t.perRun.toFixed(1)}</td>
                <td>{f.tokens(t.first)}</td>
                <td>{f.tokens(t.peak)}</td>
                <td className={t.over ? styles.over : undefined}>{f.pct(t.reread)}</td>
                <td>{f.usd(t.cost)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      </Panel>
    ),
    right: (
      <>
        <Panel index="02" title="Selected type" className={styles.rightTop}>
          {current ? (
            <>
              <p className={styles.typeName} title={current.name}>{current.name}</p>
              <p className={`${type_.monoS} ${tabs.muted}`}>
                {current.model} · {f.counted(current.runs, 'run')} · {v.days} d
              </p>
              <dl className={`${styles.stats} ${styles.statsTwo} ${own.statsAbove}`}>
                <div>
                  <dt className={type_.monoS}>calls / run</dt>
                  <dd><StatValue text={current.perRun.toFixed(1)} scope={current.key} /></dd>
                </div>
                <div>
                  <dt className={type_.monoS}>first call</dt>
                  <dd><StatValue text={f.tokens(current.first)} scope={current.key} /></dd>
                </div>
                <div>
                  <dt className={type_.monoS}>avg peak</dt>
                  <dd><StatValue text={f.tokens(current.peak)} scope={current.key} /></dd>
                </div>
                <div className={own.cost}>
                  <dt className={type_.monoS}>est. cost</dt>
                  <dd><StatValue text={f.usd(current.cost)} scope={current.key} /></dd>
                </div>
              </dl>
              <div className={own.reread}>
                <span className={`${type_.monoS} ${own.rereadLabel}`}>Re-read</span>
                <span className={`${type_.monoS} ${own.rereadValue}`}>
                  <span className={current.over ? styles.meterWarn : styles.meterValue}>{f.pct(current.reread)}</span>
                  {' · '}<span className={own.target}>target {f.pct(v.target)}</span>
                </span>
                <div className={own.rereadBar}>
                  <Meter value={current.reread} tone="live" label={`${current.name} re-read share`} />
                  <span
                    className={own.targetTick}
                    style={{ left: `${Math.min(1, Math.max(0, v.target)) * 100}%` }}
                    aria-hidden="true"
                  />
                </div>
              </div>
              <Heading>Runs · last {v.runDays} d</Heading>
              <Fit fallback={[264, 56]} height={56}>
                {(w) => (
                  <Bars
                    data={runBars}
                    width={w}
                    height={56}
                    tone="gold"
                    today
                    label={`${current.name} runs per day`}
                  />
                )}
              </Fit>
            </>
          ) : (
            <p className={`${type_.mono} ${tabs.none}`}>No agent types</p>
          )}
        </Panel>
        <Panel index="05" title="Re-read by type" className={styles.rightLow}>
          <div className={styles.rows}>
            {v.reread.map((t) => (
              <ListRow
                key={t.key}
                name={t.name}
                meta={`${f.counted(t.runs, 'run')} · ${t.perRun.toFixed(1)} calls each`}
                value={f.pct(t.reread)}
                fill={t.reread}
                tone={t.over ? 'warn' : 'normal'}
              />
            ))}
          </div>
          {v.reread.length ? (
            <p className={`${type_.monoS} ${own.foot}`}>
              Target {f.pct(v.target)}{v.reread.every((t) => t.over) ? ' · every type is above it' : ''}
            </p>
          ) : null}
        </Panel>
      </>
    ),
  };
  return (
    <TabFrame
      tab="sub-agents"
      title="Sub-agents"
      period={period}
      slots={slots}
      empty={v?.empty ?? false}
      error={q.error}
      fetching={q.isFetching} placeholder={q.isPlaceholderData}
      emptyText="No sub-agent runs in this period"
    />
  );
}
