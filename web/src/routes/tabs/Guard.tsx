import { useMemo } from 'react';
import type { Schemas } from '../../api';
import { useTranscript, useTrend } from '../../app/queries';
import { Bars, type BarDatum } from '../../charts/Bars';
import { GuardPiece } from '../../charts/pieces';
import { guardPieceFields, type GuardInput } from '../../charts/pieces/guard';
import { KPI, ListRow, Panel } from '../../components';
import * as f from '../../format/format';
import { num, rows as list, ruleLabel, toolLabel } from '../../format/labels';
import { daysBetween, fraction, share } from '../../format/scale';
import { Fit } from '../../components/Fit';
import type_ from '../../styles/type.module.css';
import { TabFrame, TabPiece } from './TabFrame';
import tabs from './tabs.module.css';
import styles from './views.module.css';
import own from './Guard.module.css';
import { keyed } from '../../app/keys';

type Data = Schemas['GuardResponse'];

const ERROR_ROWS = 3;
const RULE_ROWS = 10;
const BLOCKED_CLASS: Schemas['ErrorClass'] = 'blocked by hook';
const NA = 'n/a';
const NO_DATA = 'no data';
const DASH = '—';
const LOG_NOTES: Record<Data['log']['state'], string | null> = {
  ok: null,
  starting: 'Reading the guard log…',
  missing: 'The guard log files are gone',
  absent: 'The guard writes no per-call log yet',
  unreadable: 'The guard log could not be read',
  disabled: 'No guard log collector is running',
};
const UNKNOWN_NOTE = 'The guard log state is not known';
const logNote = (state: string | undefined) =>
  (state !== undefined && Object.hasOwn(LOG_NOTES, state) ? LOG_NOTES[state as Data['log']['state']] : UNKNOWN_NOTE);
const TOOLS: Record<Schemas['GuardTool'], string> = { bash: 'Bash', file: 'File' };
const ms = (v: number | null) => (v === null ? DASH : f.ms(num(v)));

