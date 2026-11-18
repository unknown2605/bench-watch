import { describe, expect, it } from 'vitest';
import * as f from './format';
import { agentLabel, modelLabel, num, numOrNull, projectLabel, sessionLabel } from './labels';
import { clamp01, daysBetween, fillDays, fraction, maxOf, peakIndex, segments, share, timeFraction } from './scale';

describe('number formats', () => {
  it('formats token counts as the design does', () => {
    expect([0, 999, 9_600, 10_000, 171_200, 999_499, 999_500, 1_284_000, 14_900_000_000].map(f.tokens)).toEqual([
      '0', '999', '9.6k', '10k', '171k', '999k', '1.00M', '1.28M', '14.9B',
    ]);
    expect(f.tokens(1_000)).toBe('1k');
    expect(f.tokens(-2_500)).toBe('-2.5k');
    expect(f.tokens(Number.NaN)).toBe('0');
  });

  it('formats cost with cents below $1,000 only', () => {
    expect(f.usd(0)).toBe('$0.00');
    expect(f.usd(12.346)).toBe('$12.35');
    expect(f.usd(999.994)).toBe('$999.99');
    expect(f.usd(1234.6)).toBe('$1,235');
    expect(f.usd(Number.POSITIVE_INFINITY)).toBe('$0.00');
  });

  it('formats shares and counts', () => {
    expect(f.pct(0)).toBe('0%');
    expect(f.pct(0.004)).toBe('<1%');
    expect(f.pct(0.005)).toBe('1%');
    expect(f.pct(0.956)).toBe('96%');
    expect(f.pct1(0.1234)).toBe('12.3%');
    expect(f.int(1234567.4)).toBe('1,234,567');
  });

  it('describes changes against the previous period', () => {
    expect(f.deltaPct(112.4, 100, 30)).toBe('▲ 12.4% vs prev 30 d');
    expect(f.deltaPct(80, 100, 7)).toBe('▼ 20.0% vs prev 7 d');
    expect(f.deltaPct(5, 0, 7)).toBe('▲ new vs prev 7 d');
    expect(f.deltaPct(0, 0, 7)).toBe('■ 0% vs prev 7 d');
    expect(f.deltaPct(5, null, 7)).toBeNull();
    expect(f.deltaPts(0.96, 0.9)).toBe('▲ 6.0 pts');
    expect(f.deltaPts(0.5, 0.5)).toBe('■ 0.0 pts');
    expect(f.deltaPts(0.5, null)).toBeNull();
    expect(f.deltaCount(40, 52, 14, 'runs')).toBe('▼ 12 runs vs prev 14 d');
    expect(f.deltaCount(1, null, 14, 'runs')).toBeNull();
  });
});

describe('dates and times', () => {
  it('reads calendar days without a time zone shift', () => {
    expect(f.parseDay('2026-11-07')).toEqual({ y: 2026, m: 11, d: 7 });
    expect(f.parseDay('2026-11-7')).toBeNull();
    expect(f.dayLabel('2026-11-06')).toBe('06 Nov');
    expect(f.dayLabel('nope')).toBe('');
    expect(f.periodLabel('2026-10-08', '2026-11-07')).toBe('08 Oct → 07 Nov');
    expect(f.periodLabel('2026-11-07', '2026-11-07')).toBe('07 Nov');
  });

  it('labels instants in local time', () => {
    const now = new Date(2026, 10, 9, 15, 0);
    expect(f.clock(new Date(2026, 10, 9, 10, 41).toISOString())).toBe('10:41');
    expect(f.clock('garbage')).toBe('');
    expect(f.localDay(now)).toBe('2026-11-09');
    expect(f.whenLabel(new Date(2026, 10, 9, 10, 41).toISOString(), now)).toBe('Today 10:41');
    expect(f.whenLabel(new Date(2026, 10, 6, 18, 30).toISOString(), now)).toBe('06 Nov 18:30');
  });

  it('formats elapsed time', () => {
    expect([12, 42 * 60, 65 * 60, 47 * 3600, 3 * 86400, -5].map(f.duration)).toEqual(['12 s', '42 m', '1 h 05', '47 h 00', '3 d', '0 s']);
    expect(f.since(new Date(Date.UTC(2026, 0, 1, 0, 0)).toISOString(), new Date(Date.UTC(2026, 0, 1, 0, 42)))).toBe('42 m');
    expect(f.since('bad', new Date())).toBe('');
  });
});

