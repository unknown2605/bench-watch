import type { Schemas } from '../../api';
import type { ContextInput } from '../../charts/pieces/context';
import type { DockerInput } from '../../charts/pieces/docker';
import { pieceHealth } from '../tabs/dockerHealth';
import type { FillsInput } from '../../charts/pieces/fills';
import { GUARD_WAITING, guardPieceFields, type GuardInput } from '../../charts/pieces/guard';
import type { MachineInput } from '../../charts/pieces/machine';
import type { OverviewInput } from '../../charts/pieces/overview';
import type { SessionsInput } from '../../charts/pieces/sessions';
import type { SubagentsInput } from '../../charts/pieces/subagents';
import type { TokensInput } from '../../charts/pieces/tokens';
import { agentLabel, containerLabel, num, numOrNull, projectLabel, rows, sessionLabel, stackLabel,
  toolLabel } from '../../format/labels';

// Recorded responses from the git-ignored gallery-data folder; only the gallery build imports this module.
const FILES = import.meta.glob('/gallery-data/*.json', { eager: true, import: 'default' }) as Record<string, unknown>;

export const RANGES = [
  { key: 'today', label: 'Today' },
  { key: '7d', label: '7 d' },
  { key: '30d', label: '30 d' },
  { key: 'all', label: 'All' },
] as const;
export type RangeKey = (typeof RANGES)[number]['key'];

export const MACHINE_RANGES = [
  { key: 'live', label: 'Live', file: 'machine-live' },
  { key: '1h', label: '1 h', file: 'machine-1h' },
  { key: '24h', label: '24 h', file: 'machine-24h' },
  { key: '7d', label: '7 d', file: 'machine-7d' },
] as const;
export const DOCKER_RANGES = [
  { key: 'live', label: 'Live', file: 'docker-live' },
  { key: '1h', label: '1 h', file: 'docker' },
] as const;

export const hasRecorded = () => Object.keys(FILES).length > 0;

function file<T>(name: string): T | null {
  return (FILES[`/gallery-data/${name}.json`] as T | undefined) ?? null;
}

const lower = (label: string) => (label === 'Today' ? 'today' : label);

export function overviewFrom(range: RangeKey, label: string): OverviewInput | null {
  const t = file<Schemas['TokensResponse']>(`tokens-${range}`);
  const c = file<Schemas['ContextResponse']>(`context-${range}`);
  const s = file<Schemas['SubagentsResponse']>(`subagents-${range}`);
  const trend = file<Schemas['TokensResponse']>('tokens-30d');
  if (!t || !c || !s || !trend) return null;
  const bands = rows(c.bands);
  const calls = bands.reduce((a, b) => a + num(b.calls), 0);
  const over = bands.filter((b) => num(b.band.minTokens) >= 200_000).reduce((a, b) => a + num(b.calls), 0);
  return {
    tokens: num(t.tokens.value),
    previous: numOrNull(t.tokens.previous),
    days: num(t.period.days),
    mainShare: numOrNull(s.scopeShare.main),
    reread: numOrNull(c.rereadShare.value),
    sub: numOrNull(s.scopeShare.sub),
    over: calls > 0 ? over / calls : null,
    trend: rows(trend.daily).map((d) => ({ day: d.day, tokens: num(d.tokens) })),
    today: trend.period.to,
    rangeLabel: lower(label),
  };
}

export function tokensFrom(range: RangeKey, label: string): TokensInput | null {
  const t = file<Schemas['TokensResponse']>(`tokens-${range}`);
  if (!t) return null;
  return {
    kinds: rows(t.byKind).map((k) => ({ kind: k.kind, tokens: num(k.tokens), costUsd: num(k.costUsd) })),
    tokens: num(t.tokens.value),
    costUsd: num(t.costUsd.value),
    previousCost: numOrNull(t.costUsd.previous),
    days: num(t.period.days),
    rangeLabel: lower(label),
  };
}

