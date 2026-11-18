import { finite, pieceState, polar, slotAngle, type PieceState } from './geometry';

export const SESSION_SLOTS = 12;
export const PROJECT_LEGEND = 4;
export const SESSION_R = 144;
export const SESSION_BOX = 340;

export type SessionTone = 'live' | 'gold' | 'session' | 'warn' | 'dim';
export const PROJECT_TONES: readonly SessionTone[] = ['live', 'gold', 'session', 'warn'];

export type SessionItem = { key: string; project: string; tokens: number; state: 'live' | 'idle' | 'ended' };
export type ProjectItem = { project: string; sessions: number; tokens: number };
export type SessionsInput = {
  sessions: readonly SessionItem[];
  projects: readonly ProjectItem[];
  total: number;
  live: number;
};

export type SessionSlot = {
  index: number;
  angle: number;
  x: number;
  y: number;
  item: (SessionItem & { rank: number; size: number; tone: SessionTone }) | null;
};

/** Three dot sizes by tokens relative to the largest session: a quarter or more, a twelfth or more, the rest. */
export function dotSize(tokens: number, max: number): 32 | 26 | 20 {
  if (!(max > 0) || !(tokens > 0)) return 20;
  const r = tokens / max;
  return r >= 0.25 ? 32 : r >= 1 / 12 ? 26 : 20;
}

export function sessionsModel(input: SessionsInput) {
  const sorted = [...input.sessions].sort((a, b) => finite(b.tokens) - finite(a.tokens));
  const projects = [...input.projects].sort((a, b) => finite(b.tokens) - finite(a.tokens));
  const legendNames = projects.slice(0, PROJECT_LEGEND).map((p) => p.project);
  const toneOf = (project: string): SessionTone => PROJECT_TONES[legendNames.indexOf(project)] ?? 'dim';
  const max = finite(sorted[0]?.tokens);
  const c = SESSION_BOX / 2;
  const slots: SessionSlot[] = Array.from({ length: SESSION_SLOTS }, (_, i) => {
    const angle = slotAngle(i, SESSION_SLOTS);
    const { x, y } = polar(SESSION_R, angle, c, c);
    const s = sorted[i];
    return {
      index: i,
      angle,
      x,
      y,
      item: s ? { ...s, rank: i + 1, size: dotSize(finite(s.tokens), max), tone: toneOf(s.project) } : null,
    };
  });
  const rest = projects.slice(PROJECT_LEGEND);
  const legend = projects.slice(0, PROJECT_LEGEND).map((p, i) => ({
    project: p.project,
    sessions: finite(p.sessions),
    tone: PROJECT_TONES[i] ?? 'dim',
  }));
  if (rest.length) {
    legend.push({ project: 'other', sessions: rest.reduce((a, p) => a + finite(p.sessions), 0), tone: 'dim' });
  }
  const total = Math.max(finite(input.total), sorted.length);
  const live = Math.min(finite(input.live), total);
  const state: PieceState = {
    ...pieceState(sorted.map((s) => finite(s.tokens)), SESSION_SLOTS),
    capped: total > SESSION_SLOTS,
  };
  return {
    slots,
    more: Math.max(0, total - SESSION_SLOTS),
    legend,
    total,
    live,
    liveShare: total > 0 ? live / total : 0,
    state,
  };
}

const HOUR_MS = 3_600_000;
const STACK_ORDER: readonly SessionTone[] = ['live', 'session', 'gold', 'warn', 'dim'];
export type HourSpan = { project: string; start: number; end: number };
export type HourBar = {
  hour: number;
  future: boolean;
  total: number;
  stack: { tone: SessionTone; count: number }[];
  marks: number;
};

/** Sessions active in each hour of the day that starts at `dayStart`, stacked by project tone (top 4 by tokens). */
export function sessionHours(
  spans: readonly HourSpan[],
  marks: readonly number[],
  ranked: readonly string[],
  dayStart: number,
  now: number,
) {
  const toneOf = (project: string): SessionTone =>
    PROJECT_TONES[ranked.slice(0, PROJECT_LEGEND).indexOf(project)] ?? 'dim';
  const bars: HourBar[] = Array.from({ length: 24 }, (_, hour) => {
    const from = dayStart + hour * HOUR_MS;
    const to = from + HOUR_MS;
    if (from > now) return { hour, future: true, total: 0, stack: [], marks: 0 };
    const counts = new Map<SessionTone, number>();
    for (const s of spans) {
      if (s.start < to && s.end >= from) counts.set(toneOf(s.project), (counts.get(toneOf(s.project)) ?? 0) + 1);
    }
    const stack = STACK_ORDER.flatMap((tone) => (counts.get(tone) ? [{ tone, count: counts.get(tone) ?? 0 }] : []));
    const total = stack.reduce((a, p) => a + p.count, 0);
    return { hour, future: false, total, stack, marks: marks.filter((at) => at >= from && at < to).length };
  });
  const peak = Math.max(0, ...bars.map((b) => b.total));
  return {
    bars,
    top: Math.max(2, Math.ceil(peak / 2) * 2),
    nowHour: Math.min(23, Math.max(0, Math.floor((now - dayStart) / HOUR_MS))),
  };
}
