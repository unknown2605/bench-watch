import { onlineManager, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { createAppRouter } from './router';
import { navTabs, TAB_PATHS } from './tabs';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

beforeAll(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  HTMLCanvasElement.prototype.getContext = () => null;
  window.scrollTo = () => undefined;
  vi.stubGlobal('fetch', async () => new Response('{"code":"unavailable"}', { status: 503, headers: { 'Content-Type': 'application/json' } }));
});

let root: Root | null = null;
let host: HTMLElement | null = null;

afterEach(() => {
  vi.stubGlobal('fetch', async () => new Response('{"code":"unavailable"}', { status: 503, headers: { 'Content-Type': 'application/json' } }));
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const status = (docker: string, guard: string) => ({
  generatedAt: '2026-11-10T10:00:00Z',
  health: 'nominal',
  scan: { state: 'ok', lastRunAt: '2026-11-10T09:59:57Z', nextRunInSeconds: 4, toolErrors: 2 },
  machine: { state: 'ok' },
  docker: { state: docker },
  guard: { state: guard },
  database: { historyPaused: false },
  liveSessions: 0,
});
const serve = (body: unknown) =>
  vi.stubGlobal('fetch', async (url: string) =>
    String(url).startsWith('/api/status')
      ? new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
      : new Response('{"code":"unavailable"}', { status: 503, headers: { 'Content-Type': 'application/json' } }));

async function mount(path: string) {
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  const queryClient = new QueryClient();
  await act(async () => {
    root?.render(
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
  });
  await vi.waitFor(() => expect(document.querySelector('main')).not.toBeNull());
  return router;
}

const selectedTab = () => document.querySelector('[role="tab"][aria-selected="true"]');
const panelResolves = () => {
  const id = selectedTab()?.getAttribute('aria-controls');
  const panel = id ? document.getElementById(id) : null;
  return panel?.getAttribute('role') === 'tabpanel';
};

describe('shell navigation', () => {
  it('keeps aria-controls pointing at an existing panel after every client-side navigation', async () => {
    const router = await mount('/');
    expect(panelResolves()).toBe(true);
    for (const tab of navTabs) {
      await act(async () => {
        await router.navigate({ to: TAB_PATHS[tab.id] });
      });
      await vi.waitFor(() => expect(selectedTab()?.textContent).toBe(tab.label));
      expect(panelResolves(), `panel for ${tab.id}`).toBe(true);
    }
  });

  it('keeps it after a click on a tab', async () => {
    await mount('/');
    const sessions = [...document.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => t.textContent === 'Sessions');
    await act(async () => sessions?.click());
    await vi.waitFor(() => expect(selectedTab()?.textContent).toBe('Sessions'));
    expect(panelResolves()).toBe(true);
  });

  it('shows exactly one main landmark on each of the 9 tabs', async () => {
    serve(status('ok', 'ok'));
    for (const tab of navTabs) {
      await mount(TAB_PATHS[tab.id]);
      expect(document.querySelectorAll('main'), tab.id).toHaveLength(1);
      expect(document.querySelector('h1')?.textContent).toBe(tab.label);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it('shows the neutral no-data state, not live status', async () => {
    await mount('/');
    expect(document.body.textContent).toContain('NO DATA YET');
    expect(document.body.textContent).not.toContain('ALL NOMINAL');
  });

  it('answers an unknown path with a not-found page', async () => {
    await mount('/nope');
    expect(document.querySelectorAll('main')).toHaveLength(1);
    expect(document.querySelector('h1')?.textContent).toBe('Page not found');
    expect(document.querySelector('a[href="/"]')).not.toBeNull();
  });
});

describe('sections that are not on this machine', () => {
  const tabNames = () => [...document.querySelectorAll('[role="tab"]')].map((t) => t.textContent);

  it('drops the Docker and Guard tabs and the Overview link to Guard when both are absent', async () => {
    serve(status('disabled', 'disabled'));
    await mount('/');
    await vi.waitFor(() => expect(tabNames()).not.toContain('Docker'));
    expect(tabNames()).not.toContain('Guard');
    expect(tabNames()).toContain('Machine');
    expect(document.querySelector('a[href="/guard"]')).toBeNull();
  });

  it('keeps both tabs for Docker that is stopped and a guard that fails', async () => {
    serve(status('unavailable', 'failed'));
    await mount('/');
    await vi.waitFor(() => expect(document.body.textContent).toContain('ALL NOMINAL'));
    expect(tabNames()).toContain('Docker');
    expect(tabNames()).toContain('Guard');
  });

  it('keeps a hideable tab loading while /api/status retries and resolves after the final failure', async () => {
    const urls: string[] = [];
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => { release = r; });
    vi.stubGlobal('fetch', async (url: string) => {
      urls.push(String(url));
      if (urls.filter((u) => u.startsWith('/api/status')).length >= 2 && String(url).startsWith('/api/status')) await gate;
      return new Response('{"code":"unavailable"}', { status: 503, headers: { 'Content-Type': 'application/json' } });
    });
    const count = (prefix: string) => urls.filter((u) => u.startsWith(prefix)).length;
    const router = createAppRouter(createMemoryHistory({ initialEntries: ['/docker'] }));
    host = document.createElement('div');
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root?.render(
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retryDelay: 10 } } })}>
          <RouterProvider router={router} />
        </QueryClientProvider>,
      );
    });
    await vi.waitFor(() => expect(count('/api/status')).toBe(2), { timeout: 5000 });
    expect(document.querySelector('[role="status"]')?.textContent).toBe('Loading');
    expect(count('/api/docker')).toBe(0);
    release();
    await vi.waitFor(() => expect(count('/api/docker')).toBeGreaterThan(0), { timeout: 5000 });
    expect(count('/api/status')).toBeGreaterThanOrEqual(3);
  });

  it('stops waiting for /api/status while the browser is offline', async () => {
    onlineManager.setOnline(false);
    try {
      await mount('/docker');
      await vi.waitFor(() => expect(document.querySelector('[data-page="not-on-this-machine"]')).toBeNull());
      expect(tabNames()).toContain('Docker');
      expect(document.querySelector('[role="region"][aria-label="Docker"] [role="status"]')?.textContent).not.toBe('Loading');
    } finally {
      onlineManager.setOnline(true);
    }
  });

  it('answers a direct visit to a hidden route with a short state, not a crash', async () => {
    serve(status('disabled', 'ok'));
    await mount('/docker');
    await vi.waitFor(() => expect(document.querySelector('[data-page="not-on-this-machine"]')).not.toBeNull());
    expect(document.querySelectorAll('main')).toHaveLength(1);
    expect(document.body.textContent).toContain('Docker: not on this machine');
    expect(document.querySelector('a[href="/"]')).not.toBeNull();
    expect(document.querySelector('[role="tablist"]')).not.toBeNull();
    expect(document.activeElement).toBe(document.querySelector('h1'));
  });

  it('leaves no aria-labelledby pointing at a tab that is not rendered for a hidden route', async () => {
    serve(status('disabled', 'ok'));
    await mount('/docker');
    await vi.waitFor(() => expect(document.querySelector('[data-page="not-on-this-machine"]')).not.toBeNull());
    expect(document.querySelector('[role="tabpanel"]')).toBeNull();
    const ids = [...document.querySelectorAll('[aria-labelledby]')]
      .flatMap((el) => (el.getAttribute('aria-labelledby') ?? '').split(' ').filter(Boolean));
    for (const id of ids) expect(document.getElementById(id), id).not.toBeNull();
    expect(document.querySelector('[role="region"][aria-label="Docker"] main')).not.toBeNull();
  });

  it('requests nothing for a hidden section while the status is still pending', async () => {
    const urls: string[] = [];
    let release: (r: Response) => void = () => undefined;
    const pending = new Promise<Response>((resolve) => { release = resolve; });
    vi.stubGlobal('fetch', async (url: string) => {
      urls.push(String(url));
      return String(url).startsWith('/api/status')
        ? pending
        : new Response('{"code":"unavailable"}', { status: 503, headers: { 'Content-Type': 'application/json' } });
    });
    const router = createAppRouter(createMemoryHistory({ initialEntries: ['/docker'] }));
    host = document.createElement('div');
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root?.render(
        <QueryClientProvider client={new QueryClient()}>
          <RouterProvider router={router} />
        </QueryClientProvider>,
      );
    });
    await vi.waitFor(() => expect(urls.some((u) => u.startsWith('/api/status'))).toBe(true));
    expect(document.querySelector('[role="status"]')?.textContent).toBe('Loading');
    expect(urls.some((u) => u.startsWith('/api/docker'))).toBe(false);
    await act(async () => release(new Response(JSON.stringify(status('disabled', 'ok')),
      { status: 200, headers: { 'Content-Type': 'application/json' } })));
    await vi.waitFor(() => expect(document.querySelector('[data-page="not-on-this-machine"]')).not.toBeNull());
    expect(urls.some((u) => u.startsWith('/api/docker'))).toBe(false);
  });
});
