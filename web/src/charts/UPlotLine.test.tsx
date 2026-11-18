import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

const fakes = vi.hoisted(() => [] as { setData: ReturnType<typeof vi.fn>; setSize: ReturnType<typeof vi.fn>; destroy: ReturnType<typeof vi.fn> }[]);

vi.mock('uplot', () => ({
  default: class {
    setData = vi.fn();
    setSize = vi.fn();
    destroy = vi.fn();
    constructor() {
      fakes.push(this);
    }
  },
}));

import { UPlotLine } from './UPlotLine';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

beforeAll(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', { configurable: true, get: () => 400 });
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', { configurable: true, get: () => 160 });
});

afterEach(() => {
  fakes.length = 0;
});

describe('UPlotLine', () => {
  it('updates with setData and rebuilds only when the look changes', () => {
    const host = document.createElement('div');
    document.body.append(host);
    const root = createRoot(host);
    const a: [number[], number[]] = [[1, 2, 3], [4, 5, 6]];
    const b: [number[], number[]] = [[1, 2, 3, 4], [4, 5, 6, 7]];
    act(() => root.render(<UPlotLine data={a} label="Load" />));
    expect(fakes).toHaveLength(1);
    act(() => root.render(<UPlotLine data={b} label="Load" />));
    expect(fakes).toHaveLength(1);
    expect(fakes[0]?.setData).toHaveBeenLastCalledWith(b);
    expect(fakes[0]?.destroy).not.toHaveBeenCalled();
    act(() => root.render(<UPlotLine data={b} tone="warn" label="Load" />));
    expect(fakes).toHaveLength(2);
    expect(fakes[0]?.destroy).toHaveBeenCalledTimes(1);
    act(() => root.unmount());
    expect(fakes[1]?.destroy).toHaveBeenCalledTimes(1);
    host.remove();
  });

  it('describes the series to screen readers with a table', () => {
    const host = document.createElement('div');
    document.body.append(host);
    const root = createRoot(host);
    act(() => root.render(<UPlotLine data={[[1, 2, 3], [4, 9, 6]]} label="Load" />));
    const img = host.querySelector('[role="img"]');
    const table = document.getElementById(img?.getAttribute('aria-describedby') ?? '');
    expect(table?.textContent).toContain('Maximum9');
    act(() => root.unmount());
    host.remove();
  });

  it('summarises a series too long for Math.min spread', () => {
    const host = document.createElement('div');
    document.body.append(host);
    const root = createRoot(host);
    const n = 300_000;
    const xs = Array.from({ length: n }, (_, i) => i);
    const ys = xs.map((i) => (i === 7 ? -5 : i === 9 ? 1e6 : i % 100));
    act(() => root.render(<UPlotLine data={[xs, ys]} label="Load" />));
    const img = host.querySelector('[role="img"]');
    const text = document.getElementById(img?.getAttribute('aria-describedby') ?? '')?.textContent;
    expect(text).toContain('Minimum-5');
    expect(text).toContain('Maximum1000000');
    act(() => root.unmount());
    host.remove();
  });
});
