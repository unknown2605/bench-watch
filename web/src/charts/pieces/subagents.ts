import { finite, pieceState, polar, slotAngle, type Pt } from './geometry';

export const SPOKES = 8;
export const SPOKE_START = 104;
export const SPOKE_MAX = 66;
export const SPOKE_MIN = 10;
export const TIP_R = 10;
export const SUB_BOX = 396;

/** `key`, when given, identifies the type for selection; names alone may repeat. */
export type AgentType = { name: string; runs: number; key?: string };
export type SubagentsInput = { types: readonly AgentType[]; mainShare: number | null; rangeLabel: string };

export type Spoke = {
  index: number;
  angle: number;
  start: Pt;
  end: Pt;
  tip: Pt;
  len: number;
  opacity: number;
  item: (AgentType & { rank: number; share: number }) | null;
};

/** Eight spokes at fixed angles from 12 o'clock; runs set only the bar length and its opacity. */
export function subagentsModel(input: SubagentsInput) {
  const sorted = [...input.types].filter((t) => finite(t.runs) > 0).sort((a, b) => finite(b.runs) - finite(a.runs));
  const total = sorted.reduce((a, t) => a + finite(t.runs), 0);
  const max = finite(sorted[0]?.runs);
  const c = SUB_BOX / 2;
  const spokes: Spoke[] = Array.from({ length: SPOKES }, (_, i) => {
    const angle = slotAngle(i, SPOKES);
    const t = sorted[i];
    const ratio = t && max > 0 ? finite(t.runs) / max : 0;
    const len = t ? Math.max(SPOKE_MIN, ratio * SPOKE_MAX) : SPOKE_MIN;
    return {
      index: i,
      angle,
      start: polar(SPOKE_START, angle, c, c),
      end: polar(SPOKE_START + len, angle, c, c),
      tip: polar(SPOKE_START + len + TIP_R + 2, angle, c, c),
      len,
      opacity: t ? 0.45 + 0.55 * ratio : 0,
      item: t ? { ...t, rank: i + 1, share: total > 0 ? finite(t.runs) / total : 0 } : null,
    };
  });
  const rest = sorted.slice(SPOKES);
  const otherRuns = rest.reduce((a, t) => a + finite(t.runs), 0);
  const other = rest.length ? { types: rest.length, runs: otherRuns, share: total > 0 ? otherRuns / total : 0 } : null;
  const mainShare = input.mainShare === null ? null : Math.min(1, Math.max(0, finite(input.mainShare)));
  return { spokes, other, total, mainShare, state: pieceState(sorted.map((t) => finite(t.runs)), SPOKES) };
}
