import { finite, pieceState, shareSpans, type Span } from './geometry';

export const FILL_TOP = 6;
export const FILL_TAIL_ROWS = 5;
export const TAIL_SCALE = 0.05;
export const FILL_BOX = 350;
export const FILL_TONES = ['gold', 'session', 'warm', 'live', 'slate', 'bronze'] as const;
export type FillTone = (typeof FILL_TONES)[number] | 'small';

export type FillTool = { tool: string; tokens: number; weightedTokens: number; results: number };
export type FillsInput = {
  tools: readonly FillTool[];
  contextShare: number | null;
  resultTokens: number | null;
  rangeLabel: string;
};

export type FillSlice = FillTool & {
  rank: number;
  share: number;
  span: Span;
  tone: FillTone;
  side: 'left' | 'right';
};

/** Top six tools by weighted tokens in rank order from 12 o'clock, then one slice for every smaller tool. */
export function fillsModel(input: FillsInput) {
  const sorted = [...input.tools]
    .filter((t) => finite(t.weightedTokens) > 0)
    .sort((a, b) => finite(b.weightedTokens) - finite(a.weightedTokens));
  const total = sorted.reduce((a, t) => a + finite(t.weightedTokens), 0);
  const top = sorted.slice(0, FILL_TOP);
  const rest = sorted.slice(FILL_TOP);
  const restWeighted = rest.reduce((a, t) => a + finite(t.weightedTokens), 0);
  const spans = shareSpans([...top.map((t) => finite(t.weightedTokens)), restWeighted], 1);
  const side = (s: Span) => (s.start + s.sweep / 2 < 180 ? 'right' : 'left');
  const slices: FillSlice[] = top.map((t, i) => {
    const span = spans[i] ?? { start: 0, sweep: 0 };
    const share = total > 0 ? finite(t.weightedTokens) / total : 0;
    return { ...t, rank: i + 1, share, span, tone: FILL_TONES[i] ?? 'slate', side: side(span) };
  });
  const smallSpan = spans[top.length] ?? { start: 0, sweep: 0 };
  const tail = rest.slice(0, FILL_TAIL_ROWS).map((t) => {
    const share = total > 0 ? finite(t.weightedTokens) / total : 0;
    return { ...t, share, bar: Math.min(1, share / TAIL_SCALE) };
  });
  return {
    slices,
    small: rest.length ? { tools: rest.length, share: total > 0 ? restWeighted / total : 0, span: smallSpan } : null,
    tail,
    more: Math.max(0, rest.length - FILL_TAIL_ROWS),
    total,
    state: pieceState(sorted.map((t) => finite(t.weightedTokens)), FILL_TOP),
  };
}

export const LEADER_MIN_SWEEP = 6;
type AnchorItem = { key: string; span: Span; side: 'left' | 'right' };
export type LeaderAnchor = { key: string; angle: number; shared: boolean };

/** Leader start angles in ring order; a run of adjacent same-side slices under LEADER_MIN_SWEEP shares its middle. */
export function leaderAnchors(items: readonly AnchorItem[]): LeaderAnchor[] {
  const out: LeaderAnchor[] = [];
  let run: AnchorItem[] = [];
  const flush = () => {
    const first = run[0];
    const last = run[run.length - 1];
    if (first && last) {
      const angle = (first.span.start + last.span.start + last.span.sweep) / 2;
      for (const it of run) out.push({ key: it.key, angle, shared: run.length > 1 });
    }
    run = [];
  };
  for (const it of items) {
    const narrow = it.span.sweep < LEADER_MIN_SWEEP;
    if (!narrow || run[0]?.side !== it.side) flush();
    if (narrow) run.push(it);
    else out.push({ key: it.key, angle: it.span.start + it.span.sweep / 2, shared: false });
  }
  flush();
  return out;
}
