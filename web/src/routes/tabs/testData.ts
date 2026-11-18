import type { Schemas } from '../../api';

/** Test-only fixtures. Every object carries free-text canary fields and some labels break the schema patterns. */
export const CANARY = 'CANARY';

const leak = { prompt: `${CANARY} prompt`, command: `${CANARY} cmd`, cwd: `/Users/${CANARY}/x`, output: `${CANARY} out` };
function tainted<T extends object>(o: T): T {
  return Object.assign(o, leak);
}

const H = 3_600_000;
const ago = (hours: number, now: number) => new Date(now - hours * H).toISOString();

const period: Schemas['Period'] = {
  from: '2026-11-01',
  to: '2026-11-07',
  days: 7,
  previousFrom: '2026-10-25',
  previousTo: '2026-10-31',
  partial: false,
  previousPartial: false,
};

const BAD_PROJECT = `/Users/${CANARY}/repo`;
const BAD_MODEL = `${CANARY} model`;
const BAD_AGENT = `${CANARY} spaced agent`;

export function fixtures(now: number) {
  const bands = [
    { minTokens: 0, maxTokens: 100_000 },
    { minTokens: 700_000, maxTokens: null },
  ];
  const compaction: Schemas['Compaction'] = tainted({
    at: ago(2, now),
    project: 'benchwatch',
    session: '0a1b2c3d',
    scope: 'main',
    trigger: 'auto',
    tokensBefore: 180_000,
    tokensAfter: 40_000,
  });
  const overview: Schemas['OverviewResponse'] = tainted({
    claudeOnly: { available: true },
    byClient: [{ client: 'claude-code', tokens: 1_284_000, costUsd: 12.346, share: 1 }],
    unpriced: [],
    period: tainted({ ...period }),
    tokens: tainted({ value: 1_284_000, previous: 1_000_000 }),
    costUsd: { value: 12.346, previous: 10 },
    calls: { value: 4321, previous: 4000 },
    rereadShare: { value: 0.96, previous: 0.9 },
    rereadTarget: 0.15,
    scopeShare: { main: 0.7, sub: 0.3 },
    tokenMix: tainted({ input: 1000, cacheWrite: 2000, cacheRead: 9000, output: 500 }),
    daily: [
      tainted({ day: '2026-11-02', mainTokens: 600_000, subTokens: 100_000, costUsd: 7, calls: 2000, rereadShare: 0.9 }),
      { day: '2026-11-05', mainTokens: 400_000, subTokens: 184_000, costUsd: 5.346, calls: 2321, rereadShare: 0.6 },
    ],
    peakDay: null,
    contextBands: bands,
    bands: [
      tainted({ band: bands[0] ?? { minTokens: 0, maxTokens: null }, mainCalls: 300, subCalls: 120 }),
      { band: bands[1] ?? { minTokens: 0, maxTokens: null }, mainCalls: 4, subCalls: 0 },
    ],
    liveSessions: [
      tainted({
        client: 'claude-code' as const,
        session: '0a1b2c3d',
        project: 'benchwatch',
        state: 'live' as const,
        model: 'claude-opus-5-5',
        startedAt: ago(1, now),
        lastActivityAt: ago(0, now),
        contextTokens: 171_200,
        contextLimitTokens: 200_000,
        tokens: 500_000,
        subagentRuns: 3,
      }),
      tainted({
        client: 'claude-code' as const,
        session: 'deadbeef',
        project: BAD_PROJECT,
        state: 'idle' as const,
        model: BAD_MODEL,
        startedAt: ago(5, now),
        lastActivityAt: ago(4, now),
        contextTokens: 20_000,
        contextLimitTokens: 200_000,
        tokens: 30_000,
        subagentRuns: 0,
      }),
    ],
    sessionMap: tainted({ sessions: 12, live: 1, withSubagents: 4 }),
    compactions: tainted({ count: 1, avgFreedTokens: 140_000, byDay: [{ day: '2026-11-07', count: 1 }], recent: [compaction] }),
  });
  const peakDay: Schemas['DayCost'] = tainted({ day: '2026-11-03', tokens: 5000, costUsd: 600, topModel: 'claude-opus-5-5' });
  const tokens: Schemas['TokensResponse'] = tainted({
    period: { ...period },
    byClient: [{ client: 'claude-code', tokens: 9600, costUsd: 1234.6, share: 1 }],
    unpriced: [],
    tokens: { value: 9600, previous: null },
    costUsd: { value: 1234.6, previous: 1000 },
    avgCostPerDayUsd: 176.37,
    peakCostDay: peakDay,
    byModel: [
      tainted({ model: 'claude-opus-5-5', tokens: 8000, costUsd: 1000, share: 0.8 }),
      tainted({ model: BAD_MODEL, tokens: 1600, costUsd: 234.6, share: 0.2 }),
    ],
    byKind: [
      tainted({ kind: 'cacheRead' as const, tokens: 6000, costUsd: 200 }),
      { kind: 'cacheWrite' as const, tokens: 2000, costUsd: 100 },
      { kind: 'input' as const, tokens: 1000, costUsd: 34.6 },
      { kind: 'output' as const, tokens: 600, costUsd: 900 },
    ],
    recorded: { clients: [], tokens: 0, costUsd: 0 },
    daily: [peakDay, { day: '2026-11-07', tokens: 4600, costUsd: 634.6, topModel: BAD_MODEL }],
    topDays: [peakDay],
  });
  const context: Schemas['ContextResponse'] = tainted({
    period: { ...period },
    peak: tainted({ contextTokens: 965_599, contextLimitTokens: 200_000, project: 'benchwatch', day: '2026-11-04' }),
    avgContextPerCall: { value: 45_000, previous: 40_000 },
    rereadShare: { value: 0.96, previous: null },
    rereadTarget: 0.15,
    byProject: [
      tainted({ project: 'benchwatch', sessions: 5, compactions: 2, peakContextTokens: 965_599, contextLimitTokens: 200_000 }),
      tainted({ project: BAD_PROJECT, sessions: 1, compactions: 0, peakContextTokens: 50_000, contextLimitTokens: 200_000 }),
    ],
    contextBands: bands,
    bands: [
      tainted({ band: bands[0] ?? { minTokens: 0, maxTokens: null }, calls: 300, contextTokens: 1_000_000, cacheReadTokens: 900_000 }),
      { band: bands[1] ?? { minTokens: 0, maxTokens: null }, calls: 4, contextTokens: 3_000_000, cacheReadTokens: 2_850_000 },
    ],
    daily: [tainted({ day: '2026-11-04', peakContextTokens: 965_599, compactions: 2, rereadShare: 0.95, avgContextTokens: 321_000 })],
    compactions: tainted({ count: 2, avgFreedTokens: 120_000, byDay: [{ day: '2026-11-04', count: 2 }], recent: [compaction] }),
  });
  const summary = (session: string, project: string, state: Schemas['SessionState'], start: number): Schemas['SessionSummary'] =>
    tainted({
      client: 'claude-code',
      session,
      project,
      state,
      model: 'claude-opus-5-5',
      startedAt: ago(start, now),
      lastActivityAt: ago(state === 'live' ? 0 : start - 1, now),
      tokens: state === 'live' ? 700_000 : 300_000,
      peakContextTokens: 171_200,
      contextLimitTokens: 200_000,
      calls: 120,
      costUsd: 4.2,
      subagentRuns: 2,
      compactions: 1,
    });
  const sessions: Schemas['SessionsResponse'] = tainted({
    claudeOnly: { available: true },
    period: { ...period, from: '2026-11-07', days: 1 },
    totals: tainted({ sessions: 3, live: 1, subagentRuns: 5, compactions: 1 }),
    projects: [
      tainted({ project: 'benchwatch', sessions: 2, tokens: 700_000, share: 0.7 }),
      tainted({ project: BAD_PROJECT, sessions: 1, tokens: 300_000, share: 0.3 }),
    ],
    sessions: [summary('0a1b2c3d', 'benchwatch', 'live', 3), summary('deadbeef', BAD_PROJECT, 'ended', 6)],
    compactions: tainted({ count: 1, avgFreedTokens: 140_000, byDay: [{ day: '2026-11-07', count: 1 }], recent: [compaction] }),
  });
  const detail: Schemas['SessionDetailResponse'] = tainted({
    claudeOnly: { available: true },
    summary: summary('0a1b2c3d', 'benchwatch', 'live', 3),
    contextTokens: 150_000,
    tokenMix: { input: 1000, cacheWrite: 2000, cacheRead: 9000, output: 500 },
    subagents: [tainted({ agentType: 'code-reviewer', runs: 2, tokens: 90_000 }), tainted({ agentType: BAD_AGENT, runs: 1, tokens: 1000 })],
    compactions: [compaction],
  });
  const type = (agentType: string, runs: number, tokensUsed: number): Schemas['AgentTypeStats'] =>
    tainted({
      agentType,
      topModel: agentType === BAD_AGENT ? BAD_MODEL : 'claude-opus-5-5',
      runs,
      calls: runs * 10,
      tokens: tokensUsed,
      cacheReadTokens: 500_000,
      contextTokens: 1_000_000,
      avgFirstContextTokens: 12_000,
      avgPeakContextTokens: 80_000,
      costUsd: 3,
    });
  const subagents: Schemas['SubagentsResponse'] = tainted({
    period: { ...period },
    runs: { value: 40, previous: 52 },
    scopeShare: { main: 0.7, sub: 0.3 },
    costUsd: 3.5,
    totalCostUsd: 12.346,
    rereadTarget: 0.15,
    types: [type('code-reviewer', 30, 900_000), type(BAD_AGENT, 10, 100_000)],
    daily: [tainted({ day: '2026-11-06', agentType: 'code-reviewer', runs: 4 })],
    scopeDaily: [tainted({ day: '2026-11-06', subShare: 0.3, subCostUsd: 1.5 })],
  });
  const meta: Schemas['MetaResponse'] = tainted({
    clients: [{ id: 'claude-code', verified: true }],
    generatedAt: new Date(now).toISOString(),
    firstDay: '2026-10-01',
    lastDay: '2026-11-07',
    projects: ['benchwatch', BAD_PROJECT],
    models: ['claude-opus-5-5'],
    agentTypes: ['code-reviewer'],
    tools: [],
    stacks: ['stack_full', 'tools'],
    contextBands: bands,
    resultSizeBands: [],
    rereadTarget: 0.15,
    retention: [],
  });
  const stat = (tool: string, results: number, tokensUsed: number): Schemas['ToolStats'] =>
    tainted({ tool, results, tokens: tokensUsed, weightedTokens: tokensUsed * 3, maxTokens: 40_000, p95Tokens: 9_000, repeatResults: results / 5, repeatMeasuredResults: results, costUsd: 1.5 });
  const fills = tainted({
    period: { ...period },
    group: 'tool',
    results: { value: 300, previous: 250 },
    tokens: 600_000,
    contextShare: 0.4,
    largest: { tool: 'Read: whole', project: 'benchwatch', day: '2026-11-04', tokens: 40_000 },
    tools: [stat('Read: whole', 100, 400_000), stat('Read: ranged', 50, 50_000), stat('Grep', 120, 100_000), stat('Bash: git', 30, 50_000)],
    groups: [
      { tool: 'Read: whole', results: 100, tokens: 400_000, weightedTokens: 1_200_000 },
      { tool: 'Grep', results: 120, tokens: 100_000, weightedTokens: 300_000 },
    ],
    top: [tainted({ result: 'abcdef0123456789', tool: 'Read: whole', project: 'benchwatch', day: '2026-11-04', tokens: 40_000, resends: 5, weightedTokens: 200_000 })],
    daily: [{ day: '2026-11-06', tool: 'Read: whole', tokens: 30_000, results: 7 }],
    byProject: [
      { project: 'benchwatch', weightedTokens: 900_000, tools: [{ tool: 'Read: whole', weightedTokens: 720_000 }, { tool: 'Grep', weightedTokens: 180_000 }] },
      { project: 'other', weightedTokens: 600_000, tools: [{ tool: 'Grep', weightedTokens: 600_000 }] },
    ],
    resultSizeBands: [],
  }) as unknown as Schemas['FillsResponse'];
  const guard = tainted({
    period: { ...period },
    calls: { value: 840, previous: 700 },
    log: { state: 'ok', lastReadAt: ago(1, now), skipped: { malformed: 0, oversized: 0, badVersion: 0, badRule: 0, notRegular: 0 } },
    decisions: { allow: 800, ask: 30, deny: 10, failClosed: 2, timeCap: 1, subDeny: 3 },
    byTool: [
      { tool: 'bash', calls: 600, ask: 20, deny: 8, p50Ms: 3, p95Ms: 9, p99Ms: 20 },
      { tool: 'file', calls: 240, ask: 10, deny: 2, p50Ms: 2, p95Ms: 6, p99Ms: null },
    ],
    subAgent: { calls: 840, sub: 210 },
    callsByDay: [{ day: '2026-11-06', calls: 120, ask: 4, deny: 2 }, { day: '2026-11-07', calls: 140, ask: 5, deny: 0 }],
    hours: Array.from({ length: 24 }, (_, hour) => ({ hour, calls: hour < 13 ? 20 + hour : 0, deny: hour === 9 ? 2 : 0 })),
    rules: [
      { rule: 'git-force-push', tool: 'bash', decision: 'deny', calls: 6 },
      { rule: 'env-file-read', tool: 'file', decision: 'ask', calls: 4 },
    ],
    lastBlock: { at: ago(2, now), tool: 'bash', rule: 'git-force-push' },
    install: { source: 'system', bash: 'go', file: 'node', installedAt: ago(30, now), build: 'a1b2c3d4e5f6' },
    trend: [
      { day: '2026-11-05', results: 200, blocked: 4 },
      { day: '2026-11-06', results: 300, blocked: 6 },
    ],
    toolErrors: [{ tool: 'Bash: git', errorClass: 'other', errors: 3, results: 30 }],
  }) as unknown as Schemas['GuardResponse'];
  const at = new Date(now).toISOString();
  const status = {
    generatedAt: at,
    health: 'nominal',
    scan: { state: 'ok', lastRunAt: at, nextRunInSeconds: 5, toolErrors: 0 },
    machine: { state: 'ok' },
    docker: { state: 'ok' },
    guard: { state: 'ok' },
    database: { historyPaused: false },
    liveSessions: 2,
  } as unknown as Schemas['StatusResponse'];
  const machine = tainted({
    range: '1h',
    resolution: 'raw',
    detailIntervalSeconds: 5,
    coverageFrom: ago(1, now),
    partial: false,
    historyPaused: false,
    now: {
      at,
      cpuBusyPct: 23,
      cores: [
        { core: 0, kind: 'performance', busyPct: 40 },
        { core: 1, kind: 'performance', busyPct: 20 },
        { core: 2, kind: 'efficiency', busyPct: 10 },
      ],
      performanceCores: 2,
      efficiencyCores: 1,
      load: { one: 2.41, five: 2.18, fifteen: 1.96 },
      memory: { totalBytes: 32 * 1024 ** 3, usedBytes: 18 * 1024 ** 3, appBytes: 12 * 1024 ** 3, cachedBytes: 5 * 1024 ** 3, compressedBytes: 1024 ** 3, swapUsedBytes: 0, swapTotalBytes: 1024 ** 3, pressure: 'normal' },
      diskIo: { readBps: 1_000_000, writeBps: 2_000_000 },
      interfaces: [{ interface: 'en0', rxBps: 4_200_000, txBps: 800_000, mergedNames: 0 }],
      volumes: [{ volume: 'macintosh-hd', kind: 'internal', filesystem: 'apfs', totalBytes: 500_000_000_000, freeBytes: 412_000_000_000, mergedNames: 0 }],
      apps: [tainted({ app: 'Chrome', cpuPct: 12, memBytes: 2 * 1024 ** 3 })],
    },
    series: [tainted({ at, cpuBusyPct: 23, cpuBusyMaxPct: 40, load1: 2.4, memUsedBytes: 18 * 1024 ** 3, swapUsedBytes: 0, diskReadBps: 1_000_000, diskWriteBps: 2_000_000 })],
    coreSeries: [],
    interfaceSeries: [{ interface: 'en0', peakRxBps: 9_000_000, peakRxAt: at, points: [{ at: ago(1, now), rxBps: 1, txBps: 1 }, { at, rxBps: 4_200_000, txBps: 800_000 }] }],
  }) as unknown as Schemas['MachineResponse'];
  const container = (name: string, stack: string, mem: number, restarts = 0) =>
    tainted({ container: name, stack, state: 'running', health: 'healthy', cpuPct: 5, memBytes: mem, memLimitBytes: null, rxBps: 1000, txBps: 500, restarts, startedAt: ago(5, now), mergedNames: 0 });
  const docker = tainted({
    range: '1h',
    resolution: 'raw',
    detailIntervalSeconds: 10,
    coverageFrom: ago(1, now),
    partial: false,
    available: true,
    historyPaused: false,
    now: {
      at,
      network: 'stack_full',
      vm: { memTotalBytes: 8 * 1024 ** 3, memUsedBytes: 3 * 1024 ** 3, cpuPct: 12, swapUsedBytes: 0, cpus: 8 },
      counts: { containers: 3, running: 3, healthy: 2, unhealthy: 0, starting: 0, noHealthcheck: 1 },
      containers: [container('php84_service_1', 'stack_full', 900 * 1024 ** 2), container('mariadb_service', 'stack_full', 600 * 1024 ** 2, 1), container('floci_service', 'tools', 100 * 1024 ** 2)],
      stacks: [{ stack: 'stack_full', containers: 2, memBytes: 1500 * 1024 ** 2, rxBps: 2000, txBps: 1000 }, { stack: 'tools', containers: 1, memBytes: 100 * 1024 ** 2, rxBps: 10, txBps: 10 }],
      disk: {
        measuredAt: at,
        stale: false,
        vmDiskBytes: null,
        usedBytes: 20_000_000_000,
        reclaimableBytes: 4_000_000_000,
        images: { bytes: 12_000_000_000, count: 30, unused: 5, reclaimableBytes: 3_000_000_000, largestBytes: 2_000_000_000 },
        volumes: { bytes: 6_000_000_000, count: 12, unused: 2, reclaimableBytes: 500_000_000 },
        buildCache: { bytes: 2_000_000_000, reclaimableBytes: 500_000_000 },
      },
    },
    vmSeries: [],
    containerSeries: [],
    events: [{ at, container: 'mariadb_service', kind: 'restart' }],
  }) as unknown as Schemas['DockerResponse'];
  return { overview, tokens, context, sessions, detail, subagents, meta, fills, guard, machine, docker, status };
}
