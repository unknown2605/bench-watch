import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useLiveStream, useStreamUp } from './live';
import type { MachineData } from './liveCache';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

class FakeSource {
  static all: FakeSource[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  handlers = new Map<string, (e?: unknown) => void>();
  constructor(public url: string) {
    FakeSource.all.push(this);
  }
  addEventListener(name: string, fn: (e?: unknown) => void) {
    this.handlers.set(name, fn);
  }
  close() {
    this.closed = true;
  }
}
const last = () => FakeSource.all[FakeSource.all.length - 1];

function Probe() {
  useLiveStream();
  return <span data-up={String(useStreamUp())} />;
}

let root: Root | null = null;
const host = document.createElement('div');
const up = () => host.firstElementChild?.getAttribute('data-up');
const mount = (client: QueryClient) => {
  root = createRoot(host);
  act(() => root?.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>));
};

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  vi.unstubAllGlobals();
  vi.useRealTimers();
  FakeSource.all = [];
});

describe('useLiveStream', () => {
  it('invalidates transcript queries once per event burst, without cancelling a running fetch', () => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', FakeSource);
    const client = new QueryClient();
    const spy = vi.spyOn(client, 'invalidateQueries').mockResolvedValue();
    mount(client);
    const source = last();
    expect(source?.url).toBe('/api/live');
    const names = [...(source?.handlers.keys() ?? [])].sort();
    expect(names).toEqual(['activity', 'docker', 'machine', 'sessions', 'status']);
    source?.handlers.get('sessions')?.();
    source?.handlers.get('status')?.();
    vi.advanceTimersByTime(1_500);
    expect(spy).toHaveBeenCalledTimes(1);
    expect(spy.mock.calls[0]?.[1]).toEqual({ cancelRefetch: false });
    const predicate = spy.mock.calls[0]?.[0]?.predicate;
    expect(predicate?.({ queryKey: ['/api/overview', { range: '30d' }] } as never)).toBe(true);
    expect(predicate?.({ queryKey: ['/api/machine', {}] } as never)).toBe(false);
    act(() => root?.unmount());
    expect(source?.closed).toBe(true);
    root = null;
  });

  it('puts a machine event straight into the Live query and refetches only the other ranges', () => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', FakeSource);
    const client = new QueryClient();
    const spy = vi.spyOn(client, 'invalidateQueries').mockResolvedValue();
    const empty: MachineData = {
      range: 'live', resolution: 'raw', detailIntervalSeconds: 2, coverageFrom: null, partial: false,
      historyPaused: false, now: null, series: [], coreSeries: [], interfaceSeries: [],
    };
    client.setQueryData(['/api/machine', { range: 'live' }], empty);
    client.setQueryData(['/api/machine', { range: '5m' }], { ...empty, range: '5m' });
    mount(client);
    const data = {
      at: '2026-11-11T10:00:00Z', cpuBusyPct: 42, cores: [], performanceCores: 1, efficiencyCores: 0, load: null,
      memory: null, diskIo: null, interfaces: [], volumes: [], apps: [],
    };
    last()?.handlers.get('machine')?.({ data: JSON.stringify({ type: 'machine', data }) });
    expect(client.getQueryData<MachineData>(['/api/machine', { range: 'live' }])?.series[0]?.cpuBusyPct).toBe(42);
    expect(client.getQueryData<MachineData>(['/api/machine', { range: '5m' }])?.series).toHaveLength(0);
    vi.advanceTimersByTime(2_100);
    const matches = (key: unknown[]) => spy.mock.calls.some((c) => c[0]?.predicate?.({ queryKey: key } as never));
    expect(matches(['/api/machine', { range: '5m' }])).toBe(true);
    expect(matches(['/api/machine', { range: 'live' }])).toBe(false);
    last()?.handlers.get('machine')?.({ data: 'not json' });
  });

  it('reopens with backoff after an error and polls only while the stream is down', () => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', FakeSource);
    mount(new QueryClient());
    expect(up()).toBe('false');
    act(() => last()?.onopen?.());
    expect(up()).toBe('true');
    const first = last();
    act(() => first?.onerror?.());
    expect(up()).toBe('false');
    expect(first?.closed).toBe(true);
    expect(FakeSource.all).toHaveLength(1);
    act(() => void vi.advanceTimersByTime(1_000));
    expect(FakeSource.all).toHaveLength(2);
    act(() => last()?.onerror?.());
    act(() => void vi.advanceTimersByTime(1_000));
    expect(FakeSource.all).toHaveLength(2);
    act(() => void vi.advanceTimersByTime(1_000));
    expect(FakeSource.all).toHaveLength(3);
  });

  it('closes the stream while the page is hidden and reopens it when shown', () => {
    vi.stubGlobal('EventSource', FakeSource);
    mount(new QueryClient());
    const first = last();
    const hidden = vi.spyOn(document, 'hidden', 'get');
    hidden.mockReturnValue(true);
    act(() => void document.dispatchEvent(new Event('visibilitychange')));
    expect(first?.closed).toBe(true);
    hidden.mockReturnValue(false);
    act(() => void document.dispatchEvent(new Event('visibilitychange')));
    expect(FakeSource.all).toHaveLength(2);
    hidden.mockRestore();
  });

  it('does nothing where EventSource is missing', () => {
    vi.stubGlobal('EventSource', undefined);
    expect(() => mount(new QueryClient())).not.toThrow();
  });

  const machineEvent = () => ({
    data: JSON.stringify({
      type: 'machine',
      data: {
        at: '2026-11-11T10:00:00Z', cpuBusyPct: 1, cores: [], performanceCores: 1, efficiencyCores: 0, load: null,
        memory: null, diskIo: null, interfaces: [], volumes: [], apps: [],
      },
    }),
  });

  it('refetches the 15m and 1h ranges at most every 5 s while the stream is up', () => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', FakeSource);
    const client = new QueryClient();
    const spy = vi.spyOn(client, 'invalidateQueries').mockResolvedValue();
    mount(client);
    const hits = (range: string) =>
      spy.mock.calls.filter((c) => c[0]?.predicate?.({ queryKey: ['/api/machine', { range }] } as never)).length;
    last()?.handlers.get('machine')?.(machineEvent());
    for (let i = 0; i < 10; i += 1) {
      vi.advanceTimersByTime(1_000);
      last()?.handlers.get('machine')?.(machineEvent());
    }
    vi.advanceTimersByTime(1_000);
    expect(hits('1h')).toBeLessThanOrEqual(3);
    expect(hits('15m')).toBe(hits('1h'));
    expect(hits('5m')).toBeGreaterThanOrEqual(5);
  });

  it('keeps the backoff growing for a stream that opens and drops at once, and resets it after 10 s open', () => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', FakeSource);
    mount(new QueryClient());
    act(() => last()?.onopen?.());
    act(() => last()?.onerror?.());
    act(() => void vi.advanceTimersByTime(1_000));
    expect(FakeSource.all).toHaveLength(2);
    act(() => last()?.onopen?.());
    act(() => last()?.onerror?.());
    act(() => void vi.advanceTimersByTime(1_999));
    expect(FakeSource.all).toHaveLength(2);
    act(() => void vi.advanceTimersByTime(1));
    expect(FakeSource.all).toHaveLength(3);
    act(() => last()?.onopen?.());
    act(() => void vi.advanceTimersByTime(10_000));
    act(() => last()?.onerror?.());
    act(() => void vi.advanceTimersByTime(1_000));
    expect(FakeSource.all).toHaveLength(4);
  });

  it('treats a stream with no event for 30 s as down, then reopens it', () => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', FakeSource);
    mount(new QueryClient());
    const first = last();
    act(() => first?.onopen?.());
    act(() => void vi.advanceTimersByTime(25_000));
    act(() => void first?.handlers.get('status')?.());
    act(() => void vi.advanceTimersByTime(25_000));
    expect(up()).toBe('true');
    act(() => void vi.advanceTimersByTime(5_000));
    expect(up()).toBe('false');
    expect(first?.closed).toBe(true);
    act(() => void vi.advanceTimersByTime(1_000));
    expect(FakeSource.all).toHaveLength(2);
  });

  it('does not refetch the unfiltered Live Docker query because a stack-filtered one has another shape', () => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', FakeSource);
    const client = new QueryClient();
    const spy = vi.spyOn(client, 'invalidateQueries').mockResolvedValue();
    const container = (c: string, stack: string) => ({
      container: c, stack, state: 'running', health: 'none', cpuPct: 1, memBytes: 2, memLimitBytes: null,
      rxBps: 3, txBps: 4, restarts: 0, startedAt: null, mergedNames: 0,
    });
    const snapshot = (containers: unknown[]) => ({
      at: '2026-11-11T10:00:00Z',
      vm: { memTotalBytes: 10, memUsedBytes: 5, cpuPct: 3, swapUsedBytes: 0, cpus: 4 },
      counts: {}, containers, stacks: [], disk: null,
    });
    const both = [container('web', 'a'), container('db', 'b')];
    const shell = (now: unknown) => ({
      range: 'live', now, vmSeries: [], containerSeries: [], events: [], available: true,
    });
    client.setQueryData(['/api/docker', { range: 'live' }], shell(snapshot(both)));
    client.setQueryData(['/api/docker', { range: 'live', stack: 'a' }], shell(snapshot([container('web', 'a')])));
    mount(client);
    last()?.handlers.get('docker')?.({ data: JSON.stringify({ type: 'docker', data: snapshot(both) }) });
    vi.advanceTimersByTime(2_100);
    const predicate = spy.mock.calls[0]?.[0]?.predicate;
    expect(predicate?.({ queryKey: ['/api/docker', { range: 'live' }] } as never)).toBe(false);
  });

  it('refetches the unfiltered Live Docker query when only a container exit code changed', () => {
    vi.useFakeTimers();
    vi.stubGlobal('EventSource', FakeSource);
    const client = new QueryClient();
    const refetched: boolean[] = [];
    vi.spyOn(client, 'invalidateQueries').mockImplementation(async (filters) => {
      const live = client.getQueryCache().find({ queryKey: ['/api/docker', { range: 'live' }] });
      if (live) refetched.push(filters?.predicate?.(live) ?? false);
    });
    const exited = (exitCode: number) => ({
      container: 'job', stack: 'a', state: 'exited', exitCode, health: 'none', cpuPct: null, memBytes: null,
      memLimitBytes: null, rxBps: null, txBps: null, restarts: 0, startedAt: null, mergedNames: 0,
    });
    const snapshot = (at: string, exitCode: number) => ({
      at,
      vm: { memTotalBytes: 10, memUsedBytes: 5, cpuPct: 3, swapUsedBytes: 0, cpus: 4 },
      counts: {}, containers: [exited(exitCode)], stacks: [], disk: null,
    });
    const shell = (now: unknown) => ({
      range: 'live', now, vmSeries: [], containerSeries: [], events: [], available: true,
    });
    client.setQueryData(['/api/docker', { range: 'live' }], shell(snapshot('2026-11-11T10:00:00Z', 0)));
    mount(client);
    const send = (at: string, exitCode: number) =>
      last()?.handlers.get('docker')?.({ data: JSON.stringify({ type: 'docker', data: snapshot(at, exitCode) }) });
    send('2026-11-11T10:00:05Z', 137);
    vi.advanceTimersByTime(2_100);
    expect(refetched).toContain(true);
  });
});
