import { describe, expect, it } from 'vitest';
import { CONTEXT_RINGS, contextModel, ringRadius, EMPTY_SWEEP, MIN_SWEEP, type BandDef } from './context';
import { dockerModel, HEX_PER_STACK, memBar, STACK_GROUPS, type DockerContainer } from './docker';
import { FILL_TAIL_ROWS, FILL_TOP, fillsModel, leaderAnchors, type FillTool } from './fills';
import { arcPath, logScale, pieceState, polar, projectShort, shareSpans, slotAngle, stateAttr } from './geometry';
import { GUARD_SEGMENTS, guardModel, type GuardInput } from './guard';
import { MACHINE_WEDGES, machineModel, type CoreRow } from './machine';
import { OV_TRACK, overviewModel, trendRuns, trendSlots, TREND_SLOTS, type OverviewInput } from './overview';
import { dotSize, SESSION_BOX, SESSION_R, SESSION_SLOTS, sessionsModel, type SessionItem } from './sessions';
import { SPOKE_MAX, SPOKE_MIN, SPOKES, subagentsModel } from './subagents';
import { tokensModel } from './tokens';

const sum = (xs: readonly number[]) => xs.reduce((a, x) => a + x, 0);

describe('geometry', () => {
  it('puts 0 degrees at 12 o\'clock and turns clockwise', () => {
    const top = polar(10, 0);
    const right = polar(10, 90);
    expect([top.x, top.y, right.x, right.y].map((v) => Math.round(v) + 0)).toEqual([0, -10, 10, 0]);
    expect(slotAngle(3, 12)).toBe(90);
  });

  it('closes a full sweep as two half arcs', () => {
    expect(arcPath(10, 0, 360).match(/A /g)).toHaveLength(2);
    expect(arcPath(10, 0, 90)).toBe('M 0 -10 A 10 10 0 0 1 10 0');
  });

  it('lifts a tiny share to the minimum and still adds up to the circle', () => {
    const spans = shareSpans([1e9, 1, 0], 1);
    expect(spans[1]?.sweep).toBe(1);
    expect(spans[2]?.sweep).toBe(0);
    expect(sum(spans.map((s) => s.sweep))).toBeCloseTo(360, 1);
    expect(spans[1]?.start).toBeCloseTo(359, 1);
  });

  it('gives all-zero shares no spans', () => {
    expect(shareSpans([0, 0])).toEqual([{ start: 0, sweep: 0 }, { start: 0, sweep: 0 }]);
  });

  it('maps a log scale and clamps it', () => {
    expect(logScale(1e5, 1e5, 1e9)).toBe(0);
    expect(logScale(1e7, 1e5, 1e9)).toBeCloseTo(0.5);
    expect(logScale(1e12, 1e5, 1e9)).toBe(1);
  });

  it('names the four edge states', () => {
    expect(stateAttr(pieceState([], 4))).toBe('empty');
    expect(stateAttr(pieceState([5, 0], 4))).toBe('single');
    expect(stateAttr(pieceState([1, 1, 1, 1, 1], 4))).toBe('capped');
    expect(stateAttr(pieceState([90, 5, 5], 4))).toBe('skewed');
    expect(stateAttr(pieceState([40, 30, 30], 4))).toBe('normal');
  });

  it('shortens project names for tight lists only by known prefixes', () => {
    expect(projectShort('www-self-projects-chat-bot')).toBe('chat-bot');
    expect(projectShort('www-self-projects-ops-ops-portal')).toBe('ops-portal');
    expect(projectShort('www-tag')).toBe('tag');
    expect(projectShort('Docker')).toBe('Docker');
  });
});

