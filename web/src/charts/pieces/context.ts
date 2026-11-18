import { finite, pieceState } from './geometry';

export const CONTEXT_RINGS = 5;
export const CTX_HUB_R = 58;
export const CTX_BASE = 84;
export const CTX_STEP = 22;
export const CTX_STROKE = 10;
export const CTX_BOX = 432;
export const CONTEXT_LIMIT = 200_000;
export const EMPTY_SWEEP = 0.25;
export const MIN_SWEEP = 4;

export type BandDef = { minTokens: number; maxTokens: number | null };
export type BandRow = { minTokens: number; calls: number; contextTokens: number; cacheReadTokens: number };
export type ContextInput = {
  defs: readonly BandDef[];
  bands: readonly BandRow[];
  avgPerCall: number | null;
  compactions: number;
  limit?: number;
  rangeLabel: string;
};

export type ContextRing = {
  index: number;
  r: number;
  band: BandDef;
  calls: number;
  share: number;
  sweep: number;
  reread: number | null;
  tone: 'live' | 'warn';
};

export const ringRadius = (i: number) => CTX_BASE + CTX_STEP * i;

/** Five rings, innermost the lowest band; definitions past the fifth fold into the outermost ring. */
export function contextModel(input: ContextInput) {
  const limit = input.limit ?? CONTEXT_LIMIT;
  const defs = [...input.defs].sort((a, b) => a.minTokens - b.minTokens);
  const shown: BandDef[] = defs.slice(0, CONTEXT_RINGS);
  const last = shown[CONTEXT_RINGS - 1];
  if (defs.length > CONTEXT_RINGS && last) shown[CONTEXT_RINGS - 1] = { minTokens: last.minTokens, maxTokens: null };
  const ringOf = (min: number) => {
    let at = -1;
    shown.forEach((d, i) => {
      if (min >= d.minTokens) at = i;
    });
    return at;
  };
  const sums = shown.map(() => ({ calls: 0, context: 0, read: 0 }));
  for (const b of input.bands) {
    const s = sums[ringOf(finite(b.minTokens))];
    if (!s) continue;
    s.calls += finite(b.calls);
    s.context += finite(b.contextTokens);
    s.read += finite(b.cacheReadTokens);
  }
  const totalCalls = sums.reduce((a, s) => a + s.calls, 0);
  const rings: ContextRing[] = shown.map((band, i) => {
    const s = sums[i] ?? { calls: 0, context: 0, read: 0 };
    const share = totalCalls > 0 ? s.calls / totalCalls : 0;
    return {
      index: i,
      r: ringRadius(i),
      band,
      calls: s.calls,
      share,
      sweep: s.calls > 0 ? Math.max(MIN_SWEEP, share * 360) : EMPTY_SWEEP,
      reread: s.context > 0 ? s.read / s.context : null,
      tone: band.maxTokens !== null && band.maxTokens <= limit ? 'live' : 'warn',
    };
  });
  const under = rings.filter((r) => r.tone === 'live').length;
  const limitR = under > 0 ? ringRadius(under - 1) + CTX_STEP / 2 : CTX_BASE - CTX_STEP / 2;
  return {
    rings,
    limitR,
    totalCalls,
    avg: input.avgPerCall,
    state: { ...pieceState(sums.map((s) => s.calls), CONTEXT_RINGS), capped: defs.length > CONTEXT_RINGS },
  };
}
