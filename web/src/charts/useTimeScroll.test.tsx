import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { timeBuckets } from '../format/scale';
import { FlowLines } from './FlowLines';
import { medianGap } from './useTimeScroll';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root | null = null;
let host: HTMLElement | null = null;
const frames: FrameRequestCallback[] = [];
let now = 0;

const SPAN = 300_000;
const WIDTH = 600;

function chart(latest: number) {
  const times = Array.from({ length: 10 }, (_, i) => latest - (9 - i) * 1_000);
  const values = times.map((_, i) => i * 10);
  return (
    <FlowLines
      times={times}
      series={[{ name: 'Down', tone: 'live', values }]}
      width={WIDTH}
      height={46}
      span={SPAN}
      format={String}
      label="Network"
    />
  );
}

function render(latest: number) {
  host ??= document.body.appendChild(document.createElement('div'));
  root ??= createRoot(host);
  act(() => root?.render(chart(latest)));
}

function renderFlow(values: readonly number[], span: number | undefined) {
  const times = values.map((_, i) => 1_000_000 + i * 1_000);
  host ??= document.body.appendChild(document.createElement('div'));
  root ??= createRoot(host);
  const series = [{ name: 'Down', tone: 'live' as const, values }];
  const flow = { times, series, width: WIDTH, height: 46, span, format: String, label: 'Network' };
  act(() => root?.render(<FlowLines {...flow} />));
}

/** Largest stretch factor (or its inverse) on any group, 1 when none is scaled. */
function stretchOf(): number {
  let worst = 1;
  for (const g of host?.querySelectorAll('g[transform*="scale"]') ?? []) {
    const k = Number(/scale\(1 ([^)]+)\)/.exec(g.getAttribute('transform') ?? '')?.[1]);
    worst = Math.max(worst, k, 1 / k);
  }
  return worst;
}

function runFrame(at: number) {
  now = at;
  act(() => {
    for (const cb of frames.splice(0)) cb(at);
  });
}

const slide = () => host?.querySelector('g[clip-path] > g')?.getAttribute('transform') ?? null;

function stubMotion(reduce: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: reduce && query.includes('reduce'),
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }));
}

beforeEach(() => {
  now = 0;
  frames.length = 0;
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => frames.push(cb));
  vi.stubGlobal('cancelAnimationFrame', () => undefined);
  vi.spyOn(performance, 'now').mockImplementation(() => now);
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('live time scroll', () => {
  it('slides the chart left at a steady speed between samples, with no jump when one arrives', () => {
    stubMotion(false);
    const perSample = (WIDTH / SPAN) * 1_000;
    render(1_000_000);
    expect(slide()).toBe(`translate(${perSample} 0)`);
    runFrame(500);
    expect(slide()).toBe(`translate(${perSample / 2} 0)`);
    runFrame(1_000);
    expect(slide()).toBe('translate(0 0)');
    now = 1_000;
    render(1_001_000);
    expect(slide()).toBe(`translate(${perSample} 0)`);
    const path = host?.querySelector('path[pathLength]')?.getAttribute('d') ?? '';
    expect(path).not.toContain('NaN');
  });

  it('stops after two late intervals instead of running past the newest sample', () => {
    stubMotion(false);
    render(1_000_000);
    runFrame(60_000);
    expect(slide()).toBe(`translate(${-(WIDTH / SPAN) * 1_000} 0)`);
  });

  it('catches up after a stall by easing, with no hop when the late sample arrives', () => {
    stubMotion(false);
    const perSample = (WIDTH / SPAN) * 1_000;
    render(1_000_000);
    runFrame(3_000);
    expect(slide()).toBe(`translate(${-perSample} 0)`);
    now = 3_000;
    render(1_003_000);
    expect(slide()).toBe(`translate(${2 * perSample} 0)`);
    runFrame(4_000);
    expect(slide()).toBe('translate(0 0)');
  });

  it('keeps the peak stretch bounded when the range flips between live and fixed, both ways', () => {
    stubMotion(false);
    const peaks: number[] = [];
    renderFlow([0, 30, 60, 90], SPAN);
    for (const span of [undefined, SPAN, undefined, SPAN]) {
      renderFlow([0, 30, 60, 90], span);
      peaks.push(stretchOf());
      for (let t = 16; t <= 800; t += 16) {
        runFrame(now + 16);
        peaks.push(stretchOf());
      }
    }
    expect(Math.max(...peaks)).toBe(1);
  });

  it('keeps the peak stretch bounded when the peak leaves or reaches near zero', () => {
    stubMotion(false);
    const peaks: number[] = [];
    renderFlow([0, 0, 0, 0], SPAN);
    for (const values of [[0, 0, 0, 5_000], [0, 0, 0, 0.001], [0, 0, 0, 5_000]]) {
      renderFlow(values, SPAN);
      for (let t = 16; t <= 800; t += 16) {
        runFrame(now + 16);
        peaks.push(stretchOf());
      }
    }
    expect(Math.max(...peaks)).toBeLessThanOrEqual(4);
    expect(Math.max(...peaks)).toBeGreaterThan(1);
  });

  it('stays still under reduced motion', () => {
    stubMotion(true);
    render(1_000_000);
    runFrame(500);
    expect(slide()).toBeNull();
  });
});

describe('time buckets', () => {
  it('averages samples into buckets aligned to the clock, so old bars keep their values', () => {
    const times = [10_000, 15_000, 21_000, 29_000, 31_000];
    const values = [2, 4, 6, null, 9];
    expect(timeBuckets(times, values, 10_000, 12_000)).toEqual({ starts: [10_000, 20_000, 30_000], means: [3, 6, 9] });
    expect(timeBuckets(times.slice(0, 3), values.slice(0, 3), 10_000, 12_000).means).toEqual([3, 6]);
  });

  it('takes the median sample gap', () => {
    expect(medianGap([0, 1_000, 2_000, 3_500, 4_500], 9)).toBe(1_000);
    expect(medianGap([5], 9)).toBe(9);
  });
});
