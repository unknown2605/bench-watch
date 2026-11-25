import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Schemas } from '../../api';
import { resetFilters, setMetricsRange } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { healthOf } from '../../charts/pieces/docker';
import { containerTrend, dockerView } from './Docker';
import { badEvents } from './dockerEvents';
import { machineView } from './Machine';
import { fixtures } from './testData';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

type Fixtures = ReturnType<typeof fixtures>;
let data: Fixtures;
let root: Root | null = null;
let host: HTMLElement | null = null;
let client: QueryClient;

beforeAll(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  HTMLCanvasElement.prototype.getContext = () => null;
  window.scrollTo = () => undefined;
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    const name = new URL(String(input), 'http://app.test').pathname.replace('/api/', '') as keyof Fixtures;
    const body = data[name];
    return new Response(JSON.stringify(body ?? { code: 'not_found' }), {
      status: body ? 200 : 404,
      headers: { 'Content-Type': 'application/json' },
    });
  });
});

beforeEach(() => {
  data = fixtures(Date.now());
  resetFilters();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(path: string) {
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  host = document.body.appendChild(document.createElement('div'));
  root = createRoot(host);
  await act(async () => {
    root?.render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
  });
  await vi.waitFor(() => expect(piece()).not.toBeNull());
}

/** The piece on show; a cross-fade keeps the leaving one under an aria-hidden layer until its animation ends. */
const piece = () => [...(host?.querySelectorAll('[data-piece]') ?? [])].find((el) => !el.closest('[aria-hidden="true"]')) ?? null;
const marks = () => [...(piece()?.querySelectorAll('[role="img"][aria-label]') ?? [])];

describe('Machine centre piece', () => {
  it('feeds the piece the cores, CPU and load of the response', () => {
    const v = machineView(data.machine);
    expect(v.piece).toEqual({
      cores: data.machine.now?.cores.map((c) => ({ core: c.core, kind: c.kind, busyPct: c.busyPct })),
      cpuBusyPct: 23,
      load: { one: 2.41, five: 2.18, fifteen: 1.96 },
    });
  });

  it('draws the response and keeps the same piece nodes on a live sample', async () => {
    await mount('/machine');
    expect(piece()?.getAttribute('data-piece')).toBe('machine');
    expect(piece()?.textContent).toContain('LOAD 2.41');
    const before = piece();
    const wedges = marks();
    expect(wedges.length).toBeGreaterThan(0);
    if (data.machine.now) {
      data.machine.now.cpuBusyPct = 61;
      data.machine.now.load = { one: 5.5, five: 2.18, fifteen: 1.96 };
    }
    await act(async () => {
      await client.invalidateQueries();
    });
    await vi.waitFor(() => expect(piece()?.textContent).toContain('LOAD 5.50'));
    expect(piece()).toBe(before);
    expect(marks()).toHaveLength(wedges.length);
    marks().forEach((el, i) => expect(el).toBe(wedges[i]));
  });

  it('redraws a new piece when the range switches', async () => {
    await mount('/machine');
    const before = piece();
    if (data.machine.now) data.machine.now.load = { one: 7.25, five: 2.18, fifteen: 1.96 };
    act(() => setMetricsRange('machine', '1h'));
    await vi.waitFor(() => expect(piece()?.textContent).toContain('LOAD 7.25'));
    expect(piece()).not.toBe(before);
  });
});

describe('Docker centre piece', () => {
  it('feeds the piece every container and stack of the response', () => {
    const v = dockerView(data.docker, Date.now());
    expect(v.piece.containers.map((c) => [c.name, c.stack, c.memBytes, c.restarts])).toEqual([
      ['php84_service_1', 'stack_full', 900 * 1024 ** 2, 0],
      ['mariadb_service', 'stack_full', 600 * 1024 ** 2, 1],
      ['floci_service', 'tools', 100 * 1024 ** 2, 0],
    ]);
    expect(v.piece.stacks.map((s) => [s.stack, s.memBytes, s.rxBps, s.txBps])).toEqual([
      ['stack_full', 1500 * 1024 ** 2, 2000, 1000],
      ['tools', 100 * 1024 ** 2, 10, 10],
    ]);
  });

  it('titles the network panel with the network the agent names, and falls back without a snapshot', () => {
    const d = structuredClone(data.docker);
    if (d.now) d.now.network = 'stack_full';
    expect(dockerView(d, Date.now()).networkTitle).toBe('Network · stack_full');
    d.now = null;
    expect(dockerView(d, Date.now()).networkTitle).toBe('Network · by container');
  });

  it('trends the Containers card by how many containers have a point at each sample time', () => {
    const p = (at: string) => ({ at, cpuPct: 1, memBytes: 1, rxBps: 1, txBps: 1 });
    const series = [
      { container: 'a', stack: 's', points: [p('2026-11-01T10:00:20Z'), p('2026-11-01T10:00:10Z')] },
      { container: 'b', stack: 's', points: [p('2026-11-01T10:00:20Z')] },
    ];
    expect(containerTrend(series)).toEqual([1, 2]);
    expect(containerTrend([])).toEqual([]);
    const d = structuredClone(data.docker);
    d.containerSeries = series;
    expect(dockerView(d, Date.now()).containers.trend).toEqual([1, 2]);
  });

  it('marks a container that is not running as needing a look, and leaves running ones as they were', () => {
    const d = structuredClone(data.docker);
    const running = dockerView(d, Date.now()).piece.containers.map((c) => healthOf(c.health));
    const [first] = d.now?.containers ?? [];
    if (first) Object.assign(first, { state: 'exited', health: 'none' });
    const after = dockerView(d, Date.now()).piece.containers.map((c) => healthOf(c.health));
    expect(after[0]).toBe('warn');
    expect(after.slice(1)).toEqual(running.slice(1));
    expect(running[0]).not.toBe('warn');
  });

  const OK = { state: 'running', health: 'healthy', exitCode: null, restarts: 0 };
  it.each([
    ['exited', 0, 'finished', 'finished'], ['exited', 1, 'warn', 'exited 1'], ['exited', 137, 'warn', 'exited 137'],
    ['exited', null, 'warn', 'exited'], ['exited', undefined, 'warn', 'exited'], ['paused', 0, 'warn', 'paused'],
    ['dead', 0, 'warn', 'dead'], ['restarting', 0, 'warn', 'restarting'], ['created', 0, 'warn', 'created'],
  ])('reads a container in state %s with exit code %s as %s in piece, KPI and list', (state, exitCode, want, uptime) => {
    const d = structuredClone(data.docker);
    if (!d.now) throw new Error('the fixture has no snapshot');
    for (const c of d.now.containers) Object.assign(c, OK);
    Object.assign(d.now.containers[0]!, { state, exitCode, health: 'none', restarts: 2 });
    const v = dockerView(d, Date.now());
    expect(v.piece.containers).toHaveLength(3);
    expect(v.containers.value).toBe('3');
    expect(v.piece.containers.map((c) => healthOf(c.health))).toEqual([want, 'ok', 'ok']);
    const row = v.table.find((r) => r.full === 'php84_service_1');
    if (want === 'finished') {
      expect(v.containers.delta).toBe('2 healthy · 0 no check · 1 finished');
      expect(row).toMatchObject({ health: 'finished', uptime: 'finished', warn: false });
    } else {
      expect(v.containers.delta).toBe('2 healthy · 1 warning');
      expect(row).toMatchObject({ uptime, warn: true, health: 'down' });
    }
  });

  it('reads a running healthy container with exit code 0 as healthy, and keeps — for one without a start time', () => {
    const d = structuredClone(data.docker);
    for (const c of d.now!.containers) Object.assign(c, OK);
    Object.assign(d.now!.containers[0]!, { exitCode: 0, startedAt: null });
    const v = dockerView(d, Date.now());
    expect(v.piece.containers[0]?.health).toBe('healthy');
    expect(v.table.find((r) => r.full === 'php84_service_1')).toMatchObject({
      health: 'healthy', uptime: '—', warn: false });
  });

  it('warns for a running container that is unhealthy', () => {
    const d = structuredClone(data.docker);
    for (const c of d.now!.containers) Object.assign(c, OK);
    d.now!.containers[0]!.health = 'unhealthy';
    const v = dockerView(d, Date.now());
    expect(healthOf(v.piece.containers[0]!.health)).toBe('warn');
    expect(v.containers.delta).toContain('1 warning');
  });

  it('says the events footer counts the range, in singular for one', () => {
    const d = structuredClone(data.docker);
    expect(dockerView(d, Date.now()).eventFoot).toBe('1 event · 1 restart in range · 1 event needs a look');
    d.events = [];
    expect(dockerView(d, Date.now()).eventFoot).toBe('0 events · 0 restarts in range');
  });

  describe('bad Docker events', () => {
    type Event = Fixtures['docker']['events'][number];
    const base = Date.parse('2026-11-24T10:00:00.000Z');
    const ev = (kind: Event['kind'], ms: number, container = 'mariadb_service'): Event =>
      ({ at: new Date(base + ms).toISOString(), container, kind });
    const view = (events: Event[], now: Partial<Schemas['ContainerNow']> | null = {}) => {
      const d = structuredClone(data.docker);
      d.events = [...events].sort((x, y) => Date.parse(y.at) - Date.parse(x.at));
      const first = d.now!.containers[0]!;
      d.now!.containers = now ? [{ ...first, container: 'mariadb_service', state: 'running', ...now }] : [];
      const v = dockerView(d, Date.now());
      expect(v.eventFootWarn || !v.events.some((e) => e.bad)).toBe(true);
      const n = badEvents(d.events, d.now!.containers).size;
      expect(v.eventFoot.split(' · ').slice(2)).toEqual(
        n === 0 ? [] : [n === 1 ? '1 event needs a look' : `${n} events need a look`]);
      expect(v.eventFootWarn).toBe(n > 0);
      return { warn: v.eventFootWarn, bad: v.events.filter((e) => e.bad).map((e) => e.kind) };
    };
    const calm = { warn: false, bad: [] };
    const badAt = (events: Event[]) => [...badEvents(events, [])].map((e) => e.at).sort();

    it('is calm with no events and with harmless ones', () => {
      expect(view([])).toEqual(calm);
      expect(view([ev('create', 0), ev('start', 10), ev('health_healthy', 20), ev('destroy', 30)])).toEqual(calm);
    });

    it.each([['1 ms after', 1], ['82 ms before', -82]])('is calm for a stop recorded %s its die', (_when, at) => {
      expect(view([ev('kill', -8000), ev('die', 0), ev('stop', at)])).toEqual(calm);
    });

    it('is calm for a kill with no die', () => {
      expect(view([ev('kill', 0)])).toEqual(calm);
    });

    it.each(['oom', 'restart', 'health_unhealthy'] as const)('warns for %s, also outside the drawn rows', (kind) => {
      expect(view([ev('start', 0), ev(kind, 10)]).warn).toBe(true);
      const far = [ev(kind, 0), ...Array.from({ length: 20 }, (_, i) => ev('start', 100 + i))];
      expect(view(far)).toEqual({ warn: true, bad: [] });
    });

    it('warns for a die with no stop of its own container in reach', () => {
      const one = { warn: true, bad: ['exited'] };
      expect(view([ev('die', 0)])).toEqual(one);
      expect(view([ev('die', 0), ev('start', 100)])).toEqual(one);
      expect(view([ev('die', 0), ev('stop', 1, 'redis_service')])).toEqual(one);
      expect(view([ev('die', 0), ev('stop', 2001)])).toEqual(one);
      expect(view([ev('die', 0), ev('stop', -2001)])).toEqual(one);
      expect(view([ev('die', 0), ev('stop', 2000)])).toEqual(calm);
      expect(view([ev('die', 0), ev('stop', -2000)])).toEqual(calm);
    });

    const crash = [ev('kill', -500), ev('die', 0), ev('stop', 1), ev('start', 300), ev('die', 900)];
    it.each([
      ['the later die', crash, 900],
      ['the earlier die', [ev('die', -1500), ev('start', -900), ev('die', 0), ev('stop', 1)], -1500],
      ['the die farther from a stop between two dies', [ev('die', 0), ev('stop', 300), ev('die', 1000)], 1000],
    ])('lets one stop excuse only the nearest die: %s stays bad', (_name, events, ms) => {
      const want = [new Date(base + ms).toISOString()];
      expect(badAt(events)).toEqual(want);
      expect(badAt([...events].reverse())).toEqual(want);
      expect(view(events)).toEqual({ warn: true, bad: ['exited'] });
    });

    it('excuses both dies when each has its own stop', () => {
      expect(view([...crash, ev('stop', 901)])).toEqual(calm);
      expect(badAt([...crash, ev('stop', 901)])).toEqual([]);
    });

    it('pairs a die and its stop across all events, not only the drawn rows', () => {
      const later = (n: number) => Array.from({ length: n }, (_, i) => ev('start', 100 + i));
      expect(view([ev('die', 0), ev('stop', 1), ...later(20)])).toEqual(calm);
      expect(view([ev('die', 0), ...later(20)])).toEqual({ warn: true, bad: [] });
      expect(view([ev('stop', -1), ev('die', 0), ...later(5)])).toEqual(calm);
    });

    it.each([
      ['die', true], ['kill', true], ['oom', true], ['restart', true], ['health_unhealthy', true], ['destroy', true],
      ['stop', true], ['create', false], ['start', false], ['rename', false], ['health_healthy', false],
    ] as const)('draws a %s row with loud %s', (kind, loud) => {
      const d = structuredClone(data.docker);
      d.events = [ev(kind, 0)];
      expect(dockerView(d, Date.now()).events.map((e) => e.loud)).toEqual([loud]);
    });

    it('never excuses a die under the merged label', () => {
      const other = [ev('die', 0, 'other'), ev('stop', 1, 'other')];
      expect(view(other).warn).toBe(true);
      expect(view(other, { container: 'other', state: 'exited', exitCode: 0 }).warn).toBe(true);
    });

    it('excuses the die of a container that is finished now, and no other', () => {
      const die = [ev('die', 0)];
      expect(view(die, { state: 'exited', exitCode: 0 })).toEqual(calm);
      expect(view(die, { state: 'exited', exitCode: 1 }).warn).toBe(true);
      expect(view(die, { state: 'exited', exitCode: null }).warn).toBe(true);
      expect(view(die, { container: 'redis_service', state: 'exited', exitCode: 0 }).warn).toBe(true);
      expect(view(die, null).warn).toBe(true);
    });

    it('counts every bad event in the footer, also the ones that are not drawn', () => {
      const d = structuredClone(data.docker);
      const start = Array.from({ length: 20 }, (_, i) => ev('start', 100 + i));
      const foot = (events: Event[]) => {
        d.events = [...events].sort((x, y) => Date.parse(y.at) - Date.parse(x.at));
        return dockerView(d, Date.now());
      };
      expect(foot(start).eventFoot).toBe('20 events · 0 restarts in range');
      const one = foot([ev('oom', 0), ...start]);
      expect(one.events.map((e) => e.bad)).not.toContain(true);
      expect(one.eventFoot).toBe('21 events · 0 restarts in range · 1 event needs a look');
      const two = foot([ev('oom', 0), ev('health_unhealthy', 200), ...start]);
      expect(two.events.filter((e) => e.bad)).toHaveLength(1);
      expect(two.eventFoot).toBe('22 events · 0 restarts in range · 2 events need a look');
    });

    it('warns for a die while Docker has no current state', () => {
      const d = structuredClone(data.docker);
      d.now = null;
      d.events = [ev('die', 0)];
      expect(dockerView(d, Date.now()).eventFootWarn).toBe(true);
    });
  });

  const foot = () => [...(host?.querySelectorAll('p') ?? [])].find((p) => p.textContent?.includes('in range'));
  const marks = () => [...(host?.querySelectorAll('li > span[class*="eventMark"]') ?? [])].map((s) => s.textContent);

  it('draws only the die of a lone exit as a bad row, and the kill, die and stop trio as calm', async () => {
    type Row = Fixtures['docker']['events'][number];
    const event = (kind: Row['kind'], ms: number): Row =>
      ({ at: new Date(Date.now() - 60_000 + ms).toISOString(), container: 'mariadb_service', kind });
    Object.assign(data.docker.now!.containers.find((c) => c.container === 'mariadb_service')!,
      { state: 'running', exitCode: null });
    const kindOf = (text: string) => [...(host?.querySelectorAll('li span') ?? [])]
      .find((s) => s.textContent?.startsWith(text));
    data.docker.events = [event('stop', 2), event('die', 1), event('kill', 0)];
    await mount('/docker');
    await vi.waitFor(() => expect(foot()?.textContent).toBe('3 events · 0 restarts in range'));
    expect(foot()?.className).not.toMatch(/footWarn/);
    expect(host?.querySelectorAll('li[class*="eventBad"]')).toHaveLength(0);
    expect(marks()).toEqual(['·', '·', '·']);
    expect(host?.querySelector('li [class*="hidden"]')).toBeNull();
    for (const text of ['killed', 'exited', 'stopped']) expect(kindOf(text)?.className).toMatch(/eventWarn/);
    act(() => root?.unmount());
    host?.remove();
    data.docker.events = [event('start', 100), event('die', 0)];
    await mount('/docker');
    await vi.waitFor(() => expect(foot()?.className).toMatch(/footWarn/));
    const bad = [...(host?.querySelectorAll('li[class*="eventBad"]') ?? [])];
    expect(bad).toHaveLength(1);
    expect(bad[0]?.textContent).toMatch(/exited .*needs a look$/);
    const spoken = [...(host?.querySelectorAll('li [class*="hidden"]') ?? [])];
    expect(spoken).toHaveLength(1);
    expect(spoken[0]?.closest('[aria-hidden="true"]')).toBeNull();
    expect(marks()).toEqual(['·', '×']);
    expect(foot()?.textContent).toBe('2 events · 0 restarts in range · 1 event needs a look');
    expect(kindOf('started')?.className).not.toMatch(/eventWarn/);
  });

  it('puts the events footer in the normal colour when nothing happened', async () => {
    await mount('/docker');
    await vi.waitFor(() => expect(foot()?.className).toMatch(/footWarn/));
    act(() => root?.unmount());
    host?.remove();
    data.docker.events = [];
    await mount('/docker');
    await vi.waitFor(() => expect(foot()?.textContent).toBe('0 events · 0 restarts in range'));
    expect(foot()?.className).toMatch(/\b_foot_/);
    expect(foot()?.className).not.toMatch(/footWarn|eventFoot/);
  });

  it('reads an image list that was never read as n/a, not as 0', () => {
    const d = structuredClone(data.docker);
    if (!d.now?.disk) throw new Error('the fixture has no disk part');
    d.now.disk.images.largestBytes = 0;
    expect(dockerView(d, Date.now()).disk?.largest).toBe('largest image · 0 B');
    delete d.now.disk.images.largestBytes;
    expect(dockerView(d, Date.now()).disk?.largest).toBe('largest image · n/a');
  });

  it('marks disk figures whose latest refresh failed as stale, and only those', async () => {
    expect(dockerView(data.docker, Date.now()).disk?.stale).toBeNull();
    if (!data.docker.now?.disk) throw new Error('the fixture has no disk part');
    await mount('/docker');
    expect(host?.querySelector('[class*="diskStale"]')).toBeNull();
    act(() => root?.unmount());
    host?.remove();
    data.docker.now.disk.stale = true;
    expect(dockerView(data.docker, Date.now()).disk?.stale).toMatch(/^Stale · last read \d\d:\d\d$/);
    await mount('/docker');
    await vi.waitFor(() => expect(host?.querySelector('[class*="diskStale"]')?.textContent)
      .toMatch(/^Stale · last read \d\d:\d\d$/));
  });

  it('draws the response and keeps the same piece nodes on a live sample', async () => {
    await mount('/docker');
    expect(piece()?.getAttribute('data-piece')).toBe('docker');
    expect(piece()?.textContent).toContain('MEM 1.5 GB');
    const before = piece();
    const hexes = marks();
    expect(hexes.length).toBeGreaterThan(0);
    for (const s of data.docker.now?.stacks ?? []) if (s.stack === 'stack_full') s.memBytes = 2048 * 1024 ** 2;
    await act(async () => {
      await client.invalidateQueries();
    });
    await vi.waitFor(() => expect(piece()?.textContent).toContain('MEM 2.0 GB'));
    expect(piece()).toBe(before);
    expect(marks()).toHaveLength(hexes.length);
    marks().forEach((el, i) => expect(el).toBe(hexes[i]));
  });

  it('redraws a new piece when the range switches', async () => {
    await mount('/docker');
    const before = piece();
    for (const s of data.docker.now?.stacks ?? []) if (s.stack === 'stack_full') s.memBytes = 2048 * 1024 ** 2;
    act(() => setMetricsRange('docker', '1h'));
    await vi.waitFor(() => expect(piece()?.textContent).toContain('MEM 2.0 GB'));
    expect(piece()).not.toBe(before);
  });
});
