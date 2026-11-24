import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { fixtures } from './testData';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let guard: Record<string, unknown>;
let root: Root | null = null;
let host: HTMLElement | null = null;
const SKIPPED = { malformed: 0, oversized: 0, badVersion: 0, badRule: 0, notRegular: 0 };
const NO_LOG = {
  calls: { value: 0, previous: null }, decisions: null, byTool: [], subAgent: null, callsByDay: [], hours: null,
  rules: [], lastBlock: null, trend: [], toolErrors: [],
};

beforeAll(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  HTMLCanvasElement.prototype.getContext = () => null;
  window.scrollTo = () => undefined;
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    const ok = new URL(String(input), 'http://app.test').pathname === '/api/guard';
    return new Response(JSON.stringify(ok ? guard : { code: 'not_found' }), {
      status: ok ? 200 : 404,
      headers: { 'Content-Type': 'application/json' },
    });
  });
});

beforeEach(() => {
  guard = { ...fixtures(Date.now()).guard } as Record<string, unknown>;
  resetFilters();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount() {
  const router = createAppRouter(createMemoryHistory({ initialEntries: ['/guard'] }));
  host = document.body.appendChild(document.createElement('div'));
  root = createRoot(host);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => {
    root?.render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
  });
  await vi.waitFor(() => expect(host?.querySelector('main')).not.toBeNull());
}

const status = () => host?.querySelector<HTMLElement>('[data-log-status]') ?? null;
const text = () => host?.textContent ?? '';

describe('Guard tab log status', () => {
  it('shows the read time with the figures when the log is ok, and the mode caveat', async () => {
    guard.log = { ...(guard.log as object), lastReadAt: new Date().toISOString() };
    await mount();
    await vi.waitFor(() => expect(status()?.textContent).toMatch(/^Log ok · read Today \d\d:\d\d$/));
    expect(status()?.querySelector('br')).toBeNull();
    expect(status()?.querySelector('span')).toBeNull();
    expect(text()).toContain('Configured in the mode file · not proof of what ran');
    expect(text()).toContain('840');
  });

  it('shows dashes, never zeros, for ok with no decisions', async () => {
    guard = { ...guard, decisions: null, byTool: [], hours: null, rules: [], callsByDay: [], trend: [] };
    await mount();
    await vi.waitFor(() => expect(status()).not.toBeNull());
    expect(host?.querySelector('[data-piece]')?.textContent).toContain('Blocked—');
    expect(text()).not.toMatch(/Blocked\s*0\b/);
    expect(text()).not.toContain('Allow');
  });

  it('shows the empty state for an absent log with no install and no figures', async () => {
    guard = {
      ...guard, ...NO_LOG, install: { source: 'none', bash: 'unknown', file: 'unknown' },
      log: { state: 'absent', skipped: SKIPPED },
    };
    await mount();
    await vi.waitFor(() => expect(text()).toContain('Guard report not available yet'));
  });

  it('titles and labels the trend panel as blocked results when only the transcripts have data', async () => {
    const trend = [{ day: fixtures(Date.now()).guard.period.to, results: 50, blocked: 3 }];
    guard = { ...guard, ...NO_LOG, trend, log: { state: 'absent', skipped: SKIPPED } };
    await mount();
    await vi.waitFor(() => expect(text()).toContain('Blocked results per day · last 14 d'));
    expect(text()).not.toContain('Calls per day');
    expect(host?.innerHTML).toContain('Blocked tool results per day');
    expect(host?.innerHTML).toContain('3 blocked of 50 results');
  });

  it('keeps the figures and shows the missing status when the log files are gone', async () => {
    guard = { ...guard, log: { state: 'missing', skipped: SKIPPED } };
    await mount();
    await vi.waitFor(() => expect(status()?.textContent).toBe('Log missing · stored figures'));
    expect(text()).toContain('840');
    expect(text()).toContain('git-force-push');
    expect(text()).not.toContain('Guard report not available yet');
  });

  it('shows an unreadable log with its code and skipped counts beside the stored figures', async () => {
    guard = { ...guard, log: { state: 'unreadable', code: 'ENOTREGULAR', skipped: { ...SKIPPED, malformed: 2, notRegular: 1 } } };
    await mount();
    await vi.waitFor(() => expect(status()?.textContent).toContain('Log unreadable · ENOTREGULAR'));
    expect(status()?.textContent).toContain('Skipped 2 malformed, 1 notRegular');
    expect(text()).toContain('git-force-push');
  });

  it('shows the skipped breakdown as visible text on a second line, with no title', async () => {
    guard = { ...guard, log: { state: 'unreadable', code: 'ENOTREGULAR', skipped: { ...SKIPPED, malformed: 2, notRegular: 1 } } };
    await mount();
    await vi.waitFor(() => expect(status()).not.toBeNull());
    expect(status()?.title).toBe('');
    const spans = [...(status()?.querySelectorAll('span') ?? [])];
    expect(spans).toHaveLength(1);
    expect(spans[0]?.textContent).toBe('. ');
    expect(status()?.textContent).toContain('. Skipped 2 malformed, 1 notRegular');
    expect(status()?.innerHTML).toMatch(/<\/span><br>Skipped 2 malformed, 1 notRegular/);
  });

  it('shows the day-aware read time for an old read and for an unreadable log', async () => {
    const old = new Date(Date.now() - 3 * 86_400_000).toISOString();
    guard = { ...guard, log: { state: 'unreadable', code: 'EIO', lastReadAt: old, skipped: SKIPPED } };
    await mount();
    await vi.waitFor(() => expect(status()?.textContent).toMatch(/^Log unreadable · EIO · read (?!Today)\S+ \S* ?\d\d:\d\d$/));
  });

  it('shows fixed text for a log state this build does not know, never the raw state', async () => {
    guard = { ...guard, log: { state: 'quantum-<b>x', skipped: SKIPPED }, decisions: null, rules: [], callsByDay: [] };
    await mount();
    await vi.waitFor(() => expect(status()?.textContent).toBe('Log state unknown'));
    expect(text()).not.toContain('quantum');
    expect(text()).toContain('The guard log state is not known');
  });

  it('renders a guard body without trend or toolErrors as empty lists', async () => {
    guard = Object.fromEntries(Object.entries(guard).filter(([k]) => k !== 'trend' && k !== 'toolErrors'));
    await mount();
    await vi.waitFor(() => expect(status()).not.toBeNull());
    expect(host?.querySelector('main')).not.toBeNull();
    expect(text()).toContain('Blocked by rule');
  });

  it('scrolls the rule rows in a labelled, focusable region and keeps the heading outside it', async () => {
    await mount();
    await vi.waitFor(() => expect(host?.querySelector('[role="region"][aria-label="Blocked by rule rows"]')).not.toBeNull());
    const region = host?.querySelector<HTMLElement>('[role="region"][aria-label="Blocked by rule rows"]');
    expect(region?.tabIndex).toBe(0);
    const panel = region?.closest('section');
    expect(panel?.textContent).toContain('Blocked by rule');
    const heading = [...(panel?.querySelectorAll('h1,h2,h3,h4,header,[class*="title"]') ?? [])]
      .find((e) => e.textContent?.includes('Blocked by rule'));
    expect(heading).toBeDefined();
    expect(region?.contains(heading as Node)).toBe(false);
    expect(region?.textContent).toContain('git-force-push');
  });

  it('has no empty tab stop when there are no rule rows', async () => {
    guard = { ...guard, rules: [] };
    await mount();
    await vi.waitFor(() => expect(status()).not.toBeNull());
    expect(host?.querySelector('[aria-label="Blocked by rule rows"]')).toBeNull();
    expect(host?.querySelector('section[aria-label="Blocked by rule"] [tabindex]')).toBeNull();
  });
});