describe('overview', () => {
  const base: OverviewInput = {
    tokens: 10, previous: 5, days: 1, mainShare: 0.25, reread: 0.97, sub: 0.75, over: 0.15, trend: [],
    today: '2026-11-11', rangeLabel: 'today',
  };

  it('sweeps each fixed arc by its share of the 300 degree track', () => {
    const m = overviewModel(base);
    expect(m.arcs.map((a) => a.key)).toEqual(['reread', 'sub', 'over']);
    expect(m.arcs[0]?.sweep).toBeCloseTo(0.97 * OV_TRACK);
    expect(overviewModel({ ...base, reread: 3 }).arcs[0]?.sweep).toBe(OV_TRACK);
    expect(overviewModel({ ...base, reread: null }).arcs[0]?.sweep).toBe(0);
  });

  it('keeps 14 day slots ending today and leaves missing days null', () => {
    const slots = trendSlots(
      [{ day: '2026-11-11', tokens: 5 }, { day: '2026-10-29', tokens: 3 }, { day: '2026-10-01', tokens: 9 }],
      '2026-11-11',
    );
    expect(slots).toHaveLength(TREND_SLOTS);
    expect(slots[0]).toBe(3);
    expect(slots[13]).toBe(5);
    expect(slots.filter((v) => v === null)).toHaveLength(12);
  });

  it('splits the line at a gap and scales to the largest day', () => {
    const { runs, max } = trendRuns([1, 2, null, 4], 30, 10);
    expect(max).toBe(4);
    expect(runs).toHaveLength(2);
    expect(runs[1]?.[0]).toEqual({ slot: 3, x: 30, y: 0, value: 4 });
  });

  it('defines its states: no days, one day, more days than slots', () => {
    expect(overviewModel(base).state.empty).toBe(true);
    expect(overviewModel({ ...base, trend: [{ day: '2026-11-11', tokens: 1 }] }).state.single).toBe(true);
    const month = Array.from({ length: 30 }, (_, i) => ({
      day: `2026-10-${String(i + 1).padStart(2, '0')}`, tokens: 1,
    }));
    expect(overviewModel({ ...base, trend: month }).state.capped).toBe(true);
  });
});

describe('tokens', () => {
  const kinds = [
    { kind: 'input', tokens: 17_176, costUsd: 0.05 },
    { kind: 'cacheWrite', tokens: 27_255_180, costUsd: 125 },
    { kind: 'cacheRead', tokens: 1_017_865_009, costUsd: 203 },
    { kind: 'output', tokens: 7_113_279, costUsd: 115 },
  ];
  const input = { kinds, tokens: 1_052_250_644, costUsd: 444, previousCost: 409, days: 1, rangeLabel: 'today' };

  it('keeps the four kinds in fixed slot order whatever the response order', () => {
    expect(tokensModel(input).slices.map((s) => s.key)).toEqual(['cacheRead', 'cacheWrite', 'output', 'input']);
  });

  it('draws true proportions with a 1 degree minimum and lists slices under 5%', () => {
    const m = tokensModel(input);
    expect(m.slices[3]?.span.sweep).toBe(1);
    expect(sum(m.slices.map((s) => s.span.sweep))).toBeCloseTo(360, 1);
    expect(m.small.map((s) => s.key)).toEqual(['cacheWrite', 'output', 'input']);
    expect(m.big.map((s) => s.key)).toEqual(['cacheRead']);
  });

  it('zooms the share outside the largest slice to a full circle', () => {
    const m = tokensModel(input);
    expect(m.zoom?.of.key).toBe('cacheRead');
    expect(m.zoom?.factor).toBe(31);
    expect(sum(m.zoom?.spans.map((s) => s.sweep) ?? [])).toBeCloseTo(360, 1);
    expect(m.state.skewed).toBe(true);
  });

  it('has no zoom ring for one kind or no data', () => {
    expect(tokensModel({ ...input, kinds: [kinds[2] ?? kinds[0]!] }).zoom).toBeNull();
    const empty = tokensModel({ ...input, kinds: [] });
    expect(empty.zoom).toBeNull();
    expect(empty.state.empty).toBe(true);
    expect(empty.small).toEqual([]);
  });
});

