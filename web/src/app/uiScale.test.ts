import { afterEach, describe, expect, it, vi } from 'vitest';
import { cssZoomOf, scaleFor, windowScale } from './uiScale';

describe('scaleFor', () => {
  it('stays at 1 up to the design size and below the desktop layout', () => {
    expect(scaleFor(1440, 900)).toBe(1);
    expect(scaleFor(1280, 900)).toBe(1);
    expect(scaleFor(768, 1024)).toBe(1);
    expect(scaleFor(2560, 900)).toBe(1);
    expect(scaleFor(1440, 2000)).toBe(1);
  });

  it('grows with the tighter axis so the page still fits', () => {
    expect(scaleFor(2560, 1440)).toBe(1.6);
    expect(scaleFor(3840, 2160)).toBe(2.4);
    expect(scaleFor(2560, 1080)).toBe(1.2);
    expect(scaleFor(1728, 1000)).toBe(1.111);
  });

  it('never rounds up past the fit', () => {
    const z = scaleFor(1919, 1079);
    expect(z * 1440).toBeLessThanOrEqual(1919);
    expect(z * 900).toBeLessThanOrEqual(1079);
  });

  it('answers 1 for sizes that are not numbers', () => {
    expect(scaleFor(Number.NaN, 900)).toBe(1);
  });
});

describe('cssZoomOf', () => {
  it('reads the browser value and falls back to 1', () => {
    expect(cssZoomOf({ currentCSSZoom: 1.6 } as unknown as Element)).toBe(1.6);
    expect(cssZoomOf({} as Element)).toBe(1);
    expect(cssZoomOf({ currentCSSZoom: 0 } as unknown as Element)).toBe(1);
  });
});

describe('windowScale', () => {
  afterEach(() => {
    Reflect.deleteProperty(Element.prototype, 'currentCSSZoom');
    vi.unstubAllGlobals();
  });

  it('stays 1 on a large window when the browser cannot report zoom', () => {
    vi.stubGlobal('innerWidth', 2560);
    vi.stubGlobal('innerHeight', 1440);
    expect(windowScale()).toBe(1);
  });

  it('scales a large window when the browser reports zoom', () => {
    Object.defineProperty(Element.prototype, 'currentCSSZoom', { configurable: true, get: () => 1 });
    vi.stubGlobal('innerWidth', 2560);
    vi.stubGlobal('innerHeight', 1440);
    expect(windowScale()).toBe(1.6);
  });
});
