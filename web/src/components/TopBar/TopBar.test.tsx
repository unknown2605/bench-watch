import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { NavTabs, TopBar } from './TopBar';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const tabs = [{ id: 'overview', label: 'Overview' }, { id: 'docker', label: 'Docker' }];
const rect = (left: number, right: number) => ({ left, right, top: 0, bottom: 0, width: right - left, height: 0 }) as DOMRect;

const scrollBy = vi.fn();

afterEach(() => {
  vi.restoreAllMocks();
  Reflect.deleteProperty(Element.prototype, 'scrollBy');
});

describe('TopBar', () => {
  it('scrolls the tab list so the selected tab is fully visible', async () => {
    const host = document.createElement('div');
    document.body.append(host);
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.getAttribute('role') === 'tablist') return rect(0, 768);
      return this.hasAttribute('data-selected') ? rect(738, 818) : rect(0, 0);
    });
    Object.defineProperty(Element.prototype, 'scrollBy', { configurable: true, value: scrollBy });
    const root = createRoot(host);
    act(() => root.render(<NavTabs activeKey="docker" onSelect={() => undefined}><TopBar tabs={tabs} updated="" health="" /></NavTabs>));
    await vi.waitFor(() => expect(scrollBy).toHaveBeenCalledWith({ left: 58 }));
    act(() => root.unmount());
    host.remove();
  });
});
