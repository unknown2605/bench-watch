import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters, setRange } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { fillsView, repeatGap, repeatNote, repeatShare } from './Fills';
import { guardView } from './Guard';
import type { Schemas } from '../../api';
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
const piece = () =>
  [...(host?.querySelectorAll('[data-piece]') ?? [])].find((el) => !el.closest('[aria-hidden="true"]')) ?? null;
const marks = () => [...(piece()?.querySelectorAll('[role="img"][aria-label], [role="button"][aria-label]') ?? [])];
const labels = () => marks().map((el) => el.getAttribute('aria-label'));

const grep = () => data.fills.tools.find((t) => t.tool === 'Grep');

describe('Fills centre piece', () => {
  it('feeds the piece every tool of the response', () => {
    const v = fillsView(data.fills, null);
    expect(v.piece.tools).toHaveLength(data.fills.tools.length);
    expect(v.piece.tools.map((t) => t.weightedTokens)).toEqual(data.fills.tools.map((t) => t.weightedTokens));
    expect(v.piece).toMatchObject({ contextShare: 0.4, resultTokens: 600_000, rangeLabel: '7 d' });
  });

  it('shows the short project name in the top-results table with the full label in the title', async () => {
    data.fills.top[0]!.project = 'www-self-projects-benchwatch';
    await mount('/fills');
    const cell = host?.querySelector('span[title="www-self-projects-benchwatch"]');
    expect(cell?.textContent).toBe('benchwatch');
  });

  it('draws the tool shares and keeps its nodes on a live answer', async () => {
    await mount('/fills');
    expect(piece()?.getAttribute('data-piece')).toBe('fills');
    expect(labels()).toContain('Rank 1: Read: whole, 66.7%');
    const before = piece();
    const byTool = (el: Element) => el.getAttribute('aria-label')?.split(': ').slice(1).join(': ').split(',')[0];
    const slices = new Map(marks().map((el) => [byTool(el), el]));
    if (grep()) grep()!.weightedTokens = 3_000_000;
    await act(async () => {
      await client.invalidateQueries();
    });
    await vi.waitFor(() => expect(labels()).toContain('Rank 1: Grep, 66.7%'));
    expect(piece()).toBe(before);
    marks().forEach((el) => expect(el).toBe(slices.get(byTool(el))));
  });

  it('redraws a new piece when the range switches', async () => {
    await mount('/fills');
    const before = piece();
    if (grep()) grep()!.weightedTokens = 3_000_000;
    act(() => setRange('fills', '30d'));
    await vi.waitFor(() => expect(labels()).toContain('Rank 1: Grep, 66.7%'));
    expect(piece()).not.toBe(before);
  });
});

