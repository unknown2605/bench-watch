import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters, setClient } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { clientName } from '../../format/clients';
import { fixtures } from './testData';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

type Answer = { status: number; body: unknown };
type Clients = { id: string; verified: boolean }[];

const MANY: Clients = [
  { id: 'claude-code', verified: true },
  { id: 'codex', verified: true },
  { id: 'gemini-cli', verified: false },
];
const SPLIT = [
  { client: 'claude-code', tokens: 900_000, costUsd: 9, share: 0.75 },
  { client: 'gemini-cli', tokens: 300_000, costUsd: 0, share: 0.25 },
];

let clients: Clients = MANY;
let tune: (path: string, body: Record<string, unknown>) => Answer | null = () => null;
const calls: string[] = [];
let metaGate: Promise<void> | null = null;
const CLAUDE_ONLY = ['/api/context', '/api/subagents', '/api/fills', '/api/guard'];

function answer(path: string, client: string | null): Answer {
  const data = fixtures(Date.now());
  const map: Record<string, Record<string, unknown>> = {
    '/api/overview': { ...data.overview, byClient: SPLIT },
    '/api/tokens': { ...data.tokens, byClient: SPLIT },
    '/api/context': data.context,
    '/api/sessions': data.sessions,
    '/api/subagents': data.subagents,
    '/api/fills': data.fills,
    '/api/guard': data.guard,
    '/api/meta': { ...data.meta, clients },
    '/api/status': data.status,
  };
  const body = map[path];
  if (!body) return { status: 404, body: { code: 'not_found' } };
  if (client && client !== 'claude-code' && CLAUDE_ONLY.includes(path)) {
    return { status: 422, body: { code: 'not_for_client' } };
  }
  return tune(path, body) ?? { status: 200, body };
}

beforeAll(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  HTMLCanvasElement.prototype.getContext = () => null;
  window.scrollTo = () => undefined;
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://app.test');
    calls.push(`${url.pathname}?${url.searchParams.toString()}`);
    if (url.pathname === '/api/meta' && metaGate) await metaGate;
    const a = answer(url.pathname, url.searchParams.get('client'));
    return new Response(JSON.stringify(a.body), { status: a.status, headers: { 'Content-Type': 'application/json' } });
  });
});

let root: Root | null = null;
let host: HTMLElement | null = null;

beforeEach(() => {
  clients = MANY;
  tune = () => null;
  metaGate = null;
  calls.length = 0;
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
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  const query = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => {
    root?.render(
      <QueryClientProvider client={query}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
  });
  await vi.waitFor(() => expect(document.querySelector('main')).not.toBeNull(), { timeout: 5000 });
}

const text = () => document.body.textContent ?? '';
const clientButton = () => [...document.querySelectorAll('main button')].find((b) => b.textContent?.startsWith('Client'));

describe('client filter', () => {
  it('lists only the clients found, sends the pick as ?client= and keys the query by it', async () => {
    await mount('/tokens');
    await vi.waitFor(() => expect(clientButton()).not.toBeNull(), { timeout: 5000 });
    await act(async () => clientButton()?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
    await act(async () => clientButton()?.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true })));
    const options = [...document.querySelectorAll('[role="option"]')].map((o) => o.textContent);
    expect(options).toEqual(['All clients', 'Claude Code', 'Codex', 'Gemini CLI · format unverified']);
    const tokenCalls = calls.filter((c) => c.startsWith('/api/tokens'));
    expect(tokenCalls.length).toBeGreaterThan(0);
    expect(tokenCalls.every((c) => !c.includes('client='))).toBe(true);
    await act(async () => setClient('codex'));
    await vi.waitFor(() => expect(calls).toContain('/api/tokens?range=today&client=codex'), { timeout: 5000 });
    expect(calls).toContain('/api/tokens?range=14d&client=codex');
  });

  it('is hidden while a single client is found', async () => {
    clients = [{ id: 'claude-code', verified: true }];
    tune = (path, body) => (path === '/api/tokens'
      ? { status: 200, body: { ...body, byClient: SPLIT.slice(0, 1) } } : null);
    await mount('/tokens');
    await vi.waitFor(() => expect(text()).toContain('$1,235'), { timeout: 5000 });
    expect(clientButton()).toBeUndefined();
    expect(text()).not.toContain('By client');
  });
});

