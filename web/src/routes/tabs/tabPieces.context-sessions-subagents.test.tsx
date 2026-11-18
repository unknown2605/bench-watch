import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters, setRange } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { contextView } from './Context';
import { sessionsView } from './Sessions';
import { subagentsView } from './SubAgents';
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
const isMark = (el: Element) => ['img', 'button'].includes(el.getAttribute('role') ?? '') || el.tagName === 'BUTTON';
const marks = () => [...(piece()?.querySelectorAll('[aria-label]') ?? [])].filter(isMark);
const labels = () => marks().map((el) => el.getAttribute('aria-label') ?? '');
const hasLabel = (text: string) => labels().some((l) => l.includes(text));

async function refetch() {
  await act(async () => {
    await client.invalidateQueries();
  });
}

describe('Context centre piece', () => {
  it('feeds the piece the bands and figures of the response', () => {
    const v = contextView(data.context, new Date());
    expect(v.piece.defs).toEqual(data.context.contextBands.map((b) => ({ minTokens: b.minTokens, maxTokens: b.maxTokens })));
    expect(v.piece.bands).toEqual(
      data.context.bands.map((b) => ({
        minTokens: b.band.minTokens,
        calls: b.calls,
        contextTokens: b.contextTokens,
        cacheReadTokens: b.cacheReadTokens,
      })),
    );
    expect(v.piece).toMatchObject({ avgPerCall: 45_000, compactions: 2, rangeLabel: '7 d' });
  });

  it('draws the response and keeps its nodes on a live answer', async () => {
    await mount('/context');
    expect(piece()?.getAttribute('data-piece')).toBe('context');
    expect(piece()?.textContent).toContain('Context bands · 7 d');
    expect(piece()?.textContent).toContain('304 calls');
    const before = piece();
    const nodes = marks();
    const first = data.context.bands[0];
    if (first) first.calls = 696;
    await refetch();
    await vi.waitFor(() => expect(piece()?.textContent).toContain('700 calls'));
    expect(piece()).toBe(before);
    marks().forEach((el, i) => expect(el).toBe(nodes[i]));
  });

  it('redraws a new piece when the range switches', async () => {
    await mount('/context');
    const before = piece();
    const first = data.context.bands[0];
    if (first) first.calls = 296;
    act(() => setRange('context', '30d'));
    await vi.waitFor(() => expect(piece()?.textContent).toContain('300 calls'));
    expect(piece()).not.toBe(before);
  });
});

describe('Sessions centre piece', () => {
  it('feeds the piece every session and project of the response', () => {
    const v = sessionsView(data.sessions, new Date());
    expect(v.piece.sessions.map((s) => s.tokens)).toEqual(data.sessions.sessions.map((s) => s.tokens));
    expect(v.piece.sessions.map((s) => s.state)).toEqual(['live', 'ended']);
    expect(v.piece.projects.map((p) => p.tokens)).toEqual(data.sessions.projects.map((p) => p.tokens));
    expect(v.piece).toMatchObject({ total: 3, live: 1 });
  });

  it('draws the response and keeps its nodes on a live answer', async () => {
    await mount('/sessions');
    expect(piece()?.getAttribute('data-piece')).toBe('sessions');
    expect(hasLabel('Rank 1: benchwatch')).toBe(true);
    const before = piece();
    const nodes = marks();
    const first = data.sessions.sessions[0];
    if (first) first.tokens = 2_500_000;
    await refetch();
    await vi.waitFor(() => expect(hasLabel('2.50M tokens')).toBe(true));
    expect(piece()).toBe(before);
    marks().forEach((el, i) => expect(el).toBe(nodes[i]));
  });

  it('redraws a new piece when the range switches', async () => {
    await mount('/sessions');
    const before = piece();
    const first = data.sessions.sessions[0];
    if (first) first.tokens = 3_500_000;
    act(() => setRange('sessions', '30d'));
    await vi.waitFor(() => expect(hasLabel('3.50M tokens')).toBe(true));
    expect(piece()).not.toBe(before);
  });
});

describe('Sub-agents centre piece', () => {
  it('feeds the piece every type of the response', () => {
    const v = subagentsView(data.subagents);
    expect(v.piece.types.map((t) => t.runs)).toEqual(data.subagents.types.map((t) => t.runs));
    expect(v.piece.types[0]?.name).toBe('code-reviewer');
    expect(v.piece).toMatchObject({ mainShare: 0.7, rangeLabel: '7 d' });
  });

  it('draws the response and keeps its nodes on a live answer', async () => {
    await mount('/sub-agents');
    expect(piece()?.getAttribute('data-piece')).toBe('subagents');
    expect(piece()?.textContent).toContain('Runs · 7 d');
    expect(hasLabel('code-reviewer, 30 runs')).toBe(true);
    const before = piece();
    const nodes = marks();
    const first = data.subagents.types[0];
    if (first) first.runs = 55;
    await refetch();
    await vi.waitFor(() => expect(hasLabel('code-reviewer, 55 runs')).toBe(true));
    expect(piece()).toBe(before);
    marks().forEach((el, i) => expect(el).toBe(nodes[i]));
  });

  it('redraws a new piece when the range switches', async () => {
    await mount('/sub-agents');
    const before = piece();
    const first = data.subagents.types[0];
    if (first) first.runs = 77;
    act(() => setRange('sub-agents', '30d'));
    await vi.waitFor(() => expect(hasLabel('code-reviewer, 77 runs')).toBe(true));
    expect(piece()).not.toBe(before);
  });
});
