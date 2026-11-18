import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { CANARY, fixtures } from './testData';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

type Answer = { status: number; body: unknown };
let answer: (path: string) => Answer;
const calls: string[] = [];

function routes(path: string): Answer {
  const data = fixtures(Date.now());
  const map: Record<string, unknown> = {
    '/api/overview': data.overview,
    '/api/tokens': data.tokens,
    '/api/context': data.context,
    '/api/sessions': data.sessions,
    '/api/sessions/0a1b2c3d': data.detail,
    '/api/subagents': data.subagents,
    '/api/meta': data.meta,
    '/api/status': data.status,
    '/api/fills': data.fills,
    '/api/guard': data.guard,
    '/api/machine': data.machine,
    '/api/docker': data.docker,
  };
  return path in map ? { status: 200, body: map[path] } : { status: 404, body: { code: 'not_found' } };
}

beforeAll(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  HTMLCanvasElement.prototype.getContext = () => null;
  window.scrollTo = () => undefined;
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://app.test');
    calls.push(`${url.pathname}?${url.searchParams.toString()}`);
    const a = answer(url.pathname);
    return new Response(JSON.stringify(a.body), { status: a.status, headers: { 'Content-Type': 'application/json' } });
  });
});

let root: Root | null = null;
let host: HTMLElement | null = null;

beforeEach(() => {
  answer = routes;
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
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => {
    root?.render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
  });
  await vi.waitFor(() => expect(document.querySelector('main')).not.toBeNull());
}

const text = () => document.body.textContent ?? '';

function leaks(): string[] {
  const out: string[] = [];
  for (const el of document.body.querySelectorAll('*')) {
    for (const attr of el.attributes) if (attr.value.includes(CANARY)) out.push(`${el.tagName}[${attr.name}]`);
  }
  if (text().includes(CANARY)) out.push('text');
  return out;
}

const CASES: { path: string; figures: string[] }[] = [
  { path: '/', figures: ['$12.35', '4,321', '96%', '1.28M', '▲ 28.4% vs prev 7 d', '171k / 200k', 'benchwatch', 'other', '700k+', '<100k'] },
  { path: '/tokens', figures: ['$1,235', '9.6k', '$176.37', 'peak $600.00 on 03 Nov', 'claude-opus-5-5', 'unknown', '▲ 23.5% vs prev 7 d'] },
  {
    path: '/context',
    figures: ['966k', '04 Nov · 483% of 200k · benchwatch', '45k', '90.0%', '2 compactions · avg freed 120k', '180k → 40k'],
  },
  { path: '/sessions', figures: ['benchwatch', 'other', 'code-reviewer', 'Context 150k (limit 200k)', '700k', '180k → 40k'] },
  { path: '/sub-agents', figures: ['▼ 12 runs vs prev 7 d', '28% of $12.35 total', '$3.50', '10.0', '12k', '50%', 'code-reviewer', 'unknown'] },
  { path: '/fills', figures: ['Read', 'benchwatch', '40k', 'Top tool results'] },
  { path: '/guard', figures: ['840', '▲ 20.0% vs prev', 'git-force-push', '3.0 ms', 'Tool errors'] },
  { path: '/machine', figures: ['23%', 'load 2.41 · 2.18 · 1.96', 'swap 0 B used', 'Chrome', '412 GB', 'P1', 'E1'] },
  { path: '/docker', figures: ['2 healthy · 1 warning', 'php84-1', 'stack_full', 'mariadb', '4.0 GB could be freed', 'restarted'] },
];

