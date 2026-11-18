import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters, setRange } from '../../app/filters';
import { createAppRouter } from '../../app/router';
import { projectLabel } from '../../format/labels';
import { fixtures } from './testData';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

type Fixtures = ReturnType<typeof fixtures>;
let data: Fixtures;
let root: Root | null = null;
let host: HTMLElement | null = null;
let client: QueryClient;
let router: ReturnType<typeof createAppRouter>;

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
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(path: string) {
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
  await vi.waitFor(() => expect(piece()).not.toBeNull());
}

const shown = (el: Element) => !el.closest('[aria-hidden="true"]');
const piece = () => [...(host?.querySelectorAll('[data-piece]') ?? [])].find(shown) ?? null;
const mark = (label: string) => piece()?.querySelector<HTMLElement>(`[aria-label^="${label}"]`) ?? null;
const rows = () => [...(piece()?.querySelectorAll<HTMLElement>('li button[aria-pressed], div[role="button"]') ?? [])];
const row = (text: string) => rows().find((el) => el.textContent?.includes(text)) ?? null;
const pressed = () => [...(piece()?.querySelectorAll('[aria-pressed="true"]') ?? [])];
const panel = (title: string) => host?.querySelector(`section[aria-label^="${title}"]`) ?? null;

async function click(el: Element | null) {
  await act(async () => el?.dispatchEvent(new MouseEvent('click', { bubbles: true })));
}

async function press(el: Element | null, key: string) {
  await act(async () => el?.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })));
}

async function refetch() {
  await act(async () => {
    await client.invalidateQueries();
  });
}

describe('Sessions piece selection', () => {
  const ended = () => projectLabel(data.sessions.sessions[1]?.project ?? '');

  it('selects a session from a dot and links it in the URL and the side panel', async () => {
    await mount('/sessions');
    await vi.waitFor(() => expect(mark('Rank 1')?.getAttribute('aria-pressed')).toBe('true'));
    const dot = mark('Rank 2');
    expect(dot?.tagName).toBe('BUTTON');
    expect(dot?.getAttribute('aria-pressed')).toBe('false');
    await click(dot);
    await vi.waitFor(() => expect(router.state.location.search).toMatchObject({ session: 'deadbeef' }));
    await vi.waitFor(() => expect(dot?.getAttribute('aria-pressed')).toBe('true'));
    expect(mark('Rank 1')?.getAttribute('aria-pressed')).toBe('false');
    expect(row('02')?.getAttribute('aria-pressed')).toBe('true');
    await vi.waitFor(() => expect(panel('Selected · session')?.textContent).toContain(ended()));
  });

  it('selects from the side list and keeps the selection and nodes on a live answer', async () => {
    await mount('/sessions');
    const item = row('02');
    await click(item);
    await vi.waitFor(() => expect(item?.getAttribute('aria-pressed')).toBe('true'));
    const dot = mark('Rank 2');
    const first = data.sessions.sessions[1];
    if (first) first.tokens = 350_000;
    await refetch();
    await vi.waitFor(() => expect(mark('Rank 2')?.getAttribute('aria-label')).toContain('350k'));
    expect(mark('Rank 2')).toBe(dot);
    expect(row('02')).toBe(item);
    expect(dot?.getAttribute('aria-pressed')).toBe('true');
  });
});

describe('Sub-agents piece selection', () => {
  it('selects a type from a spoke by click and from a row by click', async () => {
    await mount('/sub-agents');
    await vi.waitFor(() => expect(mark('Rank 1')?.getAttribute('aria-pressed')).toBe('true'));
    const spoke = mark('Rank 2');
    expect(spoke?.getAttribute('role')).toBe('button');
    expect(spoke?.getAttribute('tabindex')).toBe('0');
    await click(spoke);
    expect(spoke?.getAttribute('aria-pressed')).toBe('true');
    await vi.waitFor(() => expect(panel('Selected type')?.textContent).toContain('unknown'));
    expect(host?.querySelector('table button[aria-pressed="true"]')?.textContent).toBe('unknown');
    await click(row('code-reviewer'));
    expect(mark('Rank 1')?.getAttribute('aria-pressed')).toBe('true');
    expect(pressed()).toHaveLength(2);
  });

  it('keeps the picked type and its nodes on a live answer', async () => {
    await mount('/sub-agents');
    const spoke = mark('Rank 2');
    await press(spoke, 'Enter');
    const second = data.subagents.types[1];
    if (second) second.runs = 12;
    await refetch();
    await vi.waitFor(() => expect(mark('Rank 2')?.getAttribute('aria-label')).toContain('12 runs'));
    expect(mark('Rank 2')).toBe(spoke);
    expect(spoke?.getAttribute('aria-pressed')).toBe('true');
  });
});

describe('Fills piece selection', () => {
  it('picks one slice, shows its family, and clears on a second press', async () => {
    await mount('/fills');
    await vi.waitFor(() => expect(mark('Rank 3: Read: ranged')).not.toBeNull());
    expect(pressed()).toHaveLength(2);
    expect(mark('Rank 1: Read: whole')?.getAttribute('aria-pressed')).toBe('true');
    expect(panel('Selected')?.getAttribute('aria-label')).toBe('Selected · Read');
    const slice = mark('Rank 3: Read: ranged');
    await press(slice, 'Enter');
    expect(slice?.getAttribute('aria-pressed')).toBe('true');
    expect(mark('Rank 1')?.getAttribute('aria-pressed')).toBe('false');
    expect(pressed()).toHaveLength(2);
    expect(row('Read: ranged')?.getAttribute('aria-pressed')).toBe('true');
    expect(panel('By project')?.getAttribute('aria-label')).toBe('By project · Read');
    await press(row('Read: ranged'), ' ');
    expect(pressed()).toHaveLength(2);
    expect(mark('Rank 1: Read: whole')?.getAttribute('aria-pressed')).toBe('true');
    expect(panel('By project')?.getAttribute('aria-label')).toBe('By project · from tools');
  });

  it('keeps the slice on a live answer and drops it when a new range no longer has it', async () => {
    await mount('/fills');
    await vi.waitFor(() => expect(mark('Rank 2: Grep')).not.toBeNull());
    const slice = mark('Rank 2: Grep');
    await click(slice);
    await vi.waitFor(() => expect(panel('Selected')?.getAttribute('aria-label')).toBe('Selected · Grep'));
    await refetch();
    expect(mark('Rank 2: Grep')).toBe(slice);
    expect(slice?.getAttribute('aria-pressed')).toBe('true');
    data.fills.tools = data.fills.tools.filter((t) => t.tool !== 'Grep');
    act(() => setRange('fills', '30d'));
    await vi.waitFor(() => expect(panel('Selected')?.getAttribute('aria-label')).toBe('Selected · Read'));
    expect(mark('Rank 1: Read: whole')?.getAttribute('aria-pressed')).toBe('true');
  });
});
