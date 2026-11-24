import { finite, logScale, pieceState } from './geometry';

export const STACK_GROUPS = 3;
export const HEX_PER_STACK = 12;
export const HEX_W = 66;
export const HEX_H = 76;
export const HEX_ROW = 57;
export const MEM_LO = 5 * 1024 * 1024;
export const MEM_HI = 1024 * 1024 * 1024;

export type DockerContainer = { name: string; stack: string; memBytes: number; health: string; restarts: number };
export type DockerStack = { stack: string; memBytes: number; rxBps: number; txBps: number };
export type DockerInput = { containers: readonly DockerContainer[]; stacks: readonly DockerStack[] };

export type Hex = {
  key: string;
  name: string;
  memBytes: number;
  bar: number;
  health: 'ok' | 'none' | 'warn' | 'finished';
  kind: 'container' | 'more' | 'other';
  count: number;
  col: number;
  row: number;
  x: number;
  y: number;
};

export type HexGroup = {
  key: string;
  title: string;
  count: number;
  cols: number;
  width: number;
  height: number;
  hexes: Hex[];
  stack: DockerStack | null;
};

export const memBar = (bytes: number) => (bytes > 0 ? Math.max(0.04, logScale(bytes, MEM_LO, MEM_HI)) : 0);

export function healthOf(h: string): Hex['health'] {
  if (h === 'healthy') return 'ok';
  if (h === 'none' || h === '') return 'none';
  if (h === 'finished') return 'finished';
  return 'warn';
}

/** A folded hex warns when any container in it does, and reads finished only when all of them finished. */
export function groupHealth(list: readonly DockerContainer[]): Hex['health'] {
  const all = list.map((c) => healthOf(c.health));
  if (all.length === 0) return 'none';
  if (all.includes('warn')) return 'warn';
  if (all.length === 1) return all[0] ?? 'none';
  return all.every((h) => h === 'finished') ? 'finished' : 'none';
}

export function columnsFor(n: number): number {
  return n <= 3 ? Math.max(1, n) : n <= 9 ? 3 : 4;
}

function place(hexes: Omit<Hex, 'col' | 'row' | 'x' | 'y'>[], cols: number): Hex[] {
  return hexes.map((h, i) => {
    const row = Math.floor(i / cols);
    const col = i % cols;
    return { ...h, col, row, x: col * HEX_W + (row % 2 ? HEX_W / 2 : 0), y: row * HEX_ROW };
  });
}

function sized(
  key: string,
  title: string,
  count: number,
  hexes: Omit<Hex, 'col' | 'row' | 'x' | 'y'>[],
  stack: DockerStack | null,
): HexGroup {
  const cols = columnsFor(hexes.length);
  const rows = Math.ceil(hexes.length / cols);
  return {
    key,
    title,
    count,
    cols,
    width: cols * HEX_W + (rows > 1 ? HEX_W / 2 : 0),
    height: rows ? (rows - 1) * HEX_ROW + HEX_H : 0,
    hexes: place(hexes, cols),
    stack,
  };
}

/** Three largest stacks get their own honeycomb of up to 12 hexes; every other container folds into one "other" hex. */
export function dockerModel(input: DockerInput) {
  const byStack = new Map<string, DockerContainer[]>();
  for (const c of input.containers) {
    const k = c.stack && c.stack !== 'none' ? c.stack : '';
    byStack.set(k, [...(byStack.get(k) ?? []), c]);
  }
  const named = [...byStack.entries()]
    .filter(([k]) => k !== '')
    .sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]));
  const shown = named.slice(0, STACK_GROUPS).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  const folded = [...named.slice(STACK_GROUPS).flatMap(([, list]) => list), ...(byStack.get('') ?? [])];
  const stackOf = (k: string) => input.stacks.find((s) => s.stack === k) ?? null;
  const toHex = (c: DockerContainer): Omit<Hex, 'col' | 'row' | 'x' | 'y'> => ({
    key: c.name,
    name: c.name,
    memBytes: finite(c.memBytes),
    bar: memBar(finite(c.memBytes)),
    health: healthOf(c.health),
    kind: 'container',
    count: 1,
  });
  const groups: HexGroup[] = shown.map(([stack, list]) => {
    const sorted = [...list].sort((a, b) => finite(b.memBytes) - finite(a.memBytes) || a.name.localeCompare(b.name));
    const hexes = sorted.length > HEX_PER_STACK ? sorted.slice(0, HEX_PER_STACK - 1).map(toHex) : sorted.map(toHex);
    if (sorted.length > HEX_PER_STACK) {
      const rest = sorted.slice(HEX_PER_STACK - 1);
      const mem = rest.reduce((a, c) => a + finite(c.memBytes), 0);
      hexes.push({
        key: `${stack}+more`,
        name: `+${rest.length} more`,
        memBytes: mem,
        bar: memBar(mem),
        health: groupHealth(rest),
        kind: 'more',
        count: rest.length,
      });
    }
    return sized(stack, stack, list.length, hexes, stackOf(stack));
  });
  if (folded.length) {
    const mem = folded.reduce((a, c) => a + finite(c.memBytes), 0);
    const only = folded.length === 1 ? folded[0] : undefined;
    const title = named.length > STACK_GROUPS ? 'Other stacks' : 'No stack';
    groups.push(
      sized('other', title, folded.length, [
        {
          key: 'other',
          name: only ? only.name : `other · ${folded.length}`,
          memBytes: mem,
          bar: memBar(mem),
          health: groupHealth(folded),
          kind: 'other',
          count: folded.length,
        },
      ], null),
    );
  }
  const total = input.containers.length;
  return {
    groups,
    total,
    healthy: input.containers.filter((c) => healthOf(c.health) === 'ok').length,
    noCheck: input.containers.filter((c) => healthOf(c.health) === 'none').length,
    warn: input.containers.filter((c) => healthOf(c.health) === 'warn').length,
    finished: input.containers.filter((c) => healthOf(c.health) === 'finished').length,
    restarts: input.containers.reduce((a, c) => a + finite(c.restarts), 0),
    otherMem: folded.reduce((a, c) => a + finite(c.memBytes), 0),
    state: {
      ...pieceState(input.containers.map((c) => finite(c.memBytes)), HEX_PER_STACK * STACK_GROUPS),
      capped: named.length > STACK_GROUPS || named.some(([, l]) => l.length > HEX_PER_STACK),
    },
  };
}