describe('Same result re-read meter', () => {
  const meter = () => host?.querySelector('[role="meter"][aria-label*="same result re-read share"]') ?? null;
  const line = () => meter()?.parentElement ?? [...(host?.querySelectorAll('span') ?? [])]
    .find((el) => el.textContent?.startsWith('Same result re-read'))?.parentElement ?? null;
  const note = () => line()?.querySelector('[class*="meterNote"]') ?? null;
  const measure = (share: number) => {
    for (const t of data.fills.tools) {
      const measured = Math.floor(t.results * share);
      Object.assign(t, { repeatMeasuredResults: measured, repeatResults: measured > 0 ? measured / 5 : undefined });
    }
  };

  it('gives the share over the measured results of the selected tools, and none when nothing was measured', () => {
    expect(repeatShare([
      { results: 30, repeatResults: 3, repeatMeasuredResults: 30 },
      { results: 10, repeatResults: 1, repeatMeasuredResults: 10 },
    ])).toBeCloseTo(0.1);
    expect(repeatShare([{ results: 4, repeatResults: 0, repeatMeasuredResults: 4 }])).toBe(0);
    expect(repeatShare([{ results: 100, repeatResults: 10, repeatMeasuredResults: 40 }])).toBeCloseTo(0.25);
    expect(repeatShare([
      { results: 50, repeatMeasuredResults: 0 },
      { results: 20, repeatResults: 5, repeatMeasuredResults: 20 },
    ])).toBeCloseTo(0.25);
    expect(repeatShare([])).toBeNull();
    expect(repeatShare([{ results: 0, repeatResults: 0, repeatMeasuredResults: 0 }])).toBeNull();
    expect(repeatShare([{ results: 4, repeatMeasuredResults: 0 }])).toBeNull();
    expect(repeatShare([{ results: 4 } as Schemas['ToolStats']])).toBeNull();
    expect(fillsView(data.fills, null).selected?.repeat).toBeCloseTo(0.2);
  });

  it('notes a share that covers only part of the results, and none for a fully measured range', () => {
    expect(repeatNote([{ results: 30, repeatResults: 3, repeatMeasuredResults: 30 }])).toBeNull();
    expect(repeatNote([{ results: 30, repeatMeasuredResults: 0 }])).toBeNull();
    expect(repeatNote([])).toBeNull();
    expect(repeatNote([
      { results: 50, repeatMeasuredResults: 0 },
      { results: 20, repeatResults: 5, repeatMeasuredResults: 20 },
    ])).toBe('measured days only · 20 of 70 results');
    expect(repeatNote([{ results: 20000, repeatMeasuredResults: 19980 }]))
      .toBe('measured days only · 19,980 of 20,000 results');
  });

  it('shows the measured share in the plain tone at or under the target', async () => {
    for (const t of data.fills.tools) t.repeatResults = Math.floor(t.results * data.meta.rereadTarget);
    await mount('/fills');
    await vi.waitFor(() => expect(meter()).not.toBeNull());
    expect(meter()?.getAttribute('aria-valuenow')).toBe('15');
    expect(meter()?.className).not.toMatch(/warn/);
    expect(line()?.textContent).toContain('15%');
    expect(line()?.querySelector('[class*="meterWarn"]')).toBeNull();
    expect(line()?.textContent).not.toContain('not available');
  });

  it('turns warn above the target, and reads as before when every result was measured', async () => {
    await mount('/fills');
    await vi.waitFor(() => expect(meter()).not.toBeNull());
    expect(meter()?.getAttribute('aria-valuenow')).toBe('20');
    expect(meter()?.className).toMatch(/warn/);
    expect(line()?.querySelector('[class*="meterWarn"]')?.textContent).toBe('20%');
    expect(note()).toBeNull();
    expect(line()?.textContent).toMatch(/15%20%$/);
  });

  it('says not available when no selected result comes from a measured day', async () => {
    measure(0);
    await mount('/fills');
    await vi.waitFor(() => expect(line()).not.toBeNull());
    expect(meter()).toBeNull();
    expect(line()?.textContent).toMatch(/15%not available for this period$/);
  });

  it('shows the share of the measured days and says so when the range also holds unmeasured ones', async () => {
    measure(0.5);
    const results = data.fills.tools.reduce((s, t) => s + t.results, 0);
    const measured = data.fills.tools.reduce((s, t) => s + t.repeatMeasuredResults, 0);
    expect(measured).toBeGreaterThan(0);
    expect(measured).toBeLessThan(results);
    await mount('/fills');
    await vi.waitFor(() => expect(meter()).not.toBeNull());
    expect(meter()?.getAttribute('aria-valuenow')).toBe('20');
    expect(note()?.textContent).toBe(fillsView(data.fills, null).selected?.repeatNote);
    expect(note()?.textContent).toMatch(/^measured days only · \S+ of \S+ results$/);
    expect(meter()?.getAttribute('aria-label')?.endsWith(`, ${note()?.textContent}`)).toBe(true);
  });

  it('says no results, not unavailable, when the selected tools returned nothing', async () => {
    expect(repeatGap([])).toBe('no results');
    expect(repeatGap([{ results: 0 }, { results: 0 }])).toBe('no results');
    expect(repeatGap([{ results: 0 }, { results: 3 }])).toBe('not available for this period');
    for (const t of data.fills.tools) Object.assign(t, { results: 0, repeatResults: 0, repeatMeasuredResults: 0 });
    expect(fillsView(data.fills, null).selected).toMatchObject({ repeat: null, repeatGap: 'no results' });
    await mount('/fills');
    await vi.waitFor(() => expect(line()).not.toBeNull());
    expect(meter()).toBeNull();
    expect(line()?.textContent).toContain('no results');
    expect(line()?.textContent).not.toContain('not available');
  });
});

describe('Guard centre piece', () => {
  it('feeds the piece the log figures', () => {
    const v = guardView(data.guard, new Date());
    expect(v.piece).toMatchObject({
      bash: 600, file: 240, subShare: 0.25, blocked: 10, version: 'build a1b2c3d4e5f6', mode: expect.stringMatching(/^bash=go · file=node · system install · installed \d{2} \w{3}$/),
    });
    expect(v.piece.hours).toHaveLength(24);
  });

  it('reads unknown on the tab when the folder exists but both modes are unknown', async () => {
    const install = { ...data.guard.install, source: 'user', bash: 'unknown', file: 'unknown' } as const;
    data.guard = { ...data.guard, install };
    await mount('/guard');
    await vi.waitFor(() => expect(piece()?.textContent).toContain('Guard state unknown'));
    expect(piece()?.textContent).not.toContain('Guard installed');
    expect(piece()?.textContent).not.toContain('No guard found');
  });

  it('draws 24 hour segments and keeps its nodes on a live answer', async () => {
    await mount('/guard');
    expect(piece()?.getAttribute('data-piece')).toBe('guard');
    expect(marks()).toHaveLength(24);
    const before = piece();
    const segments = [...(piece()?.querySelectorAll('path[pathLength]') ?? [])];
    expect(segments).toHaveLength(24);
    await act(async () => {
      await client.invalidateQueries();
    });
    expect(piece()).toBe(before);
    [...(piece()?.querySelectorAll('path[pathLength]') ?? [])].forEach((el, i) => expect(el).toBe(segments[i]));
  });

  it('redraws a new piece when the range switches', async () => {
    await mount('/guard');
    const before = piece();
    act(() => setRange('guard', '30d'));
    await vi.waitFor(() => expect(piece()).not.toBe(before));
  });
});