describe('transcript tabs', () => {
  it.each(CASES)('$path shows the API figures and no free text', async ({ path, figures }) => {
    await mount(path);
    for (const figure of figures) await vi.waitFor(() => expect(text(), figure).toContain(figure));
    expect(leaks()).toEqual([]);
    expect(document.querySelectorAll('main')).toHaveLength(1);
  });

  it('shows the live status in the top bar once /api/status answers', async () => {
    await mount('/guard');
    await vi.waitFor(() => expect(text()).toContain('ALL NOMINAL'));
    expect(text()).toContain('2 LIVE SESSIONS');
    expect(text()).not.toContain('NO DATA YET');
  });

  it('says history is paused when the database is down', async () => {
    answer = (path) => (path === '/api/meta' ? routes(path) : { status: 503, body: { code: 'history_paused', detail: CANARY } });
    await mount('/tokens');
    // useTranscript retries a 5xx twice with backoff before the error shows.
    await vi.waitFor(() => expect(document.querySelector('[role="alert"]')?.textContent).toBe('History paused · database unreachable'), {
      timeout: 8000,
    });
    expect(leaks()).toEqual([]);
  }, 12_000);

  it('shows the empty state for a period with no calls', async () => {
    answer = (path) => {
      const data = fixtures(Date.now());
      if (path !== '/api/overview') return routes(path);
      return { status: 200, body: { ...data.overview, calls: { value: 0, previous: null }, tokens: { value: 0, previous: null } } };
    };
    await mount('/');
    await vi.waitFor(() => expect(text()).toContain('No calls in this period'));
  });

  it('asks the API again when a range chip is pressed', async () => {
    await mount('/context');
    await vi.waitFor(() => expect(text()).toContain('966k'));
    const chip = [...document.querySelectorAll<HTMLButtonElement>('[role="group"][aria-label="Period"] button')].find((b) => b.textContent === '7d');
    expect(chip).toBeDefined();
    await act(async () => chip?.click());
    await vi.waitFor(() => expect(calls).toContain('/api/context?range=7d'));
    expect(chip?.getAttribute('aria-pressed')).toBe('true');
  });

  it('selects another agent type from the table by keyboard-reachable buttons', async () => {
    await mount('/sub-agents');
    await vi.waitFor(() => expect(text()).toContain('code-reviewer'));
    const buttons = [...document.querySelectorAll<HTMLButtonElement>('table button')];
    expect(buttons.map((b) => b.textContent)).toEqual(['code-reviewer', 'unknown']);
    await act(async () => buttons[1]?.click());
    expect(buttons[1]?.getAttribute('aria-pressed')).toBe('true');
  });

  it('drills down on Fills: picking Grep filters the selected panel and the by-project panel', async () => {
    await mount('/fills');
    await vi.waitFor(() => expect(document.querySelector('[role="button"][aria-label*="Grep"]')).not.toBeNull());
    const arc = document.querySelector<SVGElement>('[role="button"][aria-label*="Grep"]');
    await act(async () => arc?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
    expect(arc?.getAttribute('aria-pressed')).toBe('true');
    const selected = () => document.querySelector('section[aria-label^="Selected"]')?.textContent;
    await vi.waitFor(() => expect(selected()).toContain('Grep'));
    expect(document.querySelector('section[aria-label="By project · Grep"]')).not.toBeNull();
  });

  it('reports the picked arc as pressed, names the ring as a group, and clears on a second click', async () => {
    await mount('/fills');
    const ring = () => document.querySelector('svg[role="group"][aria-label="Tool slices"]');
    await vi.waitFor(() => expect(ring()).not.toBeNull());
    const arc = document.querySelector<SVGElement>('[role="button"][aria-label*="Grep"]');
    const read = document.querySelector<SVGElement>('[role="button"][aria-label*="Read: whole"]');
    expect(arc?.getAttribute('aria-pressed')).toBe('false');
    await act(async () => arc?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
    expect(arc?.getAttribute('aria-pressed')).toBe('true');
    expect(read?.getAttribute('aria-pressed')).toBe('false');
    await act(async () => arc?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
    expect(arc?.getAttribute('aria-pressed')).toBe('false');
    const title = document.querySelector('section[aria-label^="Selected"]')?.getAttribute('aria-label');
    expect(title).toBe('Selected · Read');
  });

  it('keeps list keys unique for projects and agent types that share a clipped or collapsed name', async () => {
    const errors = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const prefix = 'a'.repeat(30);
    answer = (path) => {
      const data = fixtures(Date.now());
      if (path === '/api/context') {
        const project = (n: string) => ({ ...data.context.byProject[0], project: `${prefix}${n}` });
        return { status: 200, body: { ...data.context, byProject: [project('1'), project('2')] } };
      }
      if (path === '/api/subagents') {
        const [a, b] = data.subagents.types;
        return { status: 200, body: { ...data.subagents, types: [b, b, a] } };
      }
      return routes(path);
    };
    try {
      for (const path of ['/context', '/sub-agents']) {
        await mount(path);
        await vi.waitFor(() => expect(document.querySelector('[data-page]')).not.toBeNull());
        await act(async () => root?.unmount());
        host?.remove();
      }
      expect(errors.mock.calls.filter((c) => String(c[0]).includes('same key'))).toEqual([]);
    } finally {
      errors.mockRestore();
    }
  });

  it('keeps the whole project name in the title and the text, never a JS-cut copy', async () => {
    const long = `www-self-projects-${'x'.repeat(40)}`;
    answer = (path) => {
      const data = fixtures(Date.now());
      if (path === '/api/context') {
        const row = { ...data.context.byProject[0], project: long };
        return { status: 200, body: { ...data.context, byProject: [row] } };
      }
      return routes(path);
    };
    await mount('/context');
    await vi.waitFor(() => expect(document.querySelector(`[title="${long}"]`)).not.toBeNull());
    const named = [...document.querySelectorAll('[title]')].filter((e) => e.textContent === long);
    expect(named.length).toBeGreaterThan(0);
    expect(text()).not.toContain('…');
    expect(document.querySelector(`[aria-label="${long} fill"]`)).not.toBeNull();
  });

  it('redraws Machine from history on 24h and says so when history is paused', async () => {
    const before = fixtures(Date.now()).machine;
    answer = (path) =>
      path === '/api/machine' ? { status: 200, body: { ...before, range: '24h', historyPaused: true } } : routes(path);
    await mount('/machine');
    const chip = [...document.querySelectorAll<HTMLButtonElement>('[role="group"][aria-label="Range"] button')].find((b) => b.textContent === '24h');
    await act(async () => chip?.click());
    await vi.waitFor(() => expect(calls).toContain('/api/machine?range=24h'));
    await vi.waitFor(() => expect(text()).toContain('History paused'));
    expect(text()).toContain('Updated');
  });

  it('opens the Sessions tab with the session picked through the URL', async () => {
    await mount('/sessions?session=0a1b2c3d&project=benchwatch');
    await vi.waitFor(() => expect(calls).toContain('/api/sessions/0a1b2c3d?project=benchwatch'));
  });

  it('keeps the LIVE badge outside the clipped project name in the session detail', async () => {
    await mount('/sessions?session=0a1b2c3d&project=benchwatch');
    const badge = await vi.waitFor(() => {
      const b = [...document.querySelectorAll('p > span')].find((s) => s.textContent === 'LIVE' && !s.children.length);
      expect(b).toBeDefined();
      return b as HTMLElement;
    });
    const name = badge.previousElementSibling as HTMLElement;
    expect(name.getAttribute('title')).toBe(name.textContent);
    expect(badge.closest('[title]')).toBeNull();
  });
});
