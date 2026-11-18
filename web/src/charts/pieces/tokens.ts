import { finite, logScale, pieceState, shareSpans, type Span } from './geometry';

export const TOKEN_KINDS = [
  { key: 'cacheRead', name: 'cache read', tone: 'gold' },
  { key: 'cacheWrite', name: 'cache write', tone: 'session' },
  { key: 'output', name: 'output', tone: 'warm' },
  { key: 'input', name: 'input', tone: 'live' },
] as const;
export type TokenTone = (typeof TOKEN_KINDS)[number]['tone'];

export const SMALL_SHARE = 0.05;
export const ZOOM_FROM = 0.5;
export const LOG_LO = 1e5;
export const LOG_HI = 1e9;
export const TOK_BOX = 350;

export type KindRow = { kind: string; tokens: number; costUsd: number };
export type TokensInput = {
  kinds: readonly KindRow[];
  tokens: number | null;
  costUsd: number | null;
  previousCost: number | null;
  days: number;
  rangeLabel: string;
};

export type TokenSlice = {
  index: number;
  key: string;
  name: string;
  tone: TokenTone;
  tokens: number;
  cost: number;
  share: number;
  costShare: number;
  span: Span;
  bar: number;
};

/** Four kinds in fixed slot order; each non-zero kind keeps at least 1 degree so a tiny share stays visible. */
export function tokensModel(input: TokensInput) {
  const rows = TOKEN_KINDS.map((k) => {
    const hit = input.kinds.filter((r) => r.kind === k.key);
    return {
      ...k,
      tokens: hit.reduce((a, r) => a + finite(r.tokens), 0),
      cost: hit.reduce((a, r) => a + finite(r.costUsd), 0),
    };
  });
  const total = rows.reduce((a, r) => a + r.tokens, 0);
  const totalCost = rows.reduce((a, r) => a + r.cost, 0);
  const spans = shareSpans(rows.map((r) => r.tokens), 1);
  const slices: TokenSlice[] = rows.map((r, i) => ({
    index: i,
    key: r.key,
    name: r.name,
    tone: r.tone,
    tokens: r.tokens,
    cost: r.cost,
    share: total > 0 ? r.tokens / total : 0,
    costShare: totalCost > 0 ? r.cost / totalCost : 0,
    span: spans[i] ?? { start: 0, sweep: 0 },
    bar: r.tokens > 0 ? Math.max(0.02, logScale(r.tokens, LOG_LO, LOG_HI)) : 0,
  }));
  const largest = slices.reduce<TokenSlice | null>(
    (a, s) => (s.tokens > 0 && (!a || s.tokens > a.tokens) ? s : a),
    null,
  );
  const rest = largest ? total - largest.tokens : 0;
  const zoomed = largest && largest.share >= ZOOM_FROM && rest > 0 ? largest : null;
  const zoomSpans = zoomed ? shareSpans(slices.map((s) => (s === zoomed ? 0 : s.tokens)), 1) : [];
  return {
    slices,
    total,
    big: slices.filter((s) => s.tokens > 0 && s.share >= SMALL_SHARE),
    small: slices.filter((s) => s.share < SMALL_SHARE && total > 0),
    zoom: zoomed
      ? { of: zoomed, factor: Math.round(total / rest), restShare: rest / total, spans: zoomSpans }
      : null,
    state: pieceState(rows.map((r) => r.tokens), TOKEN_KINDS.length),
  };
}
