import { describe, expect, it } from 'vitest';
import type { Schemas } from '../../api';
import { guardPieceFields } from '../../charts/pieces/guard';
import { guardView } from './Guard';
import { fixtures } from './testData';

const NOW = new Date(2026, 10, 7, 12);
const base = fixtures(NOW.getTime()).guard;
type Data = Schemas['GuardResponse'];
const withData = (patch: Record<string, unknown>) => ({ ...base, ...patch }) as unknown as Data;
const err = (tool: string, errorClass: string, errors: number, results: number) =>
  ({ tool, errorClass, errors, results });

describe('guardView', () => {
  it('groups tools into families and ranks them by errors', () => {
    const d = withData({
      toolErrors: [
        err('Bash: git', 'other', 3, 30),
        err('Bash: npm', 'blocked by hook', 5, 20),
        err('Read: whole', 'other', 1, 50),
      ],
    });
    const v = guardView(d, NOW);
    expect(v.errors.map((e) => e.name)).toEqual(['Bash', 'Read']);
    expect(v.errors[0]).toMatchObject({
      meta: 'blocked by hook: 5 of 8 errors', metaTitle: 'blocked by hook: 5 of 8 errors · 50 results', top: true,
    });
  });

  it('counts a tool results once when it has several error classes', () => {
    const d = withData({
      toolErrors: [err('Bash: git', 'other', 2, 40), err('Bash: git', 'blocked by hook', 1, 40)],
    });
    const row = guardView(d, NOW).errors[0];
    expect(row?.meta).toBe('other: 2 of 3 errors');
    expect(row?.metaTitle).toBe('other: 2 of 3 errors · 40 results');
  });

  it('names a single error class once, without repeating its count, and uses the singular for 1', () => {
    const row = (errors: number, results: number) =>
      guardView(withData({ toolErrors: [err('Grep', 'other', errors, results)] }), NOW).errors[0];
    expect(row(4, 40)).toMatchObject({ meta: 'other: all 4 errors', metaTitle: 'other: all 4 errors · 40 results' });
    expect(row(1, 1)).toMatchObject({ meta: 'other: 1 error', metaTitle: 'other: 1 error · 1 result' });
  });

  it('writes both counts in full when a class is a part of the errors but both round to the same text', () => {
    const d = withData({
      toolErrors: [err('Bash: a', 'other', 1_000, 5_000), err('Bash: b', 'blocked by hook', 40, 5_000)],
    });
    expect(guardView(d, NOW).errors[0]?.meta).toBe('other: 1,000 of 1,040 errors');
    const close = withData({ toolErrors: [err('Bash: a', 'other', 1_200, 9_000), err('Bash: b', 'x', 1, 9_000)] });
    expect(guardView(close, NOW).errors[0]?.meta).toBe('other: 1,200 of 1,201 errors');
  });

  it('lets the class seen first win when two classes have equal counts', () => {
    const order = (a: string, b: string) =>
      guardView(withData({ toolErrors: [err('Bash: a', a, 2, 10), err('Bash: b', b, 2, 10)] }), NOW).errors[0]?.meta;
    expect(order('zeta', 'alpha')).toBe('zeta: 2 of 4 errors');
    expect(order('alpha', 'zeta')).toBe('alpha: 2 of 4 errors');
  });

  it('takes the fallback bars from the trend response and the blocked KPI from the period response', () => {
    const period = withData({
      decisions: null, callsByDay: [], period: { ...base.period, days: 1 },
      trend: [{ day: '2026-11-07', results: 40, blocked: 4 }],
    });
    const wide = withData({
      decisions: null, callsByDay: [], period: { ...base.period, from: '2026-10-25', to: '2026-11-07', days: 14 },
      trend: [{ day: '2026-11-03', results: 100, blocked: 9 }],
    });
    const v = guardView(period, NOW, wide);
    expect(v.bars.find((b) => b.key === '2026-11-03')).toMatchObject({ value: 9, display: '9 blocked of 100 results' });
    expect(v.bars.find((b) => b.key === '2026-11-07')?.absent).toBe(true);
    expect(v.blockedKpi).toEqual({ label: 'Blocked results · today', value: '4', delta: '4 of 40 results · 10.0%' });
  });

  it('draws blocked results per day from the transcripts when there is no guard log', () => {
    const trend = [{ day: '2026-11-05', results: 200, blocked: 0 }, { day: '2026-11-07', results: 300, blocked: 6 }];
    const v = guardView(withData({ decisions: null, callsByDay: [], trend }), NOW);
    expect(v.trendNote).toBeNull();
    expect(v.barTitle).toBe('Blocked results per day · last 14 d');
    expect(v.barLabel).toContain('Blocked tool results per day');
    const bar = (day: string) => v.bars.find((b) => b.key === day);
    expect(bar('2026-11-07')).toMatchObject({ value: 6, display: '6 blocked of 300 results' });
    expect(bar('2026-11-05')).toMatchObject({ value: 0, display: '0 blocked of 200 results' });
    expect(bar('2026-11-05')?.absent).toBeFalsy();
    expect(bar('2026-11-06')).toMatchObject({ value: 0, display: 'no data', absent: true });
  });

  it('keeps the calls bars when the log has rows, whatever the transcripts hold', () => {
    const trend = [{ day: '2026-11-07', results: 300, blocked: 250 }];
    for (const decisions of [base.decisions, null]) {
      const v = guardView(withData({ trend, decisions }), NOW);
      expect(v.barTitle).toBe('Calls per day · last 14 d');
      expect(v.barLabel).toBe('Guard calls per day, denied calls in coral; days without log rows have no data');
      expect(v.bars.find((b) => b.key === '2026-11-07')).toMatchObject({ value: 140, cap: 0 });
    }
  });

  it('keeps the note, not transcript bars, when the log has decisions but no day rows', () => {
    const v = guardView(withData({ callsByDay: [] }), NOW);
    expect(v.barTitle).toBe('Calls per day · last 14 d');
    expect(v.bars.every((b) => b.absent)).toBe(true);
    expect(v.trendNote).toBe('no data');
  });

  it('shows an absent trend day as no data, not as a measured zero', () => {
    const callsByDay = [{ day: '2026-11-06', calls: 0, ask: 0, deny: 0 }, { day: '2026-11-07', calls: 20, ask: 1, deny: 5 }];
    const v = guardView(withData({ callsByDay }), NOW);
    expect(v.bars).toHaveLength(7);
    expect(v.bars.find((b) => b.key === '2026-11-05')).toMatchObject({ value: 0, display: 'no data', absent: true });
    expect(v.bars.find((b) => b.key === '2026-11-06')).toMatchObject({ value: 0, cap: 0, display: '0 calls · 0 denied' });
    expect(v.bars.find((b) => b.key === '2026-11-07')).toMatchObject({ value: 20, cap: 5, display: '20 calls · 5 denied' });
  });

  it.each([
    ['system', 'go', 'unknown', true], ['user', 'unknown', 'node', true], ['user', 'unknown', 'unknown', null],
    ['system', 'unknown', 'unknown', null], ['none', 'unknown', 'unknown', false], ['none', 'go', 'go', false],
  ])('reads the %s install with modes %s/%s as active %s', (source, bash, file, active) => {
    const v = guardView(withData({ install: { ...base.install, source, bash, file } }), NOW);
    expect(v.piece.active).toBe(active);
  });

  it('reads the piece blocked count from the log denies, else from the transcript trend', () => {
    const trend = [{ day: '2026-11-06', results: 10, blocked: 3 }, { day: '2026-11-07', results: 20, blocked: 5 }];
    expect(guardView(withData({ trend, decisions: null }), NOW).piece.blocked).toBe(8);
    expect(guardView(withData({ trend }), NOW).piece.blocked).toBe(10);
  });

  it('gives no-data for the piece blocked count when the trend is an empty array', () => {
    expect(guardView(withData({ trend: [], decisions: null }), NOW).piece.blocked).toBeNull();
  });

  it('gives no-data for the piece blocked count when the trend is missing', () => {
    expect(guardPieceFields({ decisions: null }, NOW).blocked).toBeNull();
  });

  it('keeps a zero deny count from the log as a measured zero', () => {
    expect(guardView(withData({ decisions: { ...base.decisions, deny: 0 } }), NOW).piece.blocked).toBe(0);
  });

  it('leaves failClosed out of the decision shares, it overlaps allow, ask and deny', () => {
    const v = guardView(withData({ decisions: { allow: 80, ask: 10, deny: 10, failClosed: 50, timeCap: 0, subDeny: 0 } }), NOW);
    expect(v.decisions.map((r) => r.meta)).toEqual(['80.0%', '10.0%', '10.0%', '50.0%']);
  });

  it.each([
    ['starting', 'Reading the log…'], ['missing', 'Log missing · stored figures'],
    ['absent', 'Log absent · no stored figures'], ['unreadable', 'Log unreadable'], ['disabled', 'Collector off'],
  ])('words the %s log state as "%s" in the warning tone', (state, text) => {
    const v = guardView(withData({ log: { state, skipped: base.log.skipped } }), NOW);
    expect(v.piece.log).toEqual({ text, skipped: null, warn: true });
    expect(v.note).not.toBeNull();
  });

  it('shows only the state, code, read time and non-zero skipped counts in the log status', () => {
    const log = { state: 'unreadable', code: 'bad <code> with text', lastReadAt: '2026-11-07T09:05:00',
      skipped: { malformed: 0, oversized: 3, badVersion: 0, badRule: 0, notRegular: 0 } };
    expect(guardView(withData({ log }), NOW).piece.log).toEqual({
      text: 'Log unreadable · read Today 09:05', skipped: 'Skipped 3 oversized', warn: true,
    });
  });

  it('labels a read from an earlier day with its day', () => {
    const log = { state: 'ok', lastReadAt: '2026-11-04T09:05:00', skipped: base.log.skipped };
    expect(guardView(withData({ log }), NOW).piece.log?.text).toMatch(/^Log ok · read \S+ .*\d\d:\d\d$/);
    expect(guardView(withData({ log }), NOW).piece.log?.text).not.toContain('Today');
  });

  it('words an unlisted log state with fixed text and a fixed note, never the raw state', () => {
    for (const state of ['future-state', 'constructor', '__proto__']) {
      const v = guardView(withData({ log: { state, skipped: base.log.skipped } }), NOW);
      expect(v.piece.log).toEqual({ text: 'Log state unknown', skipped: null, warn: true });
      expect(v.note).toBe('The guard log state is not known');
    }
  });

  it('reads a body without trend, toolErrors or callsByDay as empty lists', () => {
    const v = guardView(withData({ trend: undefined, toolErrors: null, callsByDay: undefined }), NOW);
    expect(v.errors).toEqual([]);
    expect(v.blocked.of).toBe('no results in the period');
    expect(v.toolErrors.value).toBe('0');
  });

  it('draws the 14-day bars from the trend response and the KPIs from the period response', () => {
    const today = [{ day: '2026-11-07', results: 40, blocked: 4 }];
    const period = withData({ period: { ...base.period, days: 1 }, trend: today });
    const wide = withData({
      period: { ...base.period, from: '2026-10-25', to: '2026-11-07', days: 14 },
      callsByDay: [{ day: '2026-10-26', calls: 5, ask: 0, deny: 2 }, { day: '2026-11-07', calls: 9, ask: 0, deny: 1 }],
    });
    const v = guardView(period, NOW, wide);
    expect(v.bars).toHaveLength(14);
    expect(v.bars.filter((b) => !b.absent).map((b) => b.value)).toEqual([5, 9]);
    expect(v.bars.filter((b) => b.absent)).toHaveLength(12);
    expect(v.blocked).toMatchObject({ value: '4', of: '4 of 40 results' });
    expect(v.piece.blocked).toBe(10);
    expect(v.barAxis.start).toBe(guardView(wide, NOW).barAxis.start);
  });

  it('shares blocked over results and reads n/a with no results', () => {
    const v = guardView(withData({ trend: [{ day: '2026-11-07', results: 40, blocked: 10 }] }), NOW);
    expect(v.blocked).toMatchObject({ value: '10', share: '25.0%', of: '10 of 40 results' });
    const none = guardView(withData({ trend: [{ day: '2026-11-07', results: 0, blocked: 0 }] }), NOW);
    expect(none.blocked).toMatchObject({ value: '0', share: 'n/a' });
    expect(guardView(withData({ trend: [] }), NOW).blocked.share).toBe('n/a');
  });

  it('shows the log denies as the Blocked card, and the transcript fallback when there is no log', () => {
    const v = guardView(base, NOW);
    expect(v.blockedKpi).toEqual({ label: 'Blocked · 7 d', value: '10', delta: '1.2% of calls · 30 more asked' });
    const trend = [{ day: '2026-11-07', results: 40, blocked: 4 }];
    const none = guardView(withData({ decisions: null, trend }), NOW);
    expect(none.blockedKpi).toEqual({ label: 'Blocked results · 7 d', value: '4', delta: '4 of 40 results · 10.0%' });
  });

  it('puts the install source and date in the mode line, nothing for no install', () => {
    const mode = (install: Record<string, unknown>) =>
      guardView(withData({ install: { ...base.install, ...install } }), NOW).piece.mode;
    expect(mode({ source: 'system', bash: 'go', file: 'go', installedAt: undefined })).toBe('bash=go · file=go · system install');
    expect(mode({ source: 'user', bash: 'go', file: 'go', installedAt: '2026-11-01T10:00:00' })).toBe(
      'bash=go · file=go · user install · installed 01 Nov',
    );
    expect(mode({ source: 'none' })).toBeNull();
  });

  it('fills every log panel from a populated response', () => {
    const v = guardView(base, NOW);
    expect(v.calls).toEqual({ value: '840', delta: '▲ 20.0% vs prev 7 d' });
    expect(v.decisions.map((r) => [r.name, r.value])).toEqual([['Allow', '800'], ['Ask', '30'], ['Deny', '10'], ['Fail closed', '2']]);
    expect(v.decisionsFoot).toBe('Time cap 1 · sub-agent deny 3');
    expect(v.speed).toEqual([
      { tool: 'Bash', p50: '3.0 ms', p95: '9.0 ms', p99: '20 ms' },
      { tool: 'File', p50: '2.0 ms', p95: '6.0 ms', p99: '—' },
    ]);
    expect(v.rules[0]).toMatchObject({ name: 'git-force-push', meta: 'bash · deny', value: '6', warn: true });
    expect(v.rules[1]).toMatchObject({ meta: 'file · ask', warn: false });
    expect(v.piece).toMatchObject({ bash: 600, file: 240, subShare: 0.25, blocked: 10 });
    expect(v.piece.lastBlock).toContain('bash · git-force-push');
    expect(v.note).toBeNull();
  });

  it('colours an hour with a deny and keeps quiet hours as calls', () => {
    const hours = guardView(base, NOW).piece.hours ?? [];
    expect(hours).toHaveLength(24);
    expect(hours.filter((h) => h.blocked > 0).map((h) => h.hour)).toEqual([9]);
  });

  it('says the guard writes no log yet, never zeros, when the log is absent', () => {
    const empty = {
      log: { state: 'absent', skipped: base.log.skipped }, calls: { value: 0, previous: null }, decisions: null,
      byTool: [], subAgent: null, callsByDay: [], hours: null, rules: [], lastBlock: null,
    };
    const v = guardView(withData(empty), NOW);
    expect(v.note).toBe('The guard writes no per-call log yet');
    expect(v.calls).toEqual({ value: '—', delta: v.note });
    const neither = guardView(withData({ ...empty, trend: [] }), NOW);
    expect(neither.trendNote).toBe(v.note);
    expect(neither.barTitle).toBe('Calls per day · last 14 d');
    expect(neither.bars.every((b) => b.absent)).toBe(true);
    expect(v.decisions).toEqual([]);
    expect(v.speed).toEqual([]);
    expect(v.rules).toEqual([]);
    expect(v.piece).toMatchObject({ hours: null, bash: null, file: null, subShare: null, lastBlock: null, blocked: 10 });
    expect(guardView(withData({ ...empty, trend: [] }), NOW).piece.blocked).toBeNull();
  });

  it('says the log could not be read when it is unreadable', () => {
    const v = guardView(withData({
      log: { state: 'unreadable', skipped: base.log.skipped }, decisions: null, byTool: [], callsByDay: [], hours: null,
      rules: [], trend: [],
    }), NOW);
    expect(v.note).toBe('The guard log could not be read');
    expect(v.trendNote).toBe(v.note);
    expect(v.calls.value).toBe('—');
  });

  it('shows a null percentile as a dash', () => {
    const byTool = [{ tool: 'bash', calls: 3, ask: 0, deny: 0, p50Ms: null, p95Ms: null, p99Ms: null }];
    expect(guardView(withData({ byTool }), NOW).speed).toEqual([{ tool: 'Bash', p50: '—', p95: '—', p99: '—' }]);
  });

  it('keeps a rule label that is not a label as text "other", never markup', () => {
    const rules = [{ rule: '<img src=x onerror=1>', tool: 'bash', decision: 'deny', calls: 1 }];
    expect(guardView(withData({ rules }), NOW).rules[0]?.name).toBe('other');
  });

  it('labels a longer range by its days', () => {
    const v = guardView(withData({ period: { ...base.period, days: 7 } }), NOW);
    expect(v.when).toBe('7 d');
    expect(v.barAxis).not.toHaveProperty('note');
  });
});
