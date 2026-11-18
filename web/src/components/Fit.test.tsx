import { act, type ReactNode } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Fit } from './Fit';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root | null = null;
let host: HTMLElement | null = null;

function mount(node: ReactNode): HTMLElement {
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  act(() => root?.render(node));
  return host;
}

beforeEach(() => vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} }));

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(HTMLElement.prototype, 'offsetWidth');
});

describe('Fit', () => {
  it('uses the fallback size when no layout exists', () => {
    expect(mount(<Fit fallback={[300, 200]}>{(w, h) => <p>{`${w}x${h}`}</p>}</Fit>).textContent).toBe('300x200');
  });

  it('keeps a fixed height and takes its width from the box', () => {
    Object.defineProperty(HTMLElement.prototype, 'offsetWidth', { configurable: true, get: () => 420 });
    expect(mount(<Fit fallback={[300, 200]} height={70}>{(w, h) => <p>{`${w}x${h}`}</p>}</Fit>).textContent).toBe('420x70');
  });
});
