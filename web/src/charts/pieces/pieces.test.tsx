import { act, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  ContextPiece, CrossFade, DockerPiece, FillsPiece, GuardPiece, MachinePiece, OverviewPiece, SessionsPiece, SubagentsPiece, TokensPiece,
} from '.';

let root: Root | null = null;
let host: HTMLElement;

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  vi.unstubAllGlobals();
});

function draw(node: ReactNode) {
  act(() => root?.render(node));
  return host.querySelector('[data-piece]');
}

const range = { rangeLabel: 'today' };
const day = (d: number) => `2026-11-${String(d).padStart(2, '0')}`;

describe('centre pieces render each edge state', () => {
  it('overview', () => {
    const base = { tokens: null, previous: null, days: 1, mainShare: null, reread: null, sub: null, over: null, trend: [], today: day(11), ...range };
    expect(draw(<OverviewPiece data={base} />)?.getAttribute('data-state')).toBe('empty');
    const month = Array.from({ length: 20 }, (_, i) => ({ day: day(i + 1), tokens: 100 }));
    expect(draw(<OverviewPiece data={{ ...base, tokens: 5, trend: month }} />)?.getAttribute('data-state')).toBe('capped');
  });

  it('tokens', () => {
    const base = { kinds: [], tokens: null, costUsd: null, previousCost: null, days: 1, ...range };
    expect(draw(<TokensPiece data={base} />)?.getAttribute('data-state')).toBe('empty');
    const kinds = [{ kind: 'cacheRead', tokens: 95, costUsd: 1 }, { kind: 'input', tokens: 5, costUsd: 1 }];
    const el = draw(<TokensPiece data={{ ...base, kinds, tokens: 100, costUsd: 2 }} />);
    expect(el?.getAttribute('data-state')).toBe('skewed');
    expect(el?.textContent).toContain('input');
  });

  it('context', () => {
    const defs = [{ minTokens: 0, maxTokens: 200_000 }, { minTokens: 200_000, maxTokens: null }];
    const base = { defs, bands: [], avgPerCall: null, compactions: 0, ...range };
    expect(draw(<ContextPiece data={base} />)?.getAttribute('data-state')).toBe('empty');
    const bands = [{ minTokens: 0, calls: 3, contextTokens: 10, cacheReadTokens: 9 }];
    expect(draw(<ContextPiece data={{ ...base, bands }} />)?.getAttribute('data-state')).toBe('single');
  });

  it('sessions', () => {
    const base = { sessions: [], projects: [], total: 0, live: 0 };
    expect(draw(<SessionsPiece data={base} />)?.getAttribute('data-state')).toBe('empty');
    const sessions = Array.from({ length: 12 }, (_, i) => ({ key: `s${i}`, project: 'p', tokens: 100 + i, state: 'ended' as const }));
    const el = draw(<SessionsPiece data={{ ...base, sessions, total: 40, live: 0 }} />);
    expect(el?.getAttribute('data-state')).toBe('capped');
    expect(el?.querySelectorAll('button')).toHaveLength(12);
    expect(el?.textContent).toContain('+28 more');
  });

  it('sub-agents', () => {
    expect(draw(<SubagentsPiece data={{ types: [], mainShare: null, ...range }} />)?.getAttribute('data-state')).toBe('empty');
    const types = Array.from({ length: 10 }, (_, i) => ({ name: `t${i}`, runs: 10 }));
    const el = draw(<SubagentsPiece data={{ types, mainShare: 0.5, ...range }} />);
    expect(el?.getAttribute('data-state')).toBe('capped');
    expect(el?.textContent).toContain('other');
  });

  it('fills', () => {
    const base = { tools: [], contextShare: null, resultTokens: null, ...range };
    expect(draw(<FillsPiece data={base} />)?.getAttribute('data-state')).toBe('empty');
    const tools = [{ tool: 'Read', tokens: 10, weightedTokens: 10, results: 1 }];
    expect(draw(<FillsPiece data={{ ...base, tools }} />)?.getAttribute('data-state')).toBe('single');
  });

  it('guard', () => {
    const base = {
      blocked: null,
      hours: null,
      bash: null,
      file: null,
      subShare: null,
      lastBlock: null,
      active: null,
      version: null,
      mode: null,
    };
    expect(draw(<GuardPiece data={base} />)?.getAttribute('data-state')).toBe('empty');
    const hours = [{ hour: 11, calls: 4, blocked: 0 }];
    expect(draw(<GuardPiece data={{ ...base, hours }} />)?.getAttribute('data-state')).toBe('single');
  });

  it('guard verdict reads installed, not found or unknown', () => {
    const base = {
      blocked: null, hours: null, bash: null, file: null, subShare: null, lastBlock: null, version: null, mode: null,
    };
    const verdict = (active: boolean | null) => {
      draw(<GuardPiece data={{ ...base, active }} />);
      return host.textContent;
    };
    expect(verdict(true)).toContain('Guard installed');
    expect(verdict(false)).toContain('No guard found');
    const unknown = verdict(null);
    expect(unknown).toContain('Guard state unknown');
    expect(unknown).not.toContain('No guard found');
  });

  it('guard announces hours only when it has hourly data', () => {
    const base = {
      blocked: null, bash: null, file: null, subShare: null, lastBlock: null, active: null, version: null, mode: null,
    };
    const labelled = () => host.querySelectorAll('[aria-label$="blocked"]').length;
    draw(<GuardPiece data={{ ...base, hours: null }} />);
    expect(labelled()).toBe(0);
    expect(host.querySelectorAll('[tabindex="0"]')).toHaveLength(0);
    draw(<GuardPiece data={{ ...base, hours: [{ hour: 11, calls: 4, blocked: 1 }] }} />);
    expect(labelled()).toBe(24);
    expect(host.querySelector('[aria-label="11:00: 4 calls, 1 blocked"]')).not.toBeNull();
  });

  it('guard hour segments are not under an aria-hidden ancestor', () => {
    const base = {
      blocked: null, bash: null, file: null, subShare: null, lastBlock: null, active: null, version: null, mode: null,
    };
    draw(<GuardPiece data={{ ...base, hours: [{ hour: 11, calls: 4, blocked: 1 }] }} />);
    const segs = host.querySelectorAll('[tabindex]');
    expect(segs).toHaveLength(24);
    segs.forEach((s) => expect(s.closest('[aria-hidden="true"]')).toBeNull());
    const group = host.querySelector('svg[role="group"]');
    expect(group?.getAttribute('aria-label')).toBeTruthy();
    expect(group?.contains(segs[0] ?? null)).toBe(true);
    draw(<GuardPiece data={{ ...base, hours: null }} />);
    expect(host.querySelectorAll('[tabindex]')).toHaveLength(0);
    expect(host.querySelector('svg[role="group"]')).toBeNull();
    expect(host.querySelectorAll('svg[aria-hidden="true"]')).toHaveLength(2);
  });

  it('machine', () => {
    expect(draw(<MachinePiece data={{ cores: [], cpuBusyPct: null, load: null }} />)?.getAttribute('data-state')).toBe('empty');
    const cores = Array.from({ length: 20 }, (_, i) => ({ core: i, kind: 'performance' as const, busyPct: 30 }));
    expect(draw(<MachinePiece data={{ cores, cpuBusyPct: 30, load: null }} />)?.getAttribute('data-state')).toBe('capped');
  });

  it('docker', () => {
    expect(draw(<DockerPiece data={{ containers: [], stacks: [] }} />)?.getAttribute('data-state')).toBe('empty');
    const containers = [{ name: 'db', stack: 'infra', memBytes: 9e8, health: 'healthy', restarts: 0 }];
    expect(draw(<DockerPiece data={{ containers, stacks: [] }} />)?.getAttribute('data-state')).toBe('single');
  });
});

