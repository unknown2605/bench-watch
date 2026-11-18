import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import type { Schemas } from '../../api';
import { resetFilters } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { fixtures } from './testData';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const iface = (name: string, rxBps: number) => ({ interface: name, rxBps, txBps: 1, mergedNames: 0 });

function machine(...list: ReturnType<typeof iface>[]) {
  const m = structuredClone(fixtures(Date.now()).machine) as Schemas['MachineResponse'];
  if (m.now) m.now.interfaces = list;
  m.interfaceSeries = list.map((i) => ({ interface: i.interface, peakRxBps: i.rxBps, peakRxAt: null,
    points: [{ at: new Date().toISOString(), rxBps: i.rxBps, txBps: 1 }] }));
  return m;
}

let body: Schemas['MachineResponse'];
let root: Root | null = null;
let host: HTMLElement | null = null;

beforeAll(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  HTMLCanvasElement.prototype.getContext = () => null;
  window.scrollTo = () => undefined;
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    const path = new URL(String(input), 'http://app.test').pathname;
    const data = fixtures(Date.now());
    const known: Record<string, unknown> = {
      '/api/machine': body, '/api/meta': data.meta, '/api/status': data.status,
    };
    const status = path in known ? 200 : 404;
    return new Response(JSON.stringify(known[path] ?? { code: 'not_found' }),
      { status, headers: { 'Content-Type': 'application/json' } });
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  resetFilters();
});

describe('Machine network pick across refetches', () => {
  it('keeps a pick, falls back when it disappears, and does not jump back when it returns', async () => {
    body = machine(iface('en0', 10), iface('utun0', 20));
    const router = createAppRouter(createMemoryHistory({ initialEntries: ['/machine'] }));
    host = document.createElement('div');
    document.body.append(host);
    root = createRoot(host);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root?.render(
        <QueryClientProvider client={client}>
          <RouterProvider router={router} />
        </QueryClientProvider>,
      );
    });
    const chart = () => document.querySelector('[aria-label^="Network throughput"]')?.getAttribute('aria-label');
    const option = (name: string) => [...document.querySelectorAll<HTMLElement>('[role="option"]')]
      .find((o) => o.textContent?.startsWith(name));
    const refetch = async (next: Schemas['MachineResponse']) => {
      body = next;
      await act(async () => { await client.refetchQueries(); });
    };

    await vi.waitFor(() => expect(chart()).toContain('en0'));
    await act(async () => {
      for (const type of ['mousedown', 'mouseup', 'click']) {
        option('utun0')?.dispatchEvent(new MouseEvent(type, { bubbles: true }));
      }
    });
    await vi.waitFor(() => expect(chart()).toContain('utun0'));

    await refetch(machine(iface('en0', 11), iface('utun0', 21)));
    expect(chart()).toContain('utun0');

    await refetch(machine(iface('en0', 12)));
    await vi.waitFor(() => expect(chart()).toContain('en0'));

    await refetch(machine(iface('en0', 13), iface('utun0', 22)));
    expect(chart()).toContain('en0');
    expect(option('en0')?.getAttribute('aria-selected')).toBe('true');
  });
});