describe('context', () => {
  const defs: BandDef[] = [
    { minTokens: 0, maxTokens: 100_000 },
    { minTokens: 100_000, maxTokens: 200_000 },
    { minTokens: 200_000, maxTokens: 400_000 },
    { minTokens: 400_000, maxTokens: 700_000 },
    { minTokens: 700_000, maxTokens: null },
  ];
  const row = (minTokens: number, calls: number) => ({ minTokens, calls, contextTokens: 100, cacheReadTokens: 90 });

  it('puts the five bands on fixed rings, teal under the limit and coral above', () => {
    const m = contextModel({
      defs, bands: [row(0, 41), row(100_000, 44), row(200_000, 15)], avgPerCall: 1, compactions: 0, rangeLabel: 'today',
    });
    expect(m.rings.map((r) => r.r)).toEqual([0, 1, 2, 3, 4].map(ringRadius));
    expect(m.rings.map((r) => r.tone)).toEqual(['live', 'live', 'warn', 'warn', 'warn']);
    expect(m.rings[0]?.sweep).toBeCloseTo(0.41 * 360);
    expect(m.rings[3]?.sweep).toBe(EMPTY_SWEEP);
    expect(m.limitR).toBe(ringRadius(1) + 11);
    expect(m.rings[0]?.reread).toBeCloseTo(0.9);
  });

  it('folds band definitions past the fifth into the outer ring', () => {
    const more = [
      ...defs.slice(0, 4), { minTokens: 700_000, maxTokens: 900_000 }, { minTokens: 900_000, maxTokens: null },
    ];
    const m = contextModel({
      defs: more, bands: [row(700_000, 3), row(900_000, 2)], avgPerCall: 1, compactions: 0, rangeLabel: 'all',
    });
    expect(m.rings).toHaveLength(CONTEXT_RINGS);
    expect(m.rings[4]?.calls).toBe(5);
    expect(m.rings[4]?.band.maxTokens).toBeNull();
    expect(m.state.capped).toBe(true);
    expect(m.state.single).toBe(true);
  });

  it('keeps a tiny non-zero band visible', () => {
    const m = contextModel({
      defs, bands: [row(0, 10_000), row(700_000, 1)], avgPerCall: 1, compactions: 0, rangeLabel: 'x',
    });
    expect(m.rings[4]?.sweep).toBe(MIN_SWEEP);
    expect(m.state.skewed).toBe(true);
  });
});

describe('sessions', () => {
  const s = (i: number, tokens: number, state: SessionItem['state'] = 'ended'): SessionItem => ({
    key: `k${i}`, project: `p${i % 6}`, tokens, state,
  });

  it('fills 12 slots clockwise from 12 o\'clock in token order', () => {
    const m = sessionsModel({ sessions: [s(1, 5), s(2, 50), s(3, 20)], projects: [], total: 3, live: 1 });
    const c = SESSION_BOX / 2;
    expect(m.slots).toHaveLength(SESSION_SLOTS);
    expect(m.slots[0]).toMatchObject({ angle: 0, x: c, y: c - SESSION_R });
    expect(m.slots[3]).toMatchObject({ angle: 90, x: c + SESSION_R, y: c });
    expect(m.slots.map((x) => x.item?.tokens ?? null).slice(0, 4)).toEqual([50, 20, 5, null]);
    expect(m.more).toBe(0);
  });

  it('sizes dots in three steps', () => {
    expect([
      dotSize(100, 100), dotSize(25, 100), dotSize(10, 100), dotSize(1, 100), dotSize(0, 0),
    ]).toEqual([32, 32, 26, 20, 20]);
  });

  it('counts the rest as +N more past the cap and colours the top four projects', () => {
    const sessions = Array.from({ length: 30 }, (_, i) => s(i, 1000 - i));
    const projects = Array.from({ length: 6 }, (_, i) => ({ project: `p${i}`, sessions: 5, tokens: 100 - i }));
    const m = sessionsModel({ sessions, projects, total: 100, live: 4 });
    expect(m.more).toBe(88);
    expect(m.state.capped).toBe(true);
    expect(m.legend.map((l) => l.tone)).toEqual(['live', 'gold', 'session', 'warn', 'dim']);
    expect(m.legend.at(-1)).toMatchObject({ project: 'other', sessions: 10 });
    expect(m.slots[4]?.item?.tone).toBe('dim');
  });

  it('defines no data and one session', () => {
    expect(sessionsModel({ sessions: [], projects: [], total: 0, live: 0 }).state.empty).toBe(true);
    const one = sessionsModel({ sessions: [s(0, 9, 'live')], projects: [], total: 1, live: 1 });
    expect(one.state.single).toBe(true);
    expect(one.liveShare).toBe(1);
  });
});