describe('labels', () => {
  it('shortens model labels and keeps the 1m tag', () => {
    expect(f.modelShort('claude-opus-5-5')).toBe('opus');
    expect(f.modelShort('claude-sonnet-4-5[1m]')).toBe('sonnet 1m');
    expect(f.modelShort('claude-3-5-haiku')).toBe('haiku');
    expect(f.modelShort('unknown')).toBe('unknown');
  });

  it('names context bands', () => {
    expect(f.bandLabel({ minTokens: 0, maxTokens: 100_000 })).toBe('<100k');
    expect(f.bandLabel({ minTokens: 100_000, maxTokens: 200_000 })).toBe('100–200k');
    expect(f.bandLabel({ minTokens: 700_000, maxTokens: 2_000_000 })).toBe('700k–2.00M');
    expect(f.bandLabel({ minTokens: 700_000, maxTokens: null })).toBe('700k+');
  });

  it('lets only schema-shaped labels through', () => {
    expect(projectLabel('benchwatch')).toBe('benchwatch');
    expect(projectLabel('scratch (tmp)')).toBe('scratch (tmp)');
    expect(projectLabel('/Users/someone/secret')).toBe('other');
    expect(projectLabel('rm -rf /')).toBe('other');
    expect(projectLabel(42)).toBe('other');
    expect(modelLabel('claude-opus-5-5')).toBe('claude-opus-5-5');
    expect(modelLabel('gpt says hi')).toBe('unknown');
    expect(modelLabel('gpt-5.1-codex-max')).toBe('gpt-5.1-codex-max');
    expect(modelLabel('/Users/x/gpt-5')).toBe('unknown');
    expect(modelLabel('a..b')).toBe('unknown');
    expect(modelLabel(`gpt-${'a'.repeat(60)}`)).toBe('unknown');
    expect(agentLabel('code-reviewer')).toBe('code-reviewer');
    expect(agentLabel('Explore the repo please')).toBe('unknown');
    expect(sessionLabel('0a1b2c3d')).toBe('0a1b2c3d');
    expect(sessionLabel('0a1b2c3d-full-uuid')).toBe('unknown');
  });

  it('turns anything that is not a finite number into 0 or null', () => {
    expect([3, '3', Number.NaN, null, undefined].map(num)).toEqual([3, 0, 0, 0, 0]);
    expect([3, '3', Number.NaN, null].map(numOrNull)).toEqual([3, null, null, null]);
  });
});

describe('counted', () => {
  it('uses the singular for exactly one', () => {
    expect([0, 1, 2, 1200].map((n) => f.counted(n, 'run'))).toEqual(['0 runs', '1 run', '2 runs', '1,200 runs']);
  });
});

describe('scales', () => {
  it('clamps and divides safely', () => {
    expect([clamp01(-1), clamp01(0.4), clamp01(2), clamp01(Number.NaN)]).toEqual([0, 0.4, 1, 0]);
    expect(fraction(965_599, 200_000)).toBe(1);
    expect(fraction(50, 200)).toBe(0.25);
    expect(fraction(5, 0)).toBe(0);
    expect(share(1, 4)).toBe(0.25);
    expect(share(1, 0)).toBe(0);
    expect(maxOf([{ v: 2 }, { v: Number.NaN }, { v: 7 }], (r) => r.v)).toBe(7);
    expect(maxOf([], () => 1)).toBe(0);
  });

  it('lists and fills every day of a period', () => {
    expect(daysBetween('2026-10-30', '2026-11-02')).toEqual(['2026-10-30', '2026-10-31', '2026-11-01', '2026-11-02']);
    expect(daysBetween('2026-03-28', '2026-03-30')).toHaveLength(3);
    expect(daysBetween('2026-11-02', '2026-11-01')).toEqual([]);
    expect(daysBetween('bad', '2026-11-01')).toEqual([]);
    expect(daysBetween('2020-01-01', '2026-01-01')).toEqual([]);
    const rows = fillDays('2026-11-01', '2026-11-03', [{ day: '2026-11-02', n: 5 }], (day) => ({ day, n: 0 }));
    expect(rows).toEqual([{ day: '2026-11-01', n: 0 }, { day: '2026-11-02', n: 5 }, { day: '2026-11-03', n: 0 }]);
  });

  it('finds the peak', () => {
    expect(peakIndex([1, 5, 5, 2])).toBe(1);
    expect(peakIndex([0, 0])).toBe(-1);
    expect(peakIndex([])).toBe(-1);
  });

  it('splits a bar into segments with gaps', () => {
    const segs = segments([{ v: 1 }, { v: 0 }, { v: 3 }], (i) => i.v, 0.01);
    expect(segs).toHaveLength(2);
    expect(segs[0]?.start).toBe(0);
    expect(segs[0]?.end).toBeCloseTo(0.245);
    expect(segs[1]?.start).toBeCloseTo(0.255);
    expect(segs[1]?.end).toBeCloseTo(0.99);
    expect(segments([{ v: 0 }], (i) => i.v)).toEqual([]);
  });

  it('places instants on a window', () => {
    const start = Date.UTC(2026, 0, 1);
    const end = start + 1000;
    expect(timeFraction(new Date(start + 250).toISOString(), start, end)).toBe(0.25);
    expect(timeFraction(new Date(start - 5).toISOString(), start, end)).toBe(0);
    expect(timeFraction(new Date(end + 5).toISOString(), start, end)).toBe(1);
    expect(timeFraction('bad', start, end)).toBe(0);
    expect(timeFraction(new Date(start).toISOString(), end, start)).toBe(0);
  });
});
