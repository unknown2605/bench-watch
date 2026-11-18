import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { coreLoad, containers, discs, ringValues, stars, tokenFlow, tokenMarkers } from '../mock/data';
import { DiscTower } from './DiscTower';
import { Honeycomb } from './Honeycomb';
import { LineArea } from './LineArea';
import { CoreRing, RingChart } from './Ring';
import { Shield } from './Shield';
import { StarMap } from './StarMap';

describe('chart accessibility', () => {
  it('every chart names itself and ships its values as a table', () => {
    const hives = containers.map((c) => ({ name: c.name, value: `${c.memoryMb} MB`, health: c.health }));
    const charts = [
      renderToStaticMarkup(<RingChart rings={ringValues} label="Rings" />),
      renderToStaticMarkup(<CoreRing values={coreLoad} label="Cores" />),
      renderToStaticMarkup(<DiscTower discs={discs} label="Tower" />),
      renderToStaticMarkup(<StarMap nodes={stars} label="Stars" />),
      renderToStaticMarkup(<Shield value={0.5} display="50%" caption="BLOCKED" label="Shield" />),
      renderToStaticMarkup(<Honeycomb cells={hives} label="Hives" />),
      renderToStaticMarkup(<LineArea data={tokenFlow} markers={tokenMarkers} label="Flow" />),
    ];
    for (const html of charts) {
      expect(html).toMatch(/role="img" aria-label="[^"]+"/);
      expect(html).toMatch(/aria-describedby="([^"]+)"/);
      expect(html).toContain('<table');
      expect(html).toContain('<caption>');
    }
  });

  it('draws a dot for a one-point series, which has no line to draw', () => {
    const html = renderToStaticMarkup(<LineArea data={[{ x: 0, y: 5 }]} label="One" />);
    expect(html).toMatch(/<circle[^>]*r="3"/);
  });

  it('labels the accessible table rows by their names when the chart has them', () => {
    const data = [{ x: 0, y: 1 }, { x: 1, y: 2 }];
    const html = renderToStaticMarkup(<LineArea data={data} xLabels={['10 Nov', '11 Nov']} label="Days" />);
    expect(html).toContain('<td>10 Nov</td>');
    expect(html).toContain('<td>11 Nov</td>');
  });

  it('keeps each arc title on the hexagon cell for hover', () => {
    const html = renderToStaticMarkup(<Honeycomb cells={[{ name: 'long-container-name-that-is-cut', value: '1 MB', health: 'warn' }]} label="Hives" />);
    expect(html).toContain('<title>long-container-name-that-is-cut</title>');
  });

  it('shows container health as text and as a shape, not only a colour', () => {
    const cells = [{ name: 'a', value: '1 MB', health: 'warn' }, { name: 'b', value: '2 MB', health: 'down' }] as const;
    const html = renderToStaticMarkup(<Honeycomb cells={cells} label="Hives" />);
    expect(html).toContain('<td>warn</td>');
    expect(html).toContain('<td>down</td>');
    expect(html).toContain('M4 0L8 8H0Z');
  });

  it('lists every marker on its nearest point, several on one point together, under a Markers header', () => {
    const data = [0, 1, 2, 3].map((x) => ({ x, y: x * 10 }));
    const markers = [
      { x: 0.9, kind: 'event', name: 'a' },
      { x: 1.2, kind: 'event', name: 'b' },
      { x: 3, kind: 'event', name: 'c' },
    ] as const;
    const html = renderToStaticMarkup(<LineArea data={data} markers={markers} label="Small" />);
    expect(html).toContain('<th scope="col">Markers</th>');
    expect(html).toContain('<td>1</td><td>10</td><td>a, b</td>');
    expect(html).toContain('<td>2</td><td>20</td><td></td>');
    expect(html).toContain('<td>3</td><td>30</td><td>c</td>');
  });

  // 150k points: past the about 125k arguments at which a spread into Math.min or Math.max throws.
  it('lists every marker once for a very long series', { timeout: 30_000 }, () => {
    const data = Array.from({ length: 150_000 }, (_, i) => ({ x: i, y: i % 7 }));
    const markers = [
      { x: 10.2, kind: 'event', name: 'first' },
      { x: 10, kind: 'event', name: 'second' },
      { x: 149_999, kind: 'event', name: 'last' },
    ] as const;
    const html = renderToStaticMarkup(<LineArea data={data} markers={markers} label="Big" />);
    const cells = html.match(/<td>[^<]*(?:first|second|last)[^<]*<\/td>/g) ?? [];
    expect(cells).toEqual(['<td>first, second</td>', '<td>last</td>']);
  });
});

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

describe('repeated name segments', () => {
  it('give the hexagon name lines distinct keys', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    try {
      const name = 'a'.repeat(30);
      const host = document.createElement('div');
      const root = createRoot(host);
      act(() => root.render(<Honeycomb cells={[{ name, value: '1', health: 'healthy' }]} label="Hives" />));
      act(() => root.unmount());
      expect(spy.mock.calls.filter((c) => String(c[0]).includes('same key'))).toEqual([]);
    } finally {
      spy.mockRestore();
    }
  });
});

describe('honeycomb ranks', () => {
  it('keeps the cells in DOM order when ranks swap, moving them by transform only', () => {
    const host = document.createElement('div');
    const root = createRoot(host);
    const cell = (name: string) => ({ name, value: '1', health: 'healthy' as const });
    const cells = () => Array.from(host.querySelectorAll('svg > g'));
    const at = (name: string) => cells().find((g) => g.querySelector('title')?.textContent === name);
    try {
      act(() => root.render(<Honeycomb cells={['a', 'b', 'c'].map(cell)} label="Hives" />));
      const before = cells();
      const aWas = (at('a') as SVGGElement | undefined)?.style.transform;
      act(() => root.render(<Honeycomb cells={['c', 'a', 'b'].map(cell)} label="Hives" />));
      expect(cells()).toEqual(before);
      expect(cells().every((g, i) => g === before[i])).toBe(true);
      expect((at('a') as SVGGElement | undefined)?.style.transform).not.toBe(aWas);
    } finally {
      act(() => root.unmount());
    }
  });
});
