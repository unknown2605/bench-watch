import { parseDay } from './format';

export function clamp01(v: number): number {
  return Number.isFinite(v) ? Math.max(0, Math.min(1, v)) : 0;
}

/** v as a fraction of max, 0 when max is not positive. */
export function fraction(v: number, max: number): number {
  return max > 0 ? clamp01(v / max) : 0;
}

export function share(part: number, total: number): number {
  return total > 0 ? part / total : 0;
}

export function maxOf<T>(rows: readonly T[], pick: (row: T) => number): number {
  let max = 0;
  for (const r of rows) {
    const v = pick(r);
    if (Number.isFinite(v) && v > max) max = v;
  }
  return max;
}

/** Each day from `from` to `to` inclusive, as YYYY-MM-DD; empty when the range is invalid or over 400 days. */
export function daysBetween(from: string, to: string): string[] {
  const a = parseDay(from);
  const b = parseDay(to);
  if (!a || !b) return [];
  const out: string[] = [];
  const t = new Date(Date.UTC(a.y, a.m - 1, a.d));
  const end = Date.UTC(b.y, b.m - 1, b.d);
  while (t.getTime() <= end && out.length <= 400) {
    out.push(t.toISOString().slice(0, 10));
    t.setUTCDate(t.getUTCDate() + 1);
  }
  return out.length > 400 ? [] : out;
}

/** The API leaves out days with no calls; charts need every day of the period, with zeros. */
export function fillDays<T extends { day: string }>(from: string, to: string, rows: readonly T[], empty: (day: string) => T): T[] {
  const byDay = new Map(rows.map((r) => [r.day, r]));
  return daysBetween(from, to).map((day) => byDay.get(day) ?? empty(day));
}

/** Index of the largest value; the first one wins a tie, -1 for an empty or all-zero list. */
export function peakIndex(values: readonly number[]): number {
  let best = -1;
  let max = 0;
  values.forEach((v, i) => {
    if (v > max) {
      max = v;
      best = i;
    }
  });
  return best;
}

export type Segment<T> = { item: T; start: number; end: number };

/** Splits [0, 1] into consecutive segments by value, leaving `gap` between non-empty ones. */
export function segments<T>(items: readonly T[], value: (item: T) => number, gap = 0): Segment<T>[] {
  const live = items.filter((i) => value(i) > 0);
  const total = live.reduce((s, i) => s + value(i), 0);
  if (total <= 0) return [];
  const usable = Math.max(0, 1 - gap * live.length);
  let at = 0;
  return live.map((item) => {
    const len = (value(item) / total) * usable;
    const seg = { item, start: at, end: at + len };
    at += len + gap;
    return seg;
  });
}

/** Position of an instant on a window [start, end] as 0-1, clamped. */
export function timeFraction(iso: string, start: number, end: number): number {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t) || end <= start) return 0;
  return clamp01((t - start) / (end - start));
}

/** Averages `values` into at most `n` equal buckets, skipping nulls; a bucket with none is 0. */
export function bucketMean(values: readonly (number | null)[], n: number): number[] {
  if (n <= 0 || values.length === 0) return [];
  const count = Math.min(n, values.length);
  return Array.from({ length: count }, (_, i) => {
    const from = Math.floor((i * values.length) / count);
    const to = Math.floor(((i + 1) * values.length) / count);
    let sum = 0;
    let seen = 0;
    for (let k = from; k < to; k += 1) {
      const v = values[k];
      if (typeof v === 'number' && Number.isFinite(v)) {
        sum += v;
        seen += 1;
      }
    }
    return seen ? sum / seen : 0;
  });
}

/** Means of `values` per `bucket` ms aligned to the epoch, from the bucket holding `from` to the newest; empty is 0. */
export function timeBuckets(
  times: readonly number[],
  values: readonly (number | null)[],
  bucket: number,
  from: number,
): { starts: number[]; means: number[] } {
  const last = times[times.length - 1];
  if (bucket <= 0 || last === undefined) return { starts: [], means: [] };
  const first = Math.floor(from / bucket) * bucket;
  const count = Math.max(0, Math.floor(last / bucket) * bucket - first) / bucket + 1;
  const sums = new Array<number>(count).fill(0);
  const seen = new Array<number>(count).fill(0);
  times.forEach((t, i) => {
    const v = values[i];
    const at = Math.floor((t - first) / bucket);
    if (at < 0 || at >= count || typeof v !== 'number' || !Number.isFinite(v)) return;
    sums[at] = (sums[at] ?? 0) + v;
    seen[at] = (seen[at] ?? 0) + 1;
  });
  return {
    starts: sums.map((_, i) => first + i * bucket),
    means: sums.map((s, i) => ((seen[i] ?? 0) ? s / (seen[i] ?? 1) : 0)),
  };
}
