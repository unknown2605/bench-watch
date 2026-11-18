import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { describe, expect, it, vi } from 'vitest';
import { ListRow } from './ListRow';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

describe('list row bar', () => {
  it('slides for the same occupant and lands at once for a new one, in the same element', () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const root = createRoot(host);
    const bar = () => host.querySelector<HTMLElement>('[role="meter"] > span');
    const snaps: string[] = [];
    const real = window.getComputedStyle.bind(window);
    vi.spyOn(window, 'getComputedStyle').mockImplementation((el, pseudo) => {
      if (el === bar()) snaps.push(`${(el as HTMLElement).style.transition}|${(el as HTMLElement).style.width}`);
      return real(el, pseudo);
    });
    try {
      act(() => root.render(<ListRow name="web" meta="" value="10" fill={0.1} />));
      const first = bar();
      act(() => root.render(<ListRow name="web" meta="" value="20" fill={0.2} />));
      expect(snaps).toEqual([]);
      act(() => root.render(<ListRow name="db" meta="" value="90" fill={0.9} />));
      expect(snaps).toEqual(['none|90%']);
      expect(bar()).toBe(first);
      expect(bar()?.style.transition).toBe('');
    } finally {
      act(() => root.unmount());
      host.remove();
      vi.restoreAllMocks();
    }
  });
});
