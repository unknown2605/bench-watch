import { clamp01, finite, pieceState, polar, type Pt } from './geometry';

export const MACHINE_WEDGES = 16;
export const MACHINE_BOX = 440;
export const WEDGE_IN = 96;
export const WEDGE_OUT = 166;
export const LABEL_R = 194;

export type CoreRow = { core: number; kind: 'efficiency' | 'performance'; busyPct: number };
export type MachineInput = {
  cores: readonly CoreRow[];
  cpuBusyPct: number | null;
  load: { one: number; five: number; fifteen: number } | null;
};

export type Wedge = {
  index: number;
  name: string;
  kind: CoreRow['kind'];
  cores: number;
  busy: number;
  start: number;
  sweep: number;
  outer: number;
  label: Pt;
};

/** Splits n wedges between the kinds in proportion to their cores, at least one each kind that has cores. */
function shareWedges(p: number, e: number, cap: number): [number, number] {
  if (p + e <= cap) return [p, e];
  if (!p || !e) return [p ? cap : 0, e ? cap : 0];
  const wp = Math.min(cap - 1, Math.max(1, Math.round((cap * p) / (p + e))));
  return [wp, cap - wp];
}

function group(cores: readonly CoreRow[], n: number, prefix: string) {
  const out: { name: string; cores: number; busy: number }[] = [];
  for (let i = 0; i < n; i += 1) {
    const a = Math.floor((i * cores.length) / n);
    const b = Math.floor(((i + 1) * cores.length) / n);
    const part = cores.slice(a, b);
    const busy = part.reduce((s, c) => s + finite(c.busyPct), 0) / Math.max(1, part.length);
    out.push({ name: part.length > 1 ? `${prefix}${a + 1}–${b}` : `${prefix}${a + 1}`, cores: part.length, busy });
  }
  return out;
}

/** Performance cores first from 12 o'clock, then efficiency cores; load sets only each wedge's outer radius. */
export function machineModel(input: MachineInput) {
  const byCore = (a: CoreRow, b: CoreRow) => a.core - b.core;
  const perf = input.cores.filter((c) => c.kind === 'performance').sort(byCore);
  const eff = input.cores.filter((c) => c.kind !== 'performance').sort(byCore);
  const [wp, we] = shareWedges(perf.length, eff.length, MACHINE_WEDGES);
  const parts = [
    ...group(perf, wp, 'P').map((g) => ({ ...g, kind: 'performance' as const })),
    ...group(eff, we, 'E').map((g) => ({ ...g, kind: 'efficiency' as const })),
  ];
  const n = parts.length;
  const step = n ? 360 / n : 360;
  const wedges: Wedge[] = parts.map((g, i) => ({
    index: i,
    name: g.name,
    kind: g.kind,
    cores: g.cores,
    busy: g.busy,
    start: i * step,
    sweep: step,
    outer: WEDGE_IN + 10 + clamp01(g.busy / 100) * (WEDGE_OUT - WEDGE_IN - 10),
    label: polar(LABEL_R, i * step + step / 2, MACHINE_BOX / 2, MACHINE_BOX / 2),
  }));
  return {
    wedges,
    cores: input.cores.length,
    state: pieceState(input.cores.map((c) => finite(c.busyPct)), MACHINE_WEDGES),
  };
}
