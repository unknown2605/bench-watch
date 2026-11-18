import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { describe, expect, it, vi } from 'vitest';
import { Panel } from './Panel';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

describe('Panel', () => {
  it('appends bodyClassName to the body element only', () => {
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
    const host = document.createElement('div');
    document.body.append(host);
    const root = createRoot(host);
    act(() => root.render(<Panel title="T" bodyClassName="extra"><i data-kid /></Panel>));
    const body = host.querySelector('[data-kid]')?.parentElement;
    expect(body?.classList.contains('extra')).toBe(true);
    expect(body?.className.split(' ').length).toBeGreaterThan(1);
    expect(host.querySelector('section')?.classList.contains('extra')).toBe(false);
    act(() => root.unmount());
    host.remove();
    vi.unstubAllGlobals();
  });

  it('gives the body exactly one class without bodyClassName', () => {
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
    const host = document.createElement('div');
    document.body.append(host);
    const root = createRoot(host);
    act(() => root.render(<Panel title="T"><i data-kid /></Panel>));
    const body = host.querySelector('[data-kid]')?.parentElement;
    expect(body?.className.split(' ')).toHaveLength(1);
    expect(body?.className).not.toMatch(/undefined|\s$/);
    act(() => root.unmount());
    host.remove();
    vi.unstubAllGlobals();
  });
});
