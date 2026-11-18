import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetFilters, setMetricsRange } from '../../app/filters';
import { keyed } from '../../app/keys';
import { KPI, ListRow, Panel } from '../../components';
import { INTRO_MS } from '../../motion/intro';
import { TabFrame } from './TabFrame';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

type Row = { name: string; value: number };

let root: Root | null = null;
let host: HTMLElement | null = null;

function frame(rows: readonly Row[], placeholder = false) {
  const slots = {
    left: (
      <>
        <KPI label="CPU" value={`${rows[0]?.value ?? 0}%`} />
        <Panel title="Network">
          {keyed(rows, (r) => r.name).map(([key, r]) => (
            <ListRow key={key} name={r.name} meta="" value={String(r.value)} fill={r.value / 100} />
          ))}
        </Panel>
      </>
    ),
    centre: null,
    bottom: null,
    right: null,
  };
  return <TabFrame tab="machine" title="Machine" period={null} filter={<div />} slots={slots} error={null} placeholder={placeholder} />;
}

function render(rows: readonly Row[], placeholder = false) {
  host ??= document.body.appendChild(document.createElement('div'));
  root ??= createRoot(host);
  act(() => root?.render(frame(rows, placeholder)));
  return host;
}

const main = () => host?.querySelector('main') ?? null;
const rowOf = (name: string) => host?.querySelector(`[title="${name}"]`)?.parentElement ?? null;

beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
  resetFilters();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  resetFilters();
});

describe('live updates', () => {
  it('play the load motion once, then keep every element in place when the rows reorder', () => {
    render([{ name: 'web', value: 10 }, { name: 'db', value: 5 }]);
    expect(main()?.hasAttribute('data-intro')).toBe(true);
    act(() => vi.advanceTimersByTime(INTRO_MS));
    expect(main()?.hasAttribute('data-intro')).toBe(false);
    const web = rowOf('web');
    const db = rowOf('db');
    const panel = host?.querySelector('section[aria-label="Network"]');
    for (let tick = 1; tick <= 5; tick += 1) {
      render([{ name: 'db', value: 50 + tick }, { name: 'web', value: 10 + tick }]);
      expect(main()?.hasAttribute('data-intro')).toBe(false);
    }
    expect(rowOf('web')).toBe(web);
    expect(rowOf('db')).toBe(db);
    expect(host?.querySelector('section[aria-label="Network"]')).toBe(panel);
    expect(host?.querySelector('[title="db"]')?.parentElement?.nextElementSibling).toBe(web);
  });

  it('never mark a panel or a figure when what they show changes', () => {
    render([{ name: 'web', value: 10 }]);
    act(() => vi.advanceTimersByTime(INTRO_MS * 2));
    const mark = (el: Element) => [el.tagName, el.getAttribute('class'), el.getAttributeNames().join()];
    const marks = () => Array.from(main()?.querySelectorAll('*') ?? [], mark);
    const before = marks();
    const observer = new MutationObserver(() => undefined);
    observer.observe(main() as Node, { attributes: true, subtree: true, attributeFilter: ['class', 'data-intro'] });
    render([{ name: 'web', value: 90 }]);
    act(() => vi.advanceTimersByTime(INTRO_MS * 2));
    const changed = observer.takeRecords();
    observer.disconnect();
    expect(changed).toEqual([]);
    expect(marks()).toEqual(before);
    expect(main()?.hasAttribute('data-intro')).toBe(false);
  });

  it('restart the load motion when the range changes while the previous one still plays', () => {
    render([{ name: 'web', value: 10 }]);
    act(() => vi.advanceTimersByTime(INTRO_MS / 4));
    expect(main()?.hasAttribute('data-intro')).toBe(true);
    const observer = new MutationObserver(() => undefined);
    observer.observe(main() as Node, { attributes: true, attributeFilter: ['data-intro'], attributeOldValue: true });
    act(() => {
      setMetricsRange('machine', '1h');
      root?.render(frame([{ name: 'web', value: 10 }], true));
    });
    render([{ name: 'web', value: 20 }]);
    const toggles = observer.takeRecords();
    observer.disconnect();
    expect(toggles.map((r) => r.oldValue)).toEqual(['', null]);
    expect(main()?.getAttribute('data-intro')).toBe('');
    act(() => vi.advanceTimersByTime(INTRO_MS - 1));
    expect(main()?.hasAttribute('data-intro')).toBe(true);
  });

  it('play the load motion again when the range changes, once the new answer is shown', () => {
    render([{ name: 'web', value: 10 }]);
    act(() => vi.advanceTimersByTime(INTRO_MS));
    act(() => {
      setMetricsRange('machine', '1h');
      root?.render(frame([{ name: 'web', value: 10 }], true));
    });
    expect(main()?.hasAttribute('data-intro')).toBe(false);
    render([{ name: 'web', value: 20 }]);
    expect(main()?.hasAttribute('data-intro')).toBe(true);
    act(() => vi.advanceTimersByTime(INTRO_MS));
    render([{ name: 'web', value: 30 }]);
    expect(main()?.hasAttribute('data-intro')).toBe(false);
  });
});
