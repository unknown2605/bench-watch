import { describe, expect, it } from 'vitest';
import { stars } from '../mock/data';
import { layoutStars } from './StarMap';

describe('layoutStars', () => {
  it('keeps every planet clear of the others and inside the frame', () => {
    const placed = layoutStars(stars, 420, 260);
    for (const [i, a] of placed.entries()) {
      expect(a.x ?? 0).toBeGreaterThanOrEqual(a.r);
      expect(a.x ?? 0).toBeLessThanOrEqual(420 - a.r);
      for (const b of placed.slice(i + 1)) {
        const gap = Math.hypot((a.x ?? 0) - (b.x ?? 0), (a.y ?? 0) - (b.y ?? 0)) - a.r - b.r;
        expect(gap).toBeGreaterThan(0);
      }
    }
  });
  it('sizes planets from the largest value, and places none for no nodes', () => {
    expect(layoutStars([], 420, 260)).toEqual([]);
    const two = [{ name: 'a', value: 4, kind: 'idle' }, { name: 'b', value: 1, kind: 'idle' }] as const;
    const placed = layoutStars(two, 420, 260);
    expect(placed.map((n) => n.r)).toEqual([32, 23.5]);
    const zero = layoutStars([{ name: 'z', value: 0, kind: 'idle' }], 420, 260);
    expect(zero[0]?.r).toBe(15);
  });
  it('is deterministic', () => {
    expect(layoutStars(stars, 420, 260).map((n) => n.x)).toEqual(layoutStars(stars, 420, 260).map((n) => n.x));
  });
});
