import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters, setRange } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { tokensView } from './Tokens';
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
const labels = () => marks().map((el) => el.getAttribute('aria-label'));

describe('Overview centre piece', () => {
  it('draws the response figures', async () => {
    await mount('/');
    expect(piece()?.getAttribute('data-piece')).toBe('overview');
    expect(piece()?.textContent).toContain('Tokens · 7 d');
    expect(piece()?.textContent).toContain('MAIN 70% · SUB 30%');
    expect(labels()).toEqual(expect.arrayContaining(['Re-read: 96%', 'Sub-agent: 30%']));
  });

  it('updates in place on a live answer and keeps the same nodes', async () => {
    await mount('/');
    const before = piece();
    const arcs = marks();
    data.overview.scopeShare = { main: 0.4, sub: 0.6 };
    await act(async () => {
      await client.invalidateQueries();
    });
    await vi.waitFor(() => expect(labels()).toContain('Sub-agent: 60%'));
    expect(piece()).toBe(before);
    marks().forEach((el, i) => expect(el).toBe(arcs[i]));
  });

  it('redraws a new piece when the range switches', async () => {
    await mount('/');
    const before = piece();
    data.overview.scopeShare = { main: 0.2, sub: 0.8 };
    act(() => setRange('overview', '30d'));
    await vi.waitFor(() => expect(labels()).toContain('Sub-agent: 80%'));
    expect(piece()).not.toBe(before);
  });
});

describe('Tokens centre piece', () => {
  it('feeds the piece every kind of the response', () => {
    const v = tokensView(data.tokens, new Date());
    expect(v.piece.kinds).toEqual(data.tokens.byKind.map((k) => ({ kind: k.kind, tokens: k.tokens, costUsd: k.costUsd })));
    expect(v.piece).toMatchObject({ tokens: 9600, costUsd: data.tokens.costUsd.value, previousCost: data.tokens.costUsd.previous });
  });

  it('names the kind that costs more than its token share, in a short text and a full title', () => {
    expect(tokensView(data.tokens, new Date()).costly).toEqual({
      text: 'output: 6% tok, 73% cost',
      title: 'output: 6% of tokens, 73% of cost',
    });
  });

  it('says the split leaves out clients that record their own cost, beside the costly line', () => {
    expect(tokensView(data.tokens, new Date()).recorded).toBeNull();
    const d = { ...data.tokens, recorded: { clients: ['copilot-cli' as const, 'opencode' as const], tokens: 500, costUsd: 0.5 } };
    expect(tokensView(d, new Date()).recorded).toEqual({
      text: '2 clients: $0.50 unsplit',
      title: 'Copilot CLI, OpenCode: $0.50 recorded, no split',
    });
    const one = { ...d, recorded: { clients: ['codex' as const], tokens: 5, costUsd: 1234.5 } };
    expect(tokensView(one, new Date()).recorded?.text).toBe('Codex: $1,235 unsplit');
  });

  it('draws the kind shares of the response and keeps its nodes on a live answer', async () => {
    await mount('/tokens');
    expect(piece()?.getAttribute('data-piece')).toBe('tokens');
    expect(piece()?.textContent).toContain('cache read 62.5%');
    const before = piece();
    for (const k of data.tokens.byKind) if (k.kind === 'cacheRead') k.tokens = 16_000;
    await act(async () => {
      await client.invalidateQueries();
    });
    await vi.waitFor(() => expect(piece()?.textContent).toContain('cache read 81.6%'));
    expect(piece()).toBe(before);
  });

  it('redraws a new piece when the range switches', async () => {
    await mount('/tokens');
    const before = piece();
    for (const k of data.tokens.byKind) if (k.kind === 'cacheRead') k.tokens = 16_000;
    act(() => setRange('tokens', '30d'));
    await vi.waitFor(() => expect(piece()?.textContent).toContain('cache read 81.6%'));
    expect(piece()).not.toBe(before);
  });
});
