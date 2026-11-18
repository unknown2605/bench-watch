import { describe, expect, it } from 'vitest';
import { makeStars } from '../app/Starfield';
import { arcLine } from './Reveal';

describe('arcLine', () => {
  it('starts at the top and ends a quarter turn clockwise at the right', () => {
    expect(arcLine(10, 0, Math.PI / 2)).toBe('M 0.00 -10.00 A 10 10 0 0 1 10.00 -0.00');
  });

  it('closes a full turn with two half arcs', () => {
    expect(arcLine(10, 0, Math.PI * 2).match(/A /g)).toHaveLength(2);
  });
});

describe('makeStars', () => {
  it('places the same stars for the same size and keeps them above the horizon', () => {
    const a = makeStars(800, 600);
    expect(a).toEqual(makeStars(800, 600));
    expect(a.every((s) => s.y <= 600 * 0.62 && s.x <= 800)).toBe(true);
  });
});