export function guardView(d: Data, now: Date, trend: Data = d) {
  const days = num(d.period.days);
  const when = days === 1 ? 'today' : `${days} d`;
  const trendDays = daysBetween(trend.period.from, trend.period.to);
  const trendByDay = new Map(list(trend.callsByDay).map((r) => [r.day, r]));
  const bars: BarDatum[] = trendDays.map((day) => {
    const row = trendByDay.get(day);
    const label = f.dayLabel(day);
    return row
      ? {
        key: day, label, value: num(row.calls), cap: num(row.deny),
        display: `${f.int(num(row.calls))} calls · ${f.int(num(row.deny))} denied`,
      }
      : { key: day, label, value: 0, display: NO_DATA, absent: true };
  });
  const hasLog = d.decisions !== null;
  const note = logNote(d.log?.state);
  const trendNote = list(trend.callsByDay).length ? null : logNote(trend.log?.state) ?? NO_DATA;
  const dayRows = list(d.trend);
  const toolErrors = list(d.toolErrors);
  const results = dayRows.reduce((s, r) => s + num(r.results), 0);
  const blockedResults = dayRows.reduce((s, r) => s + num(r.blocked), 0);
  const totalErrors = toolErrors.reduce((s, e) => s + num(e.errors), 0);
  const guardBlocked = toolErrors.filter((e) => e.errorClass === BLOCKED_CLASS).reduce((s, e) => s + num(e.errors), 0);
  const seen = new Set<string>();
  const families = new Map<string, { errors: number; results: number; classes: Map<string, number> }>();
  for (const e of toolErrors) {
    const tool = toolLabel(e.tool);
    const name = f.toolFamily(tool);
    const row = families.get(name) ?? { errors: 0, results: 0, classes: new Map() };
    row.errors += num(e.errors);
    if (!seen.has(tool)) {
      seen.add(tool);
      row.results += num(e.results);
    }
    row.classes.set(e.errorClass, (row.classes.get(e.errorClass) ?? 0) + num(e.errors));
    families.set(name, row);
  }
  const errors = [...families.entries()]
    .map(([name, r]) => {
      const top = [...r.classes.entries()].sort((a, b) => b[1] - a[1])[0];
      const kind = top?.[0] ?? 'other';
      return { name, errors: r.errors, results: r.results, kind, rate: share(r.errors, r.results) };
    })
    .filter((r) => r.errors > 0)
    .sort((a, b) => b.errors - a.errors)
    .slice(0, ERROR_ROWS);
  const errorMax = Math.max(0, ...errors.map((e) => e.rate));
  const dec = d.decisions;
  const blocked = {
    value: f.int(blockedResults),
    share: results > 0 ? f.pct1(share(blockedResults, results)) : NA,
    of: results > 0 ? `${f.int(blockedResults)} of ${f.int(results)} results` : 'no results in the period',
  };
  const total = dec ? dec.allow + dec.ask + dec.deny : 0;
  const decisions = dec
    ? ([['Allow', dec.allow], ['Ask', dec.ask], ['Deny', dec.deny], ['Fail closed', dec.failClosed]] as const)
      .map(([name, n]) => ({
        name, value: f.int(num(n)), meta: total > 0 ? f.pct1(share(num(n), total)) : DASH,
        fill: fraction(num(n), total), warn: name === 'Deny' && n > 0,
      }))
    : [];
  const speed = list(d.byTool).map((t) => ({
    tool: TOOLS[t.tool], p50: ms(t.p50Ms), p95: ms(t.p95Ms), p99: ms(t.p99Ms),
  }));
  const rules = list(d.rules).slice(0, RULE_ROWS);
  const ruleMax = Math.max(0, ...rules.map((r) => num(r.calls)));
  return {
    empty: d.install?.source === 'none' && toolErrors.length === 0 && dayRows.length === 0 && !hasLog
      && d.log?.state === 'absent',
    days,
    when,
    note,
    trendNote,
    calls: {
      value: hasLog ? f.int(num(d.calls.value)) : DASH,
      delta: (hasLog ? f.deltaPct(num(d.calls.value), d.calls.previous, days) ?? 'no previous period' : note) ?? '',
    },
    blocked,
    blockedKpi: dec
      ? {
        label: `Blocked · ${when}`,
        value: f.int(num(dec.deny)),
        delta: `${num(d.calls.value) > 0 ? f.pct1(share(num(dec.deny), num(d.calls.value))) : NA} of calls · ${
          f.int(num(dec.ask))} more asked`,
      }
      : { label: `Blocked results · ${when}`, value: blocked.value, delta: `${blocked.of} · ${blocked.share}` },
    toolErrors: {
      value: f.int(totalErrors),
      delta: `${f.int(guardBlocked)} blocked by guard · ${f.int(totalErrors - guardBlocked)} other`,
    },
    decisions,
    decisionsFoot: dec ? `Time cap ${f.int(num(dec.timeCap))} · sub-agent deny ${f.int(num(dec.subDeny))}` : null,
    speed,
    rules: rules.map((r) => ({
      name: ruleLabel(r.rule), meta: `${r.tool} · ${r.decision}`, value: f.int(num(r.calls)),
      fill: fraction(num(r.calls), ruleMax), warn: r.decision === 'deny',
    })),
    piece: guardPieceFields(d, now) satisfies GuardInput,
    bars,
    barAxis: {
      start: f.dayLabel(trend.period.from),
      end: trend.period.to === f.localDay(now) ? 'Today' : f.dayLabel(trend.period.to),
      now: trend.period.to === f.localDay(now),
    },
    errors: errors.map((e) => ({
      name: e.name,
      meta: `${e.kind} · ${f.tokens(e.errors)} of ${f.tokens(e.results)}`,
      value: f.pct1(e.rate),
      fill: fraction(e.rate, errorMax),
      top: errorMax > 0 && e.rate === errorMax,
    })),
  };
}