export function contextFrom(range: RangeKey, label: string): ContextInput | null {
  const c = file<Schemas['ContextResponse']>(`context-${range}`);
  if (!c) return null;
  return {
    defs: rows(c.contextBands).map((b) => ({ minTokens: num(b.minTokens), maxTokens: numOrNull(b.maxTokens) })),
    bands: rows(c.bands).map((b) => ({
      minTokens: num(b.band.minTokens),
      calls: num(b.calls),
      contextTokens: num(b.contextTokens),
      cacheReadTokens: num(b.cacheReadTokens),
    })),
    avgPerCall: numOrNull(c.avgContextPerCall.value),
    compactions: num(c.compactions.count),
    rangeLabel: lower(label),
  };
}

export function sessionsFrom(range: RangeKey): SessionsInput | null {
  const s = file<Schemas['SessionsResponse']>(`sessions-${range}`);
  if (!s) return null;
  return {
    sessions: rows(s.sessions).map((x) => ({
      key: `${projectLabel(x.project)}/${sessionLabel(x.session)}`,
      project: projectLabel(x.project),
      tokens: num(x.tokens),
      state: x.state === 'live' ? 'live' : x.state === 'idle' ? 'idle' : 'ended',
    })),
    projects: rows(s.projects).map((p) => ({ project: projectLabel(p.project), sessions: num(p.sessions),
      tokens: num(p.tokens) })),
    total: num(s.totals.sessions),
    live: num(s.totals.live),
  };
}

export function subagentsFrom(range: RangeKey, label: string): SubagentsInput | null {
  const s = file<Schemas['SubagentsResponse']>(`subagents-${range}`);
  if (!s) return null;
  return {
    types: rows(s.types).map((t) => ({ name: agentLabel(t.agentType), runs: num(t.runs) })),
    mainShare: numOrNull(s.scopeShare.main),
    rangeLabel: lower(label),
  };
}

export function fillsFrom(range: RangeKey, label: string): FillsInput | null {
  const f = file<Schemas['FillsResponse']>(`fills-${range}`);
  if (!f) return null;
  return {
    tools: rows(f.tools).map((t) => ({ tool: toolLabel(t.tool), tokens: num(t.tokens),
      weightedTokens: num(t.weightedTokens), results: num(t.results) })),
    contextShare: numOrNull(f.contextShare),
    resultTokens: num(f.tokens),
    rangeLabel: lower(label),
  };
}

export function guardFrom(range: RangeKey): GuardInput | null {
  const g = file<Schemas['GuardResponse']>(`guard-${range}`);
  if (!g) return null;
  return { ...GUARD_WAITING, ...guardPieceFields(g, new Date()) };
}

export function machineFrom(name: string): MachineInput | null {
  const m = file<Schemas['MachineResponse']>(name);
  if (!m?.now) return null;
  return {
    cores: rows(m.now.cores).map((c) => ({ core: num(c.core), kind: c.kind === 'performance' ? 'performance'
      : 'efficiency', busyPct: num(c.busyPct) })),
    cpuBusyPct: numOrNull(m.now.cpuBusyPct),
    load: m.now.load ? { one: num(m.now.load.one), five: num(m.now.load.five), fifteen: num(m.now.load.fifteen) }
      : null,
  };
}

export function dockerFrom(name: string): DockerInput | null {
  return dockerInputOf(file<Schemas['DockerResponse']>(name));
}

export function dockerInputOf(d: Schemas['DockerResponse'] | null): DockerInput | null {
  if (!d?.now) return null;
  return {
    containers: rows(d.now.containers).map((c) => ({
      name: containerLabel(c.container),
      stack: stackLabel(c.stack),
      memBytes: num(c.memBytes),
      health: pieceHealth(c),
      restarts: num(c.restarts),
    })),
    stacks: rows(d.now.stacks).map((s) => ({ stack: stackLabel(s.stack), memBytes: num(s.memBytes),
      rxBps: num(s.rxBps), txBps: num(s.txBps) })),
  };
}
