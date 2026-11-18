import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { KPI } from '../components/KPI/KPI';
import { DiscTower } from './DiscTower';
import { extent } from './extent';

const BIG = 150_000;

describe('extent', () => {
  it('finds the smallest and largest value of a series too long to spread', () => {
    const values = Array.from({ length: BIG }, (_, i) => (i * 7919) % BIG - 5);
    expect(extent(values)).toEqual([-5, BIG - 6]);
    expect(extent([])).toEqual([Infinity, -Infinity]);
    expect(extent([3, Number.NaN, 1])).toEqual([1, 3]);
  });

  it('gives one value, or values that are all equal, as both ends', () => {
    expect(extent([5])).toEqual([5, 5]);
    expect(extent([2, 2, 2])).toEqual([2, 2]);
  });

  it('lets a KPI trend and a disc tower take a series that long', { timeout: 30_000 }, () => {
    const trend = Array.from({ length: BIG }, (_, i) => i % 11);
    expect(renderToStaticMarkup(<KPI label="Big" value="1" trend={trend} />)).toContain('<polyline');
    const discs = Array.from({ length: BIG }, (_, i) => ({ name: `d${i}`, value: i, tone: 'gold' as const }));
    expect(renderToStaticMarkup(<DiscTower discs={discs} label="Big" />)).toContain('<td>d149999</td>');
  });
});
