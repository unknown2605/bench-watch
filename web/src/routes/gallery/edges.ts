import type { ContextInput } from '../../charts/pieces/context';
import type { DockerContainer, DockerInput } from '../../charts/pieces/docker';
import type { FillsInput } from '../../charts/pieces/fills';
import { GUARD_WAITING, type GuardHour, type GuardInput } from '../../charts/pieces/guard';
import type { CoreRow, MachineInput } from '../../charts/pieces/machine';
import type { OverviewInput } from '../../charts/pieces/overview';
import type { SessionsInput } from '../../charts/pieces/sessions';
import type { SubagentsInput } from '../../charts/pieces/subagents';
import type { TokensInput } from '../../charts/pieces/tokens';
import { dockerFrom, fillsFrom, machineFrom, overviewFrom, sessionsFrom, subagentsFrom } from './recorded';

export const EDGES = [
  { key: 'empty', label: 'No data' },
  { key: 'one', label: 'One item' },
  { key: 'cap', label: 'Cap reached' },
  { key: 'skew', label: 'One item over 80%' },
] as const;
export type EdgeKey = (typeof EDGES)[number]['key'];

const TODAY = '2026-11-11';
const shiftDay = (day: string, by: number) => {
  const t = new Date(`${day}T00:00:00Z`);
  t.setUTCDate(t.getUTCDate() + by);
  return t.toISOString().slice(0, 10);
};

export function overviewEdge(k: EdgeKey): OverviewInput {
  const base = overviewFrom('30d', '30 d');
  const trend = base?.trend ?? Array.from({ length: 30 }, (_, i) =>
    ({ day: shiftDay(TODAY, i - 29), tokens: 4e8 + (i % 5) * 1e8 }));
  const blank: OverviewInput = { tokens: null, previous: null, days: 1, mainShare: null, reread: null, sub: null,
    over: null, trend: [], today: TODAY, rangeLabel: 'today' };
  if (k === 'empty') return blank;
  if (k === 'one') return { ...blank, tokens: 1_052_250_644, previous: null, mainShare: 0.25, reread: 0.97, sub: 0.75,
    over: 0.15, trend: [{ day: TODAY, tokens: 1_052_250_644 }] };
  if (k === 'cap') return { ...(base ?? blank), reread: 1, sub: 1, over: 1, mainShare: 0, trend, rangeLabel: '30 d' };
  const tail = trend.slice(-14).map((d, i) => ({ ...d, tokens: i === 6 ? 9e10 : d.tokens / 50 }));
  return { ...(base ?? blank), reread: 0.999, sub: 0.004, over: 0.002, mainShare: 0.996, trend: tail,
    rangeLabel: '30 d' };
}

export function tokensEdge(k: EdgeKey): TokensInput {
  const base: TokensInput = { kinds: [], tokens: null, costUsd: null, previousCost: null, days: 1,
    rangeLabel: 'today' };
  if (k === 'empty') return base;
  if (k === 'one') return { ...base, kinds: [{ kind: 'cacheRead', tokens: 812_000_000, costUsd: 162.4 }],
    tokens: 812_000_000, costUsd: 162.4 };
  if (k === 'cap') {
    const kinds = [
      { kind: 'cacheRead', tokens: 410e6, costUsd: 82 },
      { kind: 'cacheWrite', tokens: 230e6, costUsd: 1150 },
      { kind: 'output', tokens: 120e6, costUsd: 1800 },
      { kind: 'input', tokens: 90e6, costUsd: 270 },
    ];
    return { ...base, kinds, tokens: 850e6, costUsd: 3302, previousCost: 2900 };
  }
  const kinds = [
    { kind: 'cacheRead', tokens: 9_990_000_000, costUsd: 1998 },
    { kind: 'cacheWrite', tokens: 4_000_000, costUsd: 25 },
    { kind: 'output', tokens: 120_000, costUsd: 9 },
    { kind: 'input', tokens: 40, costUsd: 0.0002 },
  ];
  return { ...base, kinds, tokens: 9_994_120_040, costUsd: 2032, previousCost: 1500, rangeLabel: 'all' };
}