describe('per-client split', () => {
  it.each(['/', '/tokens'])('%s shows tokens and cost by client, unverified ones labelled', async (path) => {
    await mount(path);
    await vi.waitFor(() => expect(text()).toContain('By client'), { timeout: 5000 });
    expect(text()).toContain('Claude Code');
    expect(text()).toContain('Gemini CLI · format unverified');
    expect(text()).toContain('900k tok · 75%');
    expect(text()).toContain('$9.00');
  });

  it.each([['/', '$12.35'], ['/tokens', '$1,235']])('%s hides the split under a client filter', async (path, shown) => {
    setClient('codex');
    await mount(path);
    await vi.waitFor(() => expect(calls.some((c) => c.includes('client=codex'))).toBe(true), { timeout: 5000 });
    await vi.waitFor(() => expect(text()).toContain(shown), { timeout: 5000 });
    expect(text()).not.toContain('By client');
  });

  it.each(['/', '/tokens'])('%s does not call a client verified while /api/meta is unavailable', async (path) => {
    tune = (p) => (p === '/api/meta' ? { status: 404, body: { code: 'not_found' } } : null);
    await mount(path);
    // Under load the panel can render before the 404 lands; the label is deferred until then by design.
    await vi.waitFor(() => expect(text()).toContain('Claude Code · format unverified'), { timeout: 5000 });
    expect(text()).toContain('By client');
    expect(text()).not.toMatch(/Claude Code(?! · format unverified)/);
  });

  it.each(['/', '/tokens'])('%s labels nothing unverified while /api/meta is loading, then labels Gemini CLI', async (path) => {
    let release: () => void = () => undefined;
    metaGate = new Promise<void>((r) => { release = r; });
    await mount(path);
    await vi.waitFor(() => expect(text()).toContain('By client'), { timeout: 5000 });
    expect(text()).not.toContain('format unverified');
    await act(async () => release());
    await vi.waitFor(() => expect(text()).toContain('Gemini CLI · format unverified'), { timeout: 5000 });
    expect(text()).not.toMatch(/Claude Code · format unverified/);
  });

  it('keeps the n% of tokens line beside the recorded-cost note on the Tokens tab', async () => {
    tune = (p, body) => (p === '/api/tokens'
      ? { status: 200, body: { ...body, recorded: { clients: ['copilot-cli'], tokens: 500, costUsd: 0.5 } } } : null);
    await mount('/tokens');
    await vi.waitFor(() => expect(text()).toContain('Copilot CLI: $0.50 unsplit'), { timeout: 5000 });
    expect(text()).toMatch(/\w[\w ]*: \d+% tok, \d+% cost/);
  });

  it('names an id that is only inherited from Object as other', () => {
    expect(clientName('constructor')).toBe('other');
    expect(clientName('toString')).toBe('other');
    expect(clientName('codex')).toBe('Codex');
  });
});

