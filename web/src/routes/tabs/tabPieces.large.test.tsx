import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters, setRange } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import type { TabId } from '../../app/tabs';
import { dockerView } from './Docker';
import { fillsView } from './Fills';
import { guardView } from './Guard';
import { fixtures } from './testData';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

type Fixtures = ReturnType<typeof fixtures>;
let data: Fixtures;
let root: Root | null = null;
let host: HTMLElement | null = null;
let client: QueryClient;
let router: ReturnType<typeof createAppRouter>;
let fetched = 0;

function body(name: string): unknown {
  const id = name.startsWith('sessions/') ? name.slice('sessions/'.length) : null;
  const hit = id === null ? undefined : data.sessions.sessions.find((s) => s.session === id);
  if (hit) return { ...data.detail, summary: hit };
  return data[name as keyof Fixtures];
}

beforeAll(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  HTMLCanvasElement.prototype.getContext = () => null;
  window.scrollTo = () => undefined;
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    fetched += 1;
    const answer = body(new URL(String(input), 'http://app.test').pathname.replace('/api/', ''));
    return new Response(JSON.stringify(answer ?? { code: 'not_found' }), {
      status: answer ? 200 : 404,
      headers: { 'Content-Type': 'application/json' },
    });
  });
});

beforeEach(() => {
  data = fixtures(Date.now());
  resetFilters();
  fetched = 0;
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.restoreAllMocks();
});

const shown = (el: Element) => !el.closest('[aria-hidden="true"]');
const piece = () => [...(host?.querySelectorAll('[data-piece]') ?? [])].find(shown) ?? null;
const pieceText = () => piece()?.textContent ?? '';
const pageText = () => host?.textContent ?? '';
const pressed = () => [...(piece()?.querySelectorAll('[aria-pressed="true"]') ?? [])];
const panel = (title: string) => host?.querySelector(`section[aria-label^="${title}"]`) ?? null;
const item = (text: string) => [...(piece()?.querySelectorAll('li') ?? [])].find((el) => el.textContent === text) ?? null;
const seed = () => fixtures(0);

async function mount(path: string, tab: TabId, opts: { wait?: boolean } = {}) {
  act(() => setRange(tab, '30d'));
  router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
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
  if (opts.wait === false) {
    await vi.waitFor(() => expect(fetched).toBeGreaterThan(0));
    await act(async () => {
      await new Promise((r) => setTimeout(r, 60));
    });
    return;
  }
  await vi.waitFor(() => expect(piece()).not.toBeNull());
}

