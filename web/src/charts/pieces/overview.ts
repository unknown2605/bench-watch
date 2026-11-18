import { daysBetween } from '../../format/scale';
import { clamp01, finite, pieceState } from './geometry';

export const OV_BOX = 400;
export const OV_TRACK = 300;
export const OV_START = -150;
export const TREND_SLOTS = 14;

export type OverviewArcKey = 'reread' | 'sub' | 'over';
type OverviewArc = { key: OverviewArcKey; name: string; tone: 'gold' | 'live' | 'warn'; r: number };
export const OVERVIEW_ARCS: readonly OverviewArc[] = [
  { key: 'reread', name: 'Re-read', tone: 'gold', r: 170 },
  { key: 'sub', name: 'Sub-agent', tone: 'live', r: 154 },
  { key: 'over', name: 'Over 200K', tone: 'warn', r: 140 },
];

export type TrendDay = { day: string; tokens: number };
export type OverviewInput = {
  tokens: number | null;
  previous: number | null;
  days: number;
  mainShare: number | null;
  reread: number | null;
  sub: number | null;
  over: number | null;
  trend: readonly TrendDay[];
  today: string;
  rangeLabel: string;
};

/** The last 14 calendar days up to `today`, one fixed slot each; a day with no row is null and leaves a gap. */
export function trendSlots(rows: readonly TrendDay[], today: string, slots = TREND_SLOTS): (number | null)[] {
  const t = new Date(`${today}T00:00:00Z`);
  if (Number.isNaN(t.getTime())) return Array.from({ length: slots }, () => null);
  t.setUTCDate(t.getUTCDate() - (slots - 1));
  const days = daysBetween(t.toISOString().slice(0, 10), today);
  const byDay = new Map(rows.map((r) => [r.day, finite(r.tokens)]));
  return days.map((d) => (byDay.has(d) ? (byDay.get(d) ?? 0) : null));
}

export type TrendPoint = { slot: number; x: number; y: number; value: number };

/** Points in a width x height box, the y axis from zero to the largest day; runs split where a day is missing. */
export function trendRuns(values: readonly (number | null)[], width: number, height: number) {
  const max = Math.max(0, ...values.map((v) => v ?? 0));
  const step = values.length > 1 ? width / (values.length - 1) : 0;
  const runs: TrendPoint[][] = [];
  let run: TrendPoint[] = [];
  values.forEach((v, slot) => {
    if (v === null) {
      if (run.length) runs.push(run);
      run = [];
      return;
    }
    const y = max > 0 ? height - (v / max) * height : height;
    run.push({ slot, x: Math.round(slot * step * 100) / 100, y: Math.round(y * 100) / 100, value: v });
  });
  if (run.length) runs.push(run);
  return { runs, max };
}

export function overviewModel(input: OverviewInput) {
  const value = (k: OverviewArcKey) => (k === 'reread' ? input.reread : k === 'sub' ? input.sub : input.over);
  const arcs = OVERVIEW_ARCS.map((a) => {
    const v = value(a.key);
    return { ...a, value: v, sweep: v === null ? 0 : clamp01(v) * OV_TRACK };
  });
  const trend = trendSlots(input.trend, input.today);
  const present = trend.filter((v): v is number => v !== null);
  return {
    arcs,
    trend,
    state: { ...pieceState(present, TREND_SLOTS), capped: input.trend.length > TREND_SLOTS },
  };
}