const DEFS = [
  { minTokens: 0, maxTokens: 100_000 },
  { minTokens: 100_000, maxTokens: 200_000 },
  { minTokens: 200_000, maxTokens: 400_000 },
  { minTokens: 400_000, maxTokens: 700_000 },
  { minTokens: 700_000, maxTokens: null },
];

export function contextEdge(k: EdgeKey): ContextInput {
  const base: ContextInput = { defs: DEFS, bands: [], avgPerCall: null, compactions: 0, rangeLabel: 'today' };
  const row = (min: number, calls: number) => ({ minTokens: min, calls, contextTokens: calls * (min + 50_000),
    cacheReadTokens: calls * (min + 40_000) });
  if (k === 'empty') return base;
  if (k === 'one') return { ...base, bands: [row(0, 412)], avgPerCall: 61_200, compactions: 0 };
  if (k === 'cap') {
    const defs = [...DEFS.slice(0, 4), { minTokens: 700_000, maxTokens: 900_000 },
      { minTokens: 900_000, maxTokens: 1_000_000 }, { minTokens: 1_000_000, maxTokens: null }];
    return { ...base, defs, bands: [row(0, 3000), row(100_000, 2800), row(200_000, 2100), row(400_000, 1400),
      row(700_000, 600), row(900_000, 300), row(1_000_000,
      120)], avgPerCall: 288_000, compactions: 141, rangeLabel: 'all' };
  }
  return { ...base, bands: [row(0, 120), row(100_000, 9800), row(200_000, 60), row(700_000, 2)], avgPerCall: 151_000,
    compactions: 12 };
}

export function sessionsEdge(k: EdgeKey): SessionsInput {
  const all = sessionsFrom('all');
  const base: SessionsInput = { sessions: [], projects: [], total: 0, live: 0 };
  if (k === 'empty') return base;
  const first = all?.sessions[0] ?? { key: 'a', project: 'www-self-projects-ops-ops-portal',
    tokens: 2.4e9, state: 'live' as const };
  if (k === 'one') return { sessions: [{ ...first, state: 'live' }],
    projects: [{ project: first.project, sessions: 1, tokens: first.tokens }], total: 1, live: 1 };
  if (k === 'cap') return all ?? base;
  const twelve = (all?.sessions ?? []).slice(0, 12).map((s, i) => ({ ...s, tokens: i === 0 ? 9e10 : s.tokens / 40 }));
  return { sessions: twelve, projects: all?.projects ?? [], total: twelve.length, live: 2 };
}

export function subagentsEdge(k: EdgeKey): SubagentsInput {
  const all = subagentsFrom('all', 'All');
  const base: SubagentsInput = { types: [], mainShare: null, rangeLabel: 'today' };
  if (k === 'empty') return base;
  const longest = [...(all?.types ?? [])].sort((a, b) => b.name.length - a.name.length)[0]
    ?? { name: 'fastapi-reviewer', runs: 1 };
  if (k === 'one') return { types: [{ name: longest.name, runs: 1 }], mainShare: 0.82, rangeLabel: 'today' };
  if (k === 'cap') return all ?? base;
  const types = (all?.types ?? []).slice(0, 8).map((t, i) => ({ ...t, runs: i === 0 ? 4000
    : Math.max(1, Math.round(t.runs / 30)) }));
  return { types, mainShare: 0.05, rangeLabel: 'all' };
}

export function fillsEdge(k: EdgeKey): FillsInput {
  const all = fillsFrom('all', 'All');
  const base: FillsInput = { tools: [], contextShare: null, resultTokens: null, rangeLabel: 'today' };
  if (k === 'empty') return base;
  const tools = all?.tools ?? [];
  if (k === 'one') {
    const t = tools.find((x) => x.tool.startsWith('Read')) ?? { tool: 'Read: whole', tokens: 1e6, weightedTokens: 4e7,
      results: 300 };
    return { tools: [t], contextShare: 0.042, resultTokens: t.tokens, rangeLabel: 'today' };
  }
  if (k === 'cap') return all ?? base;
  const top = [...tools].sort((a, b) => b.weightedTokens - a.weightedTokens);
  const skewed = top.map((t, i) => ({ ...t, weightedTokens: i === 0 ? t.weightedTokens * 60 : t.weightedTokens }));
  return { ...(all ?? base), tools: skewed };
}

