import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters, setMetricsRange } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { healthOf } from '../../charts/pieces/docker';
import { containerTrend, dockerView } from './Docker';
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