async function click(el: Element | null) {
  await act(async () => el?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
}

async function refetch() {
  await act(async () => {
    await client.invalidateQueries();
  });
}

const noBrokenNumbers = () => expect(pageText()).not.toMatch(/NaN|Infinity/);

function sessionList(n: number) {
  const base = seed().sessions.sessions[0];
  if (!base) throw new Error('fixture');
  return Array.from({ length: n }, (_, i) => ({
    ...base,
    session: `0000000${i.toString(16)}`,
    project: `proj-${String(i).padStart(2, '0')}`,
    state: i === 0 ? ('live' as const) : ('ended' as const),
    tokens: 900_000 - i * 10_000,
  }));
}

function agentTypes(n: number) {
  const base = seed().subagents.types[0];
  if (!base) throw new Error('fixture');
  return Array.from({ length: n }, (_, i) => ({ ...base, agentType: `agent-${i}`, runs: 100 - i, tokens: 500_000 - i * 1000 }));
}

const TOOLS = ['Read: whole', 'Grep', 'Glob', 'Edit', 'Write', 'Task', 'WebFetch', 'Bash: git'];

function toolStats(n: number) {
  const base = seed().fills.tools[0];
  if (!base) throw new Error('fixture');
  return TOOLS.slice(0, n).map((tool, i) => ({ ...base, tool, results: 100 - i * 7, tokens: 80_000 - i * 5000, weightedTokens: 240_000 - i * 15_000 }));
}

function containers(stack: string, n: number) {
  const base = data.docker.now?.containers[0];
  if (!base) throw new Error('fixture');
  return Array.from({ length: n }, (_, i) => ({ ...base, container: `${stack}-${String(i).padStart(2, '0')}`, stack, memBytes: (500 - i) * 1024 ** 2, restarts: 0 }));
}

describe('Sessions with more sessions than the piece draws', () => {
  it('counts the rest from the response and leaves "+N more" unselectable', async () => {
    data.sessions.sessions = sessionList(14);
    data.sessions.totals = { ...data.sessions.totals, sessions: 14, live: 1 };
    await mount('/sessions', 'sessions');
    expect(pieceText()).toContain('+2 more');
    expect(piece()?.querySelectorAll('ul button')).toHaveLength(12);
    const more = item('+2 more');
    expect(more).not.toBeNull();
    expect(more?.querySelector('button, [aria-pressed]')).toBeNull();
    expect(more?.hasAttribute('aria-pressed')).toBe(false);
    await vi.waitFor(() => expect(panel('Selected · session')?.textContent).toContain('proj-00'));
    const before = { search: router.state.location.search, panel: panel('Selected · session')?.textContent };
    await click(more);
    expect(router.state.location.search).toEqual(before.search);
    expect(panel('Selected · session')?.textContent).toBe(before.panel);
  });

  it('presses the live-first default the panel describes even when it ranks beyond the drawn slots', async () => {
    const list = sessionList(14);
    const last = list[13];
    if (last) Object.assign(last, { state: 'live', tokens: 1 });
    const first = list[0];
    if (first) first.state = 'ended';
    data.sessions.sessions = list;
    data.sessions.totals = { ...data.sessions.totals, sessions: 14, live: 1 };
    await mount('/sessions', 'sessions');
    await vi.waitFor(() => expect(pressed().length).toBeGreaterThan(0));
    const names = pressed().map((el) => el.getAttribute('aria-label') ?? el.textContent ?? '');
    expect(names.join(' ')).toContain('proj-00');
    await vi.waitFor(() => expect(panel('Selected · session')?.textContent).toContain('proj-00'));
  });

  it('shows no pressed mark for a picked session that the next response no longer has', async () => {
    data.sessions.sessions = sessionList(3);
    data.sessions.totals = { ...data.sessions.totals, sessions: 3, live: 1 };
    await mount('/sessions', 'sessions');
    await click(piece()?.querySelector('[aria-label^="Rank 2"]') ?? null);
    await vi.waitFor(() => expect(router.state.location.search).toMatchObject({ session: '00000001' }));
    await vi.waitFor(() => expect(pressed().length).toBeGreaterThan(0));
    data.sessions.sessions = sessionList(3).filter((s) => s.session !== '00000001');
    data.sessions.totals = { ...data.sessions.totals, sessions: 2, live: 1 };
    act(() => setRange('sessions', '7d'));
    await vi.waitFor(() => expect(pieceText()).not.toContain('proj-01'));
    expect(pressed()).toHaveLength(0);
  });
});

describe('Sub-agents with more types than the piece draws', () => {
  it('counts "other" from the response and leaves it unselectable', async () => {
    data.subagents.types = agentTypes(10);
    await mount('/sub-agents', 'sub-agents');
    expect(pieceText()).toContain('other · 2');
    const last = piece()?.querySelector('ul li:last-child');
    expect(last?.textContent).toContain('other · 2');
    expect(last?.querySelector('button, [aria-pressed]')).toBeNull();
    const before = panel('Selected type')?.textContent;
    const marks = pressed().length;
    await click(last ?? null);
    expect(panel('Selected type')?.textContent).toBe(before);
    expect(pressed()).toHaveLength(marks);
  });

  it('presses the default the panel describes when it is not the most-run type', async () => {
    const list = agentTypes(10);
    const first = list[0];
    const rich = list[9];
    if (first) first.tokens = 1000;
    if (rich) rich.tokens = 9_000_000;
    data.subagents.types = list;
    await mount('/sub-agents', 'sub-agents');
    await vi.waitFor(() => expect(pressed().length).toBeGreaterThan(0));
    const labels = pressed().map((el) => el.getAttribute('aria-label') ?? '');
    expect(labels.some((l) => l.includes('agent-1:'))).toBe(false);
    expect(panel('Selected type')?.textContent).toContain('agent-1');
    expect(labels.filter(Boolean)).toHaveLength(1);
    expect(labels[0]).toMatch(/^Rank \d: agent-\d,/);
    expect(panel('Selected type')?.textContent).toContain((labels[0] ?? '').replace(/^Rank \d: (agent-\d),.*$/, '$1'));
  });

  it('falls back to the default mark when the picked type leaves the response', async () => {
    data.subagents.types = agentTypes(3);
    await mount('/sub-agents', 'sub-agents');
    await click(piece()?.querySelector('[aria-label^="Rank 2"]') ?? null);
    expect(pressed()).toHaveLength(2);
    data.subagents.types = agentTypes(3).filter((t) => t.agentType !== 'agent-1');
    act(() => setRange('sub-agents', '7d'));
    await vi.waitFor(() => expect(pieceText()).not.toContain('agent-1'));
    expect(pressed().filter((el) => el.getAttribute('aria-label'))).toHaveLength(1);
    expect(pressed()[0]?.getAttribute('aria-label')).toContain('agent-0');
  });
});

describe('Fills with more tool families than the ring and the piece show', () => {
  it('counts the small tools from the response and leaves them unselectable', async () => {
    data.fills.tools = toolStats(8);
    await mount('/fills', 'fills');
    expect(pieceText()).toContain('Small tools');
    expect(pieceText()).toContain('2 tools');
    const tail = [...(piece()?.querySelectorAll('p') ?? [])].find((p) => p.textContent === 'Small tools')?.parentElement;
    expect(tail?.querySelector('button, [role], [aria-pressed]')).toBeNull();
    const before = panel('Selected')?.textContent;
    const marks = pressed().length;
    await click(tail ?? null);
    expect(panel('Selected')?.textContent).toBe(before);
    expect(pressed()).toHaveLength(marks);
  });

  it('presses the slice of the family the panel describes by default', async () => {
    data.fills.tools = toolStats(8);
    await mount('/fills', 'fills');
    await vi.waitFor(() => expect(pressed().length).toBeGreaterThan(0));
    const labels = pressed().map((el) => el.getAttribute('aria-label')).filter(Boolean);
    expect(labels).toEqual([expect.stringMatching(/^Rank 1: Read: whole/)]);
    expect(panel('Selected')?.getAttribute('aria-label')).toBe('Selected · Read');
  });

  it('shows the own figures of a picked family outside the ring and the piece', () => {
    data.fills.tools = toolStats(8);
    const view = fillsView(data.fills, 'Bash');
    const git = data.fills.tools[7];
    expect(view.selected?.name).toBe('Bash');
    expect(view.selected?.line).toContain(`${git?.results} results`);
    expect(view.pressedTool).toBeNull();
    expect(fillsView(data.fills, null).pressedTool).toBe('Read: whole');
  });
});

describe('Docker with more containers than the piece draws', () => {
  function crowd() {
    const now = data.docker.now;
    if (!now) throw new Error('fixture');
    now.containers = [...containers('big', 14), ...containers('mid', 5), ...containers('sml', 4), ...containers('tiny', 2)];
    now.stacks = ['big', 'mid', 'sml', 'tiny'].map((stack) => ({ stack, containers: 1, memBytes: 1, rxBps: 1, txBps: 1 }));
    now.counts = { ...now.counts, containers: 25, healthy: 25 };
  }

  it('counts "+N more" and "Other stacks" from the response and leaves them unselectable', async () => {
    crowd();
    await mount('/docker', 'docker');
    expect(pieceText()).toContain('+3 more');
    expect(pieceText()).toContain('Other stacks');
    expect(pieceText()).toContain('other · 2');
    const hexes = [...(piece()?.querySelectorAll('[role="img"][aria-label]') ?? [])];
    expect(hexes.some((h) => h.getAttribute('aria-label')?.startsWith('+3 more'))).toBe(true);
    expect(piece()?.querySelector('[aria-pressed], [role="button"]')).toBeNull();
    const search = router.state.location.search;
    await click(hexes.find((h) => h.getAttribute('aria-label')?.startsWith('+3 more')) ?? null);
    expect(router.state.location.search).toEqual(search);
    expect(piece()?.querySelector('[aria-pressed]')).toBeNull();
  });

  it('reads a container as needing a look in the piece exactly when the page counts it as a warning', async () => {
    const now = data.docker.now;
    if (!now) throw new Error('fixture');
    const [a, b, c, d] = containers('s', 4);
    if (!a || !b || !c || !d) throw new Error('fixture');
    now.containers = [
      { ...a, restarts: 2 },
      { ...b, health: 'none' },
      { ...c, state: '' as never },
      { ...d, health: 'unhealthy' },
    ];
    const view = dockerView(data.docker, Date.now());
    expect(view.piece.containers.map((x) => x.health)).toEqual(['unhealthy', 'none', 'unhealthy', 'unhealthy']);
    expect(view.containers.delta).toContain('3 warning');
    now.counts = { ...now.counts, containers: 4, healthy: 1 };
    await mount('/docker', 'docker');
    expect(pieceText()).toContain('3 need a look');
    expect(pageText()).toContain('3 warning');
  });
});

describe('Guard blocked count', () => {
  it('sums the hook-block errors only and splits them from other errors', () => {
    const base = data.guard.toolErrors[0];
    if (!base) throw new Error('fixture');
    data.guard.toolErrors = [
      { ...base, errorClass: 'blocked by hook', errors: 4 },
      { ...base, errorClass: 'not found', errors: 6 },
    ];
    const v = guardView(data.guard, new Date());
    expect(v.toolErrors).toEqual({ value: '10', delta: '4 blocked by guard · 6 other' });
  });
});

describe('Tab-level edge states', () => {
  it('Sessions: an empty response and a single session', async () => {
    data.sessions.sessions = [];
    data.sessions.projects = [];
    data.sessions.totals = { ...data.sessions.totals, sessions: 0, live: 0 };
    await mount('/sessions', 'sessions', { wait: false });
    noBrokenNumbers();
    expect(pageText()).toContain('No sessions in this period');
    act(() => root?.unmount());
    host?.remove();
    data.sessions.sessions = sessionList(1);
    data.sessions.projects = data.sessions.projects.slice(0, 1);
    data.sessions.totals = { ...data.sessions.totals, sessions: 1, live: 1 };
    await mount('/sessions', 'sessions');
    expect(piece()?.getAttribute('data-state')).toBe('single');
    expect(piece()?.querySelectorAll('ul button')).toHaveLength(1);
    noBrokenNumbers();
  });

  it('Sub-agents: an empty response and a single type', async () => {
    data.subagents.types = [];
    data.subagents.runs = { value: 0, previous: null };
    await mount('/sub-agents', 'sub-agents', { wait: false });
    noBrokenNumbers();
    act(() => root?.unmount());
    host?.remove();
    data.subagents.types = agentTypes(1);
    data.subagents.runs = { value: 100, previous: null };
    await mount('/sub-agents', 'sub-agents');
    expect(piece()?.getAttribute('data-state')).toBe('single');
    noBrokenNumbers();
  });

  it('Fills: an empty response and a single tool', async () => {
    data.fills.tools = [];
    data.fills.results = { value: 0, previous: null };
    data.fills.tokens = 0;
    data.fills.top = [];
    data.fills.byProject = [];
    await mount('/fills', 'fills', { wait: false });
    noBrokenNumbers();
    act(() => root?.unmount());
    host?.remove();
    data.fills.tools = toolStats(1);
    data.fills.results = { value: 100, previous: null };
    data.fills.tokens = 80_000;
    await mount('/fills', 'fills');
    expect(piece()?.getAttribute('data-state')).toBe('single');
    noBrokenNumbers();
  });

  it('Machine: no snapshot, no cores and no load average', async () => {
    if (data.machine.now) data.machine.now = { ...data.machine.now, cores: [], load: null as never };
    await mount('/machine', 'machine');
    noBrokenNumbers();
    act(() => root?.unmount());
    host?.remove();
    data.machine.now = null;
    await mount('/machine', 'machine', { wait: false });
    noBrokenNumbers();
  });

  it('Docker: not available, and an empty container list', async () => {
    data.docker.available = false;
    data.docker.now = null;
    await mount('/docker', 'docker', { wait: false });
    noBrokenNumbers();
    act(() => root?.unmount());
    host?.remove();
    data.docker.available = true;
    data.docker.now = fixtures(Date.now()).docker.now ?? null;
    if (data.docker.now) {
      data.docker.now.containers = [];
      data.docker.now.stacks = [];
      data.docker.now.counts = { ...data.docker.now.counts, containers: 0, running: 0, healthy: 0, noHealthcheck: 0 };
    }
    await mount('/docker', 'docker', { wait: false });
    noBrokenNumbers();
    expect(pageText()).toMatch(/No containers/);
  });

  it('Context: no context bands', async () => {
    data.context.contextBands = [];
    data.context.bands = [];
    await mount('/context', 'context', { wait: false });
    noBrokenNumbers();
  });
});

describe('Duplicate labels', () => {
  const keyErrors = (spy: ReturnType<typeof vi.spyOn>) =>
    spy.mock.calls.filter((c: unknown[]) => String(c[0]).includes('same key') || String(c[0]).includes('unique "key"'));

  it('give no React key error on Sessions', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const [one] = sessionList(1);
    if (!one) throw new Error('fixture');
    data.sessions.sessions = [one, { ...one }, { ...one }];
    data.sessions.projects = [...data.sessions.projects, { ...(data.sessions.projects[0] ?? { project: 'x', sessions: 1, tokens: 1, share: 0.1 }) }];
    await mount('/sessions', 'sessions');
    await refetch();
    expect(keyErrors(spy)).toEqual([]);
  });

  it('give no React key error on Fills', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const one = toolStats(1)[0];
    if (!one) throw new Error('fixture');
    data.fills.tools = [one, { ...one }, { ...one, tool: 'Grep' }, { ...one, tool: 'Grep' }, ...toolStats(8).slice(2), ...toolStats(8).slice(2)];
    await mount('/fills', 'fills');
    await refetch();
    expect(keyErrors(spy)).toEqual([]);
  });

  it('give no React key error on Docker, including a stack called "other"', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const now = data.docker.now;
    if (!now) throw new Error('fixture');
    const twin = containers('big', 1)[0];
    if (!twin) throw new Error('fixture');
    now.containers = [twin, { ...twin }, ...containers('other', 3), ...containers('mid', 2), ...containers('sml', 2), ...containers('tiny', 1)];
    now.stacks = ['big', 'other', 'mid', 'sml', 'tiny'].map((stack) => ({ stack, containers: 1, memBytes: 1, rxBps: 1, txBps: 1 }));
    await mount('/docker', 'docker');
    await refetch();
    expect(keyErrors(spy)).toEqual([]);
  });
});
