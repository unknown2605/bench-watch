import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useRef } from 'react';
import { useFrameTween } from './useFrameTween';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root | null = null;
let host: HTMLElement | null = null;
const frames: FrameRequestCallback[] = [];
const rendered = vi.fn();
function Probe({ values }: { values: readonly number[] }) {
  const ref = useRef<HTMLSpanElement>(null);
  rendered();
  useFrameTween(values, (v) => {
    if (ref.current) ref.current.textContent = v.join(' ');
  });
  return <span ref={ref}>{values.join(' ')}</span>;
}

const shown = () => (host?.textContent ?? '').split(' ').filter(Boolean).map(Number);

function mount(values: readonly number[]) {
  host ??= document.body.appendChild(document.createElement('div'));
  root ??= createRoot(host);
  act(() => root?.render(<Probe values={values} />));
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

describe('useFrameTween', () => {
  it('moves each value from the old to the new one and ends on the exact targets', () => {
    stubMotion(false);
    mount([0.1, 0.9]);
    expect(shown()).toEqual([0.1, 0.9]);
    mount([0.3, 0.2]);
    expect(shown()).toEqual([0.1, 0.9]);
    runFrames(1000);
    runFrames(1300);
    const [a = 0, b = 0] = shown();
    expect(a).toBeGreaterThan(0.1);
    expect(a).toBeLessThan(0.3);
    expect(b).toBeLessThan(0.9);
    expect(b).toBeGreaterThan(0.2);
    runFrames(5000);
    expect(shown()).toEqual([0.3, 0.2]);
  });

  it('paints the frames without rendering the component again', () => {
    stubMotion(false);
    mount([0.1, 0.9]);
    mount([0.3, 0.2]);
    const before = rendered.mock.calls.length;
    runFrames(1000);
    runFrames(1200);
    runFrames(1400);
    runFrames(5000);
    expect(rendered.mock.calls.length).toBe(before);
    expect(shown()).toEqual([0.3, 0.2]);
  });

  it('keeps the frame in progress when the component renders again with the same targets', () => {
    stubMotion(false);
    mount([0.1]);
    mount([0.5]);
    runFrames(1000);
    runFrames(1300);
    const [mid = 0] = shown();
    mount([0.5]);
    expect(shown()).toEqual([mid]);
    expect(mid).toBeGreaterThan(0.1);
    expect(mid).toBeLessThan(0.5);
  });

  it('shows the new values at once under reduced motion', () => {
    stubMotion(true);
    mount([0.1, 0.9]);
    mount([1 / 3, 0.2]);
    expect(shown()).toEqual([1 / 3, 0.2]);
  });

  it('jumps when the number of values changes', () => {
    stubMotion(false);
    mount([0.1]);
    mount([0.4, 0.5]);
    expect(shown()).toEqual([0.4, 0.5]);
  });
});
