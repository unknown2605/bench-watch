import { act, useRef } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useElementSize } from './useElementSize';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root | null = null;
let host: HTMLElement | null = null;

function Probe() {
  const ref = useRef<HTMLDivElement>(null);
  const [w, h] = useElementSize(ref);
  return <div ref={ref}>{`${w}x${h}`}</div>;
}

function mount(): string {
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  act(() => root?.render(<Probe />));
  return host.textContent ?? '';
}

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', { configurable: true, get: () => 300 });
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', { configurable: true, get: () => 200 });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  Reflect.deleteProperty(HTMLElement.prototype, 'offsetWidth');
  Reflect.deleteProperty(HTMLElement.prototype, 'offsetHeight');
  Reflect.deleteProperty(Element.prototype, 'currentCSSZoom');
});

describe('useElementSize', () => {
  it('reports the layout box when the page is not zoomed', () => {
    expect(mount()).toBe('300x200');
  });

  it('divides the on-screen box by the page zoom', () => {
    Object.defineProperty(Element.prototype, 'currentCSSZoom', { configurable: true, get: () => 1.6 });
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue({ width: 800, height: 480 } as DOMRect);
    expect(mount()).toBe('500x300');
  });
});
