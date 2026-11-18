import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { Bars } from './Bars';
import { FlowLines } from './FlowLines';
import { LineArea } from './LineArea';
import { PairBars } from './PairBars';
import { RingChart } from './Ring';
import { Timeline } from './Timeline';

const bars = [10, 20, 40].map((value, i) => ({ key: `d${i}`, label: `day ${i}`, value, display: String(value) }));
const heights = (html: string) => [...html.matchAll(/<rect [^>]*?height="([\d.]+)"/g)].map((m) => Number(m[1]));
const clean = (html: string) => {
  expect(html).not.toMatch(/NaN|Infinity|undefined/);
  return html;
};

describe('Bars', () => {
  it('tells an absent day from a measured zero and keeps a datum without the flag as before', () => {
    const data = [
      { key: 'a', label: 'day a', value: 0, display: 'no data', absent: true },
      { key: 'b', label: 'day b', value: 0, display: '0' },
      { key: 'c', label: 'day c', value: 3, display: '3' },
    ];
    const html = clean(renderToStaticMarkup(<Bars data={data} width={300} height={80} label="x" />));
    const rects = [...html.matchAll(/<rect [^>]*>/g)].map((m) => m[0]);
    expect(rects).toHaveLength(3);
    expect(rects[0]).toMatch(/absentDay/);
    expect(rects[1]).toMatch(/emptyDay/);
    expect(rects[1]).not.toMatch(/absentDay/);
    expect(rects[2]).not.toMatch(/absentDay|emptyDay/);
    expect(html).toMatch(/day a<\/td><td[^>]*>no data/);
    expect(html).toMatch(/day b<\/td><td[^>]*>0</);
    const plain = renderToStaticMarkup(<Bars data={bars} width={300} height={80} label="x" />);
    expect(plain).not.toMatch(/absentDay/);
  });

  it('draws heights in proportion to the data at any width', () => {
    for (const width of [300, 600]) {
      const [a, b, c] = heights(renderToStaticMarkup(<Bars data={bars} width={width} height={100} label="x" />));
      expect(a).toBeCloseTo(c! / 4, 5);
      expect(b).toBeCloseTo(c! / 2, 5);
      expect(c).toBeCloseTo(98, 5);
    }
  });

  it('keeps every bar inside the box at a single huge value, with no data and with zeros', () => {
    const huge = [{ key: 'a', label: 'a', value: 1e15, display: '1e15' }];
    expect(heights(clean(renderToStaticMarkup(<Bars data={huge} width={320} height={70} label="x" />)))).toEqual([68]);
    clean(renderToStaticMarkup(<Bars data={[]} width={320} height={70} label="x" />));
    clean(renderToStaticMarkup(<Bars data={bars.map((b) => ({ ...b, value: 0 }))} width={320} height={70} label="x" />));
  });
});

describe('LineArea', () => {
  const pts = (n: number) => Array.from({ length: n }, (_, x) => ({ x, y: x * 10 }));

  it('renders no data, one point and a huge value at narrow and wide sizes', () => {
    for (const width of [280, 900]) {
      for (const data of [pts(0), pts(1), [{ x: 0, y: 1e15 }, { x: 1, y: 3 }]]) {
        clean(renderToStaticMarkup(<LineArea data={data} width={width} height={84} label="x" />));
      }
    }
  });

  it('ends the line on the last data point', () => {
    const html = renderToStaticMarkup(<LineArea data={pts(3)} width={200} height={100} label="x" />);
    expect(html).toMatch(/d="M44,88C[^"]*188,12"/);
  });
});

describe('other charts with edge data', () => {
  it('PairBars, FlowLines, Timeline and RingChart stay finite', () => {
    clean(renderToStaticMarkup(<PairBars a={{ name: 'a', tone: 'live', values: [] }} b={{ name: 'b', tone: 'gold', values: [] }} width={260} height={36} label="x" format={String} />));
    clean(renderToStaticMarkup(<PairBars a={{ name: 'a', tone: 'live', values: [1e15] }} b={{ name: 'b', tone: 'gold', values: [0] }} width={260} height={36} label="x" format={String} />));
    clean(renderToStaticMarkup(<FlowLines times={[]} series={[{ name: 'd', tone: 'live', values: [] }]} width={300} height={46} label="x" format={String} />));
    clean(renderToStaticMarkup(<FlowLines times={[5]} series={[{ name: 'd', tone: 'live', values: [1e15] }]} width={300} height={46} label="x" format={String} />));
    clean(renderToStaticMarkup(<Timeline rows={0} spans={[]} marks={[]} start={0} end={1000} width={300} height={88} label="x" />));
    clean(renderToStaticMarkup(<RingChart rings={[{ name: 'a', value: 0, tone: 'gold' }, { name: 'b', value: 1, tone: 'live' }]} size={240} label="x" />));
  });

});
