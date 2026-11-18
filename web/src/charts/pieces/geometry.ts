// Angles are degrees clockwise from 12 o'clock, the convention of d3-shape's arc and of every slot below.

export type Pt = { x: number; y: number };

const RAD = Math.PI / 180;

export const round2 = (n: number) => Math.round(n * 100) / 100;

export function polar(r: number, deg: number, cx = 0, cy = 0): Pt {
  return { x: round2(cx + r * Math.sin(deg * RAD)), y: round2(cy - r * Math.cos(deg * RAD)) };
}

/** Slot i of n evenly spaced around a circle, the first at `offset` degrees. */
export function slotAngle(i: number, n: number, offset = 0): number {
  return offset + (360 / n) * i;
}

/** An open arc for a stroke; a sweep of 360 or more closes as two half arcs so the path keeps its length. */
export function arcPath(r: number, from: number, sweep: number): string {
  const s = Math.max(0, sweep);
  const a = polar(r, from);
  if (s >= 359.99) {
    const b = polar(r, from + 180);
    return `M ${a.x} ${a.y} A ${r} ${r} 0 1 1 ${b.x} ${b.y} A ${r} ${r} 0 1 1 ${a.x} ${a.y}`;
  }
  const b = polar(r, from + s);
  return `M ${a.x} ${a.y} A ${r} ${r} 0 ${s > 180 ? 1 : 0} 1 ${b.x} ${b.y}`;
}

export const clamp01 = (v: number) => (Number.isFinite(v) ? Math.min(1, Math.max(0, v)) : 0);

export const finite = (v: number | null | undefined): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);

/** Position on a log scale between lo and hi, 0-1; anything at or below lo is 0. */
export function logScale(v: number, lo: number, hi: number): number {
  if (!(v > lo)) return 0;
  return clamp01(Math.log10(v / lo) / Math.log10(hi / lo));
}

export type Span = { start: number; sweep: number };

/** Shares to consecutive spans of one circle, each non-zero share at least `min` degrees. */
export function shareSpans(values: readonly number[], min = 1, total = 360): Span[] {
  const sum = values.reduce((a, v) => a + Math.max(0, finite(v)), 0);
  if (sum <= 0) return values.map(() => ({ start: 0, sweep: 0 }));
  const raw = values.map((v) => (Math.max(0, finite(v)) / sum) * total);
  const lifted = raw.map((s) => (s > 0 ? Math.max(min, s) : 0));
  // The lift is taken back from the largest span, so the spans still add up to `total`.
  const extra = lifted.reduce((a, s) => a + s, 0) - total;
  const big = raw.indexOf(Math.max(...raw));
  if (extra > 0) lifted[big] = Math.max(0, (lifted[big] ?? 0) - extra);
  let at = 0;
  return lifted.map((sweep) => {
    const span = { start: round2(at), sweep: round2(sweep) };
    at += sweep;
    return span;
  });
}

export type PieceState = { empty: boolean; single: boolean; capped: boolean; skewed: boolean };

/** The four edge states every piece defines: no data, one item, over its cap, one item above 80% of the total. */
export function pieceState(values: readonly number[], cap: number): PieceState {
  const positive = values.map((v) => Math.max(0, finite(v))).filter((v) => v > 0);
  const sum = positive.reduce((a, v) => a + v, 0);
  const max = positive.length ? Math.max(...positive) : 0;
  return {
    empty: positive.length === 0,
    single: positive.length === 1,
    capped: values.length > cap,
    skewed: positive.length > 1 && max / sum > 0.8,
  };
}

export function stateAttr(s: PieceState): string {
  return (['empty', 'single', 'capped', 'skewed'] as const).filter((k) => s[k]).join(' ') || 'normal';
}

const PROJECT_PREFIX = /^www-(?:self-projects-|tag-|deploy-)?/;

/** Short project name for tight lists: www-self-projects-chat-bot reads chat-bot; the full name is the title. */
export function projectShort(project: string): string {
  const short = project.replace(PROJECT_PREFIX, '').replace(/^([A-Za-z0-9]+)-\1-/, '$1-');
  return short || project;
}