describe('CrossFade', () => {
  it('keeps the old piece fading out until its animation ends', () => {
    act(() => root?.render(<CrossFade id="a"><p>one</p></CrossFade>));
    act(() => root?.render(<CrossFade id="b"><p>two</p></CrossFade>));
    expect(host.textContent).toBe('onetwo');
    const leaving = host.querySelector('[aria-hidden="true"]');
    // jsdom has no AnimationEvent, so React listens for the webkit-prefixed name there.
    act(() => {
      for (const type of ['animationend', 'webkitAnimationEnd']) leaving?.dispatchEvent(new Event(type, { bubbles: true }));
    });
    expect(host.textContent).toBe('two');
  });
});

describe('piece selection semantics', () => {
  const types = [{ name: 'a', runs: 9, key: 'a' }, { name: 'b', runs: 5, key: 'b' }];
  const sessions = [{ key: 'p/1', project: 'p', tokens: 9, state: 'live' as const }, { key: 'p/2', project: 'p', tokens: 5, state: 'ended' as const }];
  const key = (type: string, init: KeyboardEventInit) => new KeyboardEvent(type, { bubbles: true, cancelable: true, ...init });

  it('ignores a held Enter or Space and selects once per press', () => {
    const onSelect = vi.fn();
    draw(<SubagentsPiece data={{ types, mainShare: null, ...range }} onSelect={onSelect} />);
    const spoke = host.querySelector<SVGElement>('[aria-label^="Rank 1"]');
    act(() => {
      spoke?.dispatchEvent(key('keydown', { key: 'Enter' }));
      spoke?.dispatchEvent(key('keydown', { key: 'Enter', repeat: true }));
      spoke?.dispatchEvent(key('keydown', { key: ' ', repeat: true }));
    });
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith('a');
  });

  it('keeps list rows as list items and puts the pressed state on a button inside', () => {
    const el = draw(<SubagentsPiece data={{ types, mainShare: null, ...range }} selected="b" onSelect={() => undefined} />);
    const items = [...(el?.querySelectorAll('ul > li') ?? [])];
    expect(items).toHaveLength(2);
    for (const li of items) {
      expect(li.getAttribute('role')).toBeNull();
      expect(li.getAttribute('aria-pressed')).toBeNull();
    }
    expect(items.map((li) => li.querySelector('button')?.getAttribute('aria-pressed'))).toEqual(['false', 'true']);
    const el2 = draw(<SessionsPiece data={{ sessions, projects: [], total: 2, live: 1 }} selected="p/1" onSelect={() => undefined} />);
    const rows = [...(el2?.querySelectorAll('ul > li button') ?? [])];
    expect(rows.map((b) => b.getAttribute('aria-pressed'))).toEqual(['true', 'false']);
  });

  it('renders the gallery markup, with no buttons or roles in the rows, without onSelect', () => {
    const el = draw(<SubagentsPiece data={{ types, mainShare: null, ...range }} />);
    expect(el?.querySelectorAll('ul button')).toHaveLength(0);
    expect(el?.querySelectorAll('ul [role], ul [aria-pressed]')).toHaveLength(0);
    const el2 = draw(<SessionsPiece data={{ sessions, projects: [], total: 2, live: 1 }} />);
    expect(el2?.querySelectorAll('ul button, ul [role], ul [aria-pressed]')).toHaveLength(0);
  });
});
