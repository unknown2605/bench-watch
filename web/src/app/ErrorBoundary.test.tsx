import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { describe, expect, it, vi } from 'vitest';
import { ErrorBoundary } from './ErrorBoundary';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

function Boom(): never {
  throw new Error('boom');
}

describe('ErrorBoundary', () => {
  it('replaces a crashed tree with an alert instead of a blank page', () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    HTMLCanvasElement.prototype.getContext = () => null;
    vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
    const host = document.createElement('div');
    document.body.append(host);
    const root = createRoot(host);
    act(() => root.render(<ErrorBoundary><Boom /></ErrorBoundary>));
    const mains = host.querySelectorAll('main');
    expect(mains).toHaveLength(1);
    expect(mains[0]?.hasAttribute('role')).toBe(false);
    expect(mains[0]?.querySelector('[role="alert"]')?.textContent).toContain('could not be shown');
    expect(host.querySelector('h1')?.textContent).toBe('Something went wrong');
    act(() => root.unmount());
    host.remove();
  });
});
