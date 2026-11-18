import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act, type ReactElement } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { ChartTable } from '../../charts/ChartTable';
import { LineArea } from '../../charts/LineArea';
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

function show(node: ReactElement) {
  host ??= document.body.appendChild(document.createElement('div'));
  root ??= createRoot(host);
  act(() => root?.render(node));
}

async function mount(path: string) {
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => {
    show(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
  });
  await vi.waitFor(() => expect(host?.querySelector('main[aria-busy="false"] section')).not.toBeNull());
}

async function update() {
  await act(async () => {
    await client.invalidateQueries();
  });
}

const all = (selector: string) => [...(host?.querySelectorAll(selector) ?? [])];
const text = (els: readonly Element[]) => els.map((el) => el.textContent);

type Seen = { get: () => Element[]; els: readonly Element[]; was: readonly (string | null)[] };
function seen(get: () => Element[]): Seen {
  const els = get();
  return { get, els, was: text(els) };
}
const rowsOf = (meters: string) => () => all(meters).map((m) => m.parentElement as Element);

async function expectSameElements(before: Seen) {
  expect(before.els.length).toBeGreaterThan(0);
  await vi.waitFor(() => expect(text(before.get())).not.toEqual(before.was));
  const after = before.get();
  expect(after.length).toBe(before.els.length);
  after.forEach((el, i) => expect(el).toBe(before.els[i]));
}

describe('a live update keeps the elements it changes', () => {
  it('Docker network rows stay when the containers change rank', async () => {
    await mount('/docker');
    const rows = '[aria-label="Network · stack_full"] [role="meter"]';
    const names = () => all(rows).map((m) => m.getAttribute('aria-label'));
    const before = seen(rowsOf(rows));
    const order = names();
    data.docker.now?.containers.forEach((c, i) => Object.assign(c, { rxBps: (i + 1) * 4_000_000, txBps: 0 }));
    await update();
    await expectSameElements(before);
    expect(names()).not.toEqual(order);
  });

  it('Machine top apps stay when another app takes the first place', async () => {
    data.machine.now?.apps.push({ app: 'Docker', cpuPct: 3, memBytes: 1024 ** 3 });
    await mount('/machine');
    const rows = '[aria-label="Top apps · CPU and memory"] [role="meter"]';
    const before = seen(rowsOf(rows));
    for (const a of data.machine.now?.apps ?? []) if (a.app === 'Docker') a.cpuPct = 90;
    await update();
    await expectSameElements(before);
  });

  it('Overview context bands stay when a band is relabelled', async () => {
    await mount('/');
    const bands = '[aria-label="Context bands"] [role="img"]';
    const before = seen(() => all(bands));
    for (const b of data.overview.bands) b.band = { minTokens: b.band.minTokens + 50_000, maxTokens: b.band.maxTokens };
    data.overview.contextBands = data.overview.bands.map((b) => b.band);
    await update();
    await expectSameElements(before);
  });

  it('Context band rows stay when a band is relabelled', async () => {
    await mount('/context');
    const rows = '[aria-label="Re-read by band"] [role="meter"]';
    const before = seen(rowsOf(rows));
    for (const b of data.context.bands) b.band = { minTokens: b.band.minTokens + 50_000, maxTokens: b.band.maxTokens };
    await update();
    await expectSameElements(before);
  });

  it('chart markers stay when the count in their name changes', () => {
    const chart = (count: number) => (
      <LineArea
        data={[{ x: 0, y: 1 }, { x: 1, y: 3 }, { x: 2, y: 2 }]}
        markers={[{ x: 1, kind: 'compaction', name: `Nov 5: ${count} compactions` }, { x: 2, kind: 'compaction', name: 'Nov 6' }]}
        width={300}
        height={80}
        formatY={String}
        label="Peak"
      />
    );
    show(chart(2));
    const before = all('svg[role="img"] g[transform]');
    expect(before).toHaveLength(2);
    show(chart(3));
    all('svg[role="img"] g[transform]').forEach((g, i) => expect(g).toBe(before[i]));
  });

  it('the rows of a chart table stay when their first cell changes', async () => {
    const view = (n: number) => (
      <ChartTable id="t" caption="Sessions" head={['Name', 'Value']} rows={[[`web · ${n}`, '1'], [`db · ${n}`, '2']]} />
    );
    show(view(100));
    const rows = seen(() => all('#t tbody tr'));
    show(view(105));
    await expectSameElements(rows);
  });
});
