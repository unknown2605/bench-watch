import { act, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AnimatedNumber } from './AnimatedNumber';
import { formatLike, parseNumber } from './number';
import { useReducedMotion } from './useReducedMotion';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root | null = null;
let host: HTMLElement | null = null;
const frames: FrameRequestCallback[] = [];

function mount(node: ReactNode) {
  host ??= document.body.appendChild(document.createElement('div'));
  root ??= createRoot(host);
  act(() => root?.render(node));
  return host;
}

function runFrames(at: number) {
  act(() => {
    for (const cb of frames.splice(0)) cb(at);
  });
}

function stubMotion(reduce: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: reduce && query.includes('reduce'),
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }));
}

beforeEach(() => {
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => frames.push(cb));
  vi.stubGlobal('cancelAnimationFrame', () => undefined);
  frames.length = 0;
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.unstubAllGlobals();
});

describe('number helpers', () => {
  it('splits a formatted figure and puts a value back in the same shape', () => {
    const p = parseNumber('$1,284.50 est.');
    expect(p).toMatchObject({ pre: '$', post: ' est.', value: 1284.5, decimals: 2, grouped: true });
    expect(p && formatLike(999.126, p)).toBe('999.13');
    expect(p && formatLike(12345.6, p)).toBe('12,345.60');
  });

  it('returns null for text without a number', () => {
    expect(parseNumber('no data')).toBeNull();
  });
});

describe('AnimatedNumber', () => {
  it('shows the final text at once on the first render', () => {
    stubMotion(false);
    expect(mount(<AnimatedNumber text="$7,864" />).textContent).toBe('$7,864');
  });

  it('counts through values and ends on the exact new text when the figure changes', () => {
    stubMotion(false);
    mount(<AnimatedNumber text="100" />);
    mount(<AnimatedNumber text="200" />);
    runFrames(1000);
    runFrames(1300);
    const mid = Number(host?.textContent);
    expect(mid).toBeGreaterThan(100);
    expect(mid).toBeLessThan(200);
    runFrames(5000);
    runFrames(5001);
    expect(host?.textContent).toBe('200');
  });

  it('continues from the shown value, not from zero, when a new value arrives mid-count', () => {
    stubMotion(false);
    mount(<AnimatedNumber text="100" />);
    mount(<AnimatedNumber text="200" />);
    runFrames(1000);
    runFrames(1300);
    const mid = Number(host?.textContent);
    mount(<AnimatedNumber text="300" />);
    runFrames(2000);
    runFrames(2100);
    expect(Number(host?.textContent)).toBeGreaterThanOrEqual(mid);
    runFrames(9000);
    runFrames(9001);
    expect(host?.textContent).toBe('300');
  });

  it('jumps without counting when the unit changes', () => {
    stubMotion(false);
    mount(<AnimatedNumber text="12k" />);
    mount(<AnimatedNumber text="1.2M" />);
    expect(host?.textContent).toBe('1.2M');
  });

  it('shows the new text at once under reduced motion', () => {
    stubMotion(true);
    mount(<AnimatedNumber text="100" />);
    mount(<AnimatedNumber text="200" />);
    expect(host?.textContent).toBe('200');
    expect(frames.length).toBeLessThanOrEqual(1);
  });

  it('does not start a loop while the page is hidden', () => {
    stubMotion(false);
    mount(<AnimatedNumber text="100" />);
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
    try {
      mount(<AnimatedNumber text="300" />);
      runFrames(1000);
      expect(host?.textContent).toBe('300');
      expect(frames).toHaveLength(0);
    } finally {
      Reflect.deleteProperty(document, 'hidden');
    }
  });
});

describe('useReducedMotion', () => {
  function Probe() {
    return <p>{String(useReducedMotion())}</p>;
  }

  it('follows the media query', () => {
    stubMotion(true);
    expect(mount(<Probe />).textContent).toBe('true');
    act(() => root?.unmount());
    root = null;
    host?.remove();
    host = null;
    stubMotion(false);
    expect(mount(<Probe />).textContent).toBe('false');
  });

  it('is false where matchMedia is missing', () => {
    vi.stubGlobal('matchMedia', undefined);
    expect(mount(<Probe />).textContent).toBe('false');
  });
});