describe('subagents', () => {
  it('puts the eight largest types on fixed spokes and sums the rest into other', () => {
    const types = Array.from({ length: 12 }, (_, i) => ({ name: `t${i}`, runs: 120 - i * 10 }));
    const m = subagentsModel({ types, mainShare: 0.25, rangeLabel: 'today' });
    expect(m.spokes).toHaveLength(SPOKES);
    expect(m.spokes.map((sp) => sp.angle)).toEqual([0, 45, 90, 135, 180, 225, 270, 315]);
    expect(m.spokes[0]?.len).toBe(SPOKE_MAX);
    expect(m.other).toEqual({ types: 4, runs: 40 + 30 + 20 + 10, share: 100 / sum(types.map((t) => t.runs)) });
  });

  it('keeps a minimum spoke and leaves unused spokes empty', () => {
    const m = subagentsModel({
      types: [{ name: 'a', runs: 1000 }, { name: 'b', runs: 1 }], mainShare: null, rangeLabel: 'x',
    });
    expect(m.spokes[1]?.len).toBe(SPOKE_MIN);
    expect(m.spokes[2]?.item).toBeNull();
    expect(m.state.skewed).toBe(true);
    expect(m.other).toBeNull();
  });
});

describe('fills', () => {
  it('gives a run of narrow same-side slices one shared anchor at the middle of their arc', () => {
    const at = (key: string, start: number, sweep: number) => ({
      key, span: { start, sweep }, side: start >= 180 ? 'left' as const : 'right' as const,
    });
    const a = leaderAnchors([at('big', 0, 330), at('s1', 330, 5), at('s2', 335, 4), at('s3', 339, 21)]);
    expect(a.map((x) => x.shared)).toEqual([false, true, true, false]);
    expect(a[1]?.angle).toBe(334.5);
    expect(a[2]?.angle).toBe(334.5);
    expect(a[3]?.angle).toBe(349.5);
    expect(leaderAnchors([at('lone', 200, 3)])).toEqual([{ key: 'lone', angle: 201.5, shared: false }]);
  });
  const tool = (i: number, w: number): FillTool => ({
    tool: `Bash: t${i}`, tokens: w / 40, weightedTokens: w, results: i,
  });

  it('ranks the top six from 12 o\'clock and folds the rest into one small-tools slice', () => {
    const tools = Array.from({ length: 20 }, (_, i) => tool(i, 1000 - i * 40));
    const m = fillsModel({ tools, contextShare: 0.18, resultTokens: 1, rangeLabel: 'today' });
    expect(m.slices).toHaveLength(FILL_TOP);
    expect(m.slices[0]?.span.start).toBe(0);
    expect(m.small?.tools).toBe(14);
    expect(m.tail).toHaveLength(FILL_TAIL_ROWS);
    expect(m.more).toBe(9);
    const all = [...m.slices.map((s) => s.span.sweep), m.small?.span.sweep ?? 0];
    expect(sum(all)).toBeCloseTo(360, 1);
  });

  it('sends each callout to the side its slice sits on', () => {
    const m = fillsModel({
      tools: [tool(0, 50), tool(1, 30), tool(2, 20)], contextShare: null, resultTokens: null, rangeLabel: 'x',
    });
    expect(m.slices.map((s) => s.side)).toEqual(['right', 'left', 'left']);
    expect(m.small).toBeNull();
  });

  it('defines no data and one tool', () => {
    expect(fillsModel({ tools: [], contextShare: null, resultTokens: null, rangeLabel: 'x' }).state.empty).toBe(true);
    const one = fillsModel({ tools: [tool(0, 5)], contextShare: null, resultTokens: null, rangeLabel: 'x' });
    expect(one.slices[0]?.span.sweep).toBe(360);
  });
});

describe('guard', () => {
  const base: GuardInput = {
    blocked: null,
    hours: null,
    bash: null,
    file: null,
    subShare: null,
    lastBlock: null,
    active: null,
    version: null,
    mode: null,
  };

  it('always draws 24 hour segments, dim without a log', () => {
    const m = guardModel(base);
    expect(m.segments).toHaveLength(GUARD_SEGMENTS);
    expect(m.waiting).toBe(true);
    expect(m.state.empty).toBe(true);
    expect(m.segments.every((s) => s.status === 'none')).toBe(true);
  });

  it('marks an hour with calls teal and one with a block coral', () => {
    const m = guardModel({ ...base, hours: [{ hour: 3, calls: 10, blocked: 0 }, { hour: 9, calls: 5, blocked: 2 }] });
    expect(m.waiting).toBe(false);
    expect(m.segments[3]?.status).toBe('calls');
    expect(m.segments[9]?.status).toBe('block');
    expect(m.segments[10]?.status).toBe('none');
    expect(guardModel({ ...base, hours: [{ hour: 3, calls: 10, blocked: 0 }] }).state.single).toBe(true);
  });
});