export function Guard() {
  const q = useTranscript('/api/guard', 'guard');
  const t = useTrend('/api/guard', 'guard');
  const v = useMemo(() => (q.data ? guardView(q.data, new Date(), t.data) : null), [q.data, t.data]);
  const period = q.data ? { from: q.data.period.from, to: q.data.period.to, partial: q.data.period.partial } : null;
  const slots = v && {
    left: (
      <>
        <KPI label={`Guard calls · ${v.when}`} value={v.calls.value} delta={v.calls.delta} tone="live" />
        <KPI label={v.blockedKpi.label} value={v.blockedKpi.value} delta={v.blockedKpi.delta} tone="gold" />
        <KPI
          label={`Tool errors · ${v.when}`}
          value={v.toolErrors.value}
          delta={v.toolErrors.delta}
          tone="gold"
        />
        <Panel index="04" title="Decisions" className={own.decisions}>
          {v.decisions.length ? (
            <div className={`${styles.rowsTight} ${own.rows}`}>
              {v.decisions.map((r) => (
                <ListRow key={r.name} name={r.name} meta={r.meta} value={r.value} fill={r.fill}
                  tone={r.warn ? 'warn' : 'normal'} />
              ))}
            </div>
          ) : <p className={`${type_.mono} ${tabs.none}`}>{v.note ?? NO_DATA}</p>}
          {v.decisionsFoot ? <p className={`${type_.label} ${tabs.foot} ${own.foot}`}>{v.decisionsFoot}</p> : null}
        </Panel>
      </>
    ),
    centre: (
      <TabPiece tab="guard" placeholder={q.isPlaceholderData}>
        <GuardPiece data={v.piece} />
      </TabPiece>
    ),
    bottom: (
      <Panel index="03" title="Calls per day · last 14 d" className={styles.bottomPanel}>
        {v.trendNote ? <p className={`${type_.mono} ${tabs.none}`}>{v.trendNote}</p> : (
          <Fit fallback={[644, 76]} height={76}>
            {(w) => (
              <Bars
                data={v.bars}
                width={w}
                height={76}
                tone="gold"
                today
                axis={v.barAxis}
                label="Guard calls per day, denied calls in coral; days without log rows have no data"
              />
            )}
          </Fit>
        )}
      </Panel>
    ),
    right: (
      <>
        <Panel index="02" title="Decision time · per tool" className={`${styles.rightTop} ${own.speed}`}>
          {v.speed.length ? (
            <table className={styles.table}>
              <thead>
                <tr><th scope="col">Tool</th><th scope="col">P50</th><th scope="col">P95</th><th scope="col">P99</th></tr>
              </thead>
              <tbody>
                {v.speed.map((r) => (
                  <tr key={r.tool}>
                    <td>{r.tool}</td><td>{r.p50}</td><td>{r.p95}</td><td>{r.p99}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : <p className={`${type_.mono} ${tabs.none}`}>{v.note ?? NO_DATA}</p>}
        </Panel>
        <Panel index="05" title="Blocked by rule" className={`${styles.rightLow} ${own.rules} ${own.bounded}`}
          bodyClassName={own.rulesBody}>
          <div
            className={`${styles.rowsTight} ${own.rows} ${own.scroll}`}
            {...(v.rules.length ? { tabIndex: 0, role: 'region', 'aria-label': 'Blocked by rule rows' } : {})}
          >
            {keyed(v.rules, (r) => `${r.name}|${r.meta}`).map(([key, r]) => (
              <ListRow key={key} name={r.name} meta={r.meta} value={r.value} fill={r.fill}
                tone={r.warn ? 'warn' : 'normal'} />
            ))}
          </div>
          {v.rules.length ? null : <p className={`${type_.mono} ${tabs.none}`}>{v.note ?? 'No asks or denies'}</p>}
        </Panel>
        <Panel index="06" title="Tool errors by tool" className={`${styles.rightLow} ${own.errors}`}>
          <div className={`${styles.rowsTight} ${own.rows}`}>
            {keyed(v.errors, (e) => e.name).map(([key, e]) => (
              <ListRow
                key={key}
                name={e.name}
                meta={e.meta}
                value={e.value}
                fill={e.fill}
                tone={e.top ? 'warn' : 'normal'}
              />
            ))}
          </div>
          {v.errors.length ? null : <p className={`${type_.mono} ${tabs.none}`}>No tool errors</p>}
          <p className={`${type_.label} ${tabs.foot} ${own.foot}`}>
            Error kind only · never the output
          </p>
        </Panel>
      </>
    ),
  };
  return (
    <TabFrame
      tab="guard"
      title="Guard"
      period={period}
      slots={slots}
      empty={v?.empty ?? false}
      error={q.error}
      fetching={q.isFetching} placeholder={q.isPlaceholderData}
      emptyText="Guard report not available yet"
    />
  );
}