describe('Claude-only figures under another client', () => {
  it.each(['/context', '/sub-agents', '/fills', '/guard'])('%s says not available, no error screen', async (path) => {
    setClient('codex');
    await mount(path);
    await vi.waitFor(() => expect(text()).toContain('Not available for this client'), { timeout: 5000 });
    const api = path === '/sub-agents' ? '/api/subagents' : `/api${path}`;
    await vi.waitFor(
      () => expect(calls.some((c) => c.startsWith(`${api}?`) && c.includes('client=codex'))).toBe(true),
      { timeout: 5000 },
    );
    await act(async () => { await Promise.resolve(); });
    expect(text()).toContain('Not available for this client');
    expect(document.querySelector('[role="alert"]')).toBeNull();
    expect(text()).not.toContain('Could not load');
    expect(text()).not.toContain('966k');
  });

  it('drops a stored client the meta answer no longer lists, so the page is not stuck refused', async () => {
    clients = [{ id: 'claude-code', verified: true }];
    setClient('codex');
    await mount('/context');
    await vi.waitFor(() => expect(text()).toContain('966k'), { timeout: 5000 });
    expect(text()).not.toContain('Not available for this client');
    expect(calls.some((c) => c.startsWith('/api/context?') && !c.includes('client='))).toBe(true);
  });

  it('shows the same notice when the API answers 422 not_for_client', async () => {
    tune = (path) => (path === '/api/context' ? { status: 422, body: { code: 'not_for_client' } } : null);
    await mount('/context');
    await vi.waitFor(() => expect(text()).toContain('Not available for this client'), { timeout: 5000 });
    expect(document.querySelector('[role="alert"]')).toBeNull();
  });

  it('marks the Claude-only panels of Overview and Sessions when claudeOnly.available is false', async () => {
    tune = (path, body) => (path === '/api/overview' || path === '/api/sessions'
      ? { status: 200, body: { ...body, claudeOnly: { available: false } } } : null);
    await mount('/');
    await vi.waitFor(() => expect(text()).toContain('Not available for this client'), { timeout: 5000 });
    expect(text()).not.toContain('700k+');
    act(() => root?.unmount());
    host?.remove();
    await mount('/sessions');
    await vi.waitFor(() => expect(text()).toContain('Not available for this client'), { timeout: 5000 });
    expect(document.querySelector('main')?.textContent).not.toContain('180k → 40k');
  });
});

describe('context limit unknown', () => {
  it('Overview shows the token count alone, with no bar and no limit', async () => {
    tune = (path, body) => {
      if (path !== '/api/overview') return null;
      const live = (body['liveSessions'] as Record<string, unknown>[]).map((s) => ({ ...s, contextLimitTokens: 0 }));
      return { status: 200, body: { ...body, liveSessions: live } };
    };
    await mount('/');
    await vi.waitFor(() => expect(text()).toContain('171k'), { timeout: 5000 });
    expect(text()).not.toContain('171k / ');
    expect(document.querySelector('[aria-label="benchwatch fill"]')).toBeNull();
  });

  it('the session detail shows the count without a context bar or limit', async () => {
    const data = fixtures(Date.now());
    const detail = { ...data.detail, summary: { ...data.detail.summary, contextLimitTokens: 0 } };
    const prev = globalThis.fetch;
    vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://app.test');
      if (url.pathname.startsWith('/api/sessions/')) {
        return new Response(JSON.stringify(detail), { status: 200, headers: { 'Content-Type': 'application/json' } });
      }
      return prev(input);
    });
    try {
      await mount('/sessions');
      await vi.waitFor(() => expect(text()).toContain('Context 150k'), { timeout: 5000 });
      expect(text()).not.toContain('limit');
      expect(document.querySelector('[role="meter"][aria-label^="Context"]')).toBeNull();
    } finally {
      vi.stubGlobal('fetch', prev);
    }
  });
});

describe('unpriced models', () => {
  const unpriced = [{ client: 'gemini-cli', model: 'gemini-3-pro' }];
  it.each(['/', '/tokens'])('%s notes that cost excludes them', async (path) => {
    tune = (p, body) => (p === '/api/overview' || p === '/api/tokens'
      ? { status: 200, body: { ...body, unpriced } } : null);
    await mount(path);
    await vi.waitFor(
      () => expect(text()).toContain('Cost excludes unpriced models: Gemini CLI gemini-3-pro'),
      { timeout: 5000 },
    );
    expect(document.querySelector('[role="status"]')?.textContent ?? '').not.toContain('Cost excludes');
    expect(document.querySelector('[title^="Cost excludes unpriced models"]')).not.toBeNull();
  });

  it.each(['/', '/tokens'])('%s shows no note when every call is priced', async (path) => {
    await mount(path);
    await vi.waitFor(() => expect(text()).toContain('By client'), { timeout: 5000 });
    expect(text()).not.toContain('Cost excludes');
  });
});