describe('machine', () => {
  const core = (i: number, kind: CoreRow['kind'], busyPct: number): CoreRow => ({ core: i, kind, busyPct });

  it('puts performance cores first from 12 o\'clock and sizes wedges by load only', () => {
    const cores = [core(0, 'efficiency', 50), core(4, 'performance', 100), core(5, 'performance', 0)];
    const m = machineModel({ cores, cpuBusyPct: 50, load: null });
    expect(m.wedges.map((w) => w.name)).toEqual(['P1', 'P2', 'E1']);
    expect(m.wedges.map((w) => w.start)).toEqual([0, 120, 240]);
    expect(m.wedges[0]!.outer).toBeGreaterThan(m.wedges[2]!.outer);
    expect(m.wedges[2]!.outer).toBeGreaterThan(m.wedges[1]!.outer);
  });

  it('shares wedges between cores past the cap', () => {
    const cores = Array.from({ length: 24 }, (_, i) => core(i, i < 8 ? 'efficiency' : 'performance', 50));
    const m = machineModel({ cores, cpuBusyPct: 50, load: null });
    expect(m.wedges).toHaveLength(MACHINE_WEDGES);
    expect(sum(m.wedges.map((w) => w.cores))).toBe(24);
    expect(m.state.capped).toBe(true);
    expect(m.wedges.filter((w) => w.kind === 'performance')).toHaveLength(11);
    expect(m.wedges.map((w) => w.name)).toContain('P3–4');
  });

  it('defines no cores', () => {
    const m = machineModel({ cores: [], cpuBusyPct: null, load: null });
    expect(m.wedges).toEqual([]);
    expect(m.state.empty).toBe(true);
  });
});

describe('docker', () => {
  const c = (name: string, stack: string, memBytes: number, health = 'healthy'): DockerContainer =>
    ({ name, stack, memBytes, health, restarts: 0 });

  it('groups by stack, largest memory first, and folds no-stack containers into one other hex', () => {
    const m = dockerModel({
      containers: [c('a', 'web', 10e6), c('b', 'web', 90e6), c('x', 'none', 5e6, 'none'), c('y', 'none', 7e6)],
      stacks: [],
    });
    expect(m.groups.map((g) => g.title)).toEqual(['web', 'No stack']);
    expect(m.groups[0]?.hexes.map((h) => h.name)).toEqual(['b', 'a']);
    expect(m.groups[1]?.hexes).toHaveLength(1);
    expect(m.groups[1]?.hexes[0]).toMatchObject({ kind: 'other', count: 2, memBytes: 12e6 });
  });

  it('caps a stack at 12 hexes with a +N more hex and three named stacks', () => {
    const many = Array.from({ length: 15 }, (_, i) => c(`w${i}`, 'web', (20 - i) * 1e6));
    const extra = ['s1', 's2', 's3'].map((s) => c(`${s}-c`, s, 1e6));
    const m = dockerModel({ containers: [...many, ...extra], stacks: [] });
    expect(m.groups).toHaveLength(STACK_GROUPS + 1);
    const web = m.groups.find((g) => g.title === 'web')!;
    expect(web.hexes).toHaveLength(HEX_PER_STACK);
    expect(web.hexes.at(-1)).toMatchObject({ kind: 'more', name: '+4 more', count: 4 });
    expect(web.cols).toBe(4);
    expect(web.hexes[4]).toMatchObject({ row: 1, col: 0, x: 33 });
    expect(m.groups.at(-1)?.title).toBe('Other stacks');
    expect(m.state.capped).toBe(true);
  });

  it('puts memory on a log scale with a 4% floor', () => {
    expect(memBar(0)).toBe(0);
    expect(memBar(1)).toBe(0.04);
    expect(memBar(1024 ** 3)).toBe(1);
  });

  it('defines no containers', () => {
    const m = dockerModel({ containers: [], stacks: [] });
    expect(m.groups).toEqual([]);
    expect(m.state.empty).toBe(true);
  });
});