export function guardEdge(k: EdgeKey): GuardInput {
  if (k === 'empty') return GUARD_WAITING;
  if (k === 'one') return { ...GUARD_WAITING, blocked: 0, active: true, hours: [{ hour: 13, calls: 40, blocked: 0 }] };
  const hours: GuardHour[] = Array.from({ length: 24 }, (_, hour) =>
    ({ hour, calls: 100 + hour * 40, blocked: hour % 7 === 3 ? 2 : 0 }));
  const blocked = hours.reduce((s, h) => s + h.blocked, 0);
  const full = { ...GUARD_WAITING, active: true, blocked, hours, bash: 5912, file: 1223, subShare: 0.41,
    lastBlock: '13:02' };
  return k === 'cap' ? { ...full, version: 'Go v1.3.0', mode: 'bash=go · file=go' } : { ...full, blocked: 21 };
}

export function machineEdge(k: EdgeKey): MachineInput {
  const live = machineFrom('machine-live');
  const base: MachineInput = { cores: [], cpuBusyPct: null, load: null };
  if (k === 'empty') return base;
  if (k === 'one') return { cores: [{ core: 0, kind: 'performance', busyPct: 37 }], cpuBusyPct: 37,
    load: { one: 0.8, five: 0.6, fifteen: 0.5 } };
  if (k === 'cap') {
    const cores: CoreRow[] = Array.from({ length: 24 }, (_, i) =>
      ({ core: i, kind: i < 8 ? 'efficiency' : 'performance', busyPct: (i * 37) % 100 }));
    return { cores, cpuBusyPct: 48, load: { one: 11.2, five: 9.8, fifteen: 8.1 } };
  }
  const cores = (live?.cores ?? []).map((c, i) => ({ ...c, busyPct: i === 5 ? 100 : 1 }));
  return { cores, cpuBusyPct: 11, load: live?.load ?? null };
}

export function dockerEdge(k: EdgeKey): DockerInput {
  const live = dockerFrom('docker-live');
  const base: DockerInput = { containers: [], stacks: [] };
  if (k === 'empty') return base;
  const containers = live?.containers ?? [];
  if (k === 'one') {
    const c = containers.find((x) => x.name.length > 18) ?? { name: 'loading_balancer_service', stack: 'web-services',
      memBytes: 5.7e8, health: 'healthy', restarts: 0 };
    return { containers: [c], stacks: (live?.stacks ?? []).filter((s) => s.stack === c.stack) };
  }
  if (k === 'cap') {
    const extra: DockerContainer[] = [
      ...Array.from({ length: 4 }, (_, i) => ({ name: `php85_service_${i + 3}`, stack: 'web-services',
        memBytes: 2e7 + i * 1e7, health: i === 2 ? 'unhealthy' : 'healthy', restarts: i === 2 ? 3 : 0 })),
      { name: 'elasticsearch_node_1', stack: 'search-services', memBytes: 9e8, health: 'healthy', restarts: 0 },
      { name: 'kibana_service', stack: 'search-services', memBytes: 4e8, health: 'starting', restarts: 0 },
      { name: 'mongodb_service', stack: 'document-services', memBytes: 2e8, health: 'none', restarts: 0 },
    ];
    return { containers: [...containers, ...extra], stacks: live?.stacks ?? [] };
  }
  const skewed = containers.map((c) => ({ ...c, memBytes: c.name.startsWith('loading_balancer') ? 7.8e9
    : c.memBytes / 20 }));
  return { containers: skewed, stacks: live?.stacks ?? [] };
}
