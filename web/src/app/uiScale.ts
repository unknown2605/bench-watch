import { useSyncExternalStore } from 'react';

export const DESIGN_W = 1440;
export const DESIGN_H = 900;

/** Whole-page scale: 1 up to the 1440x900 design size, then the largest factor that still fits both axes. */
export function scaleFor(width: number, height: number): number {
  if (!(width > DESIGN_W) || !(height > DESIGN_H)) return 1;
  return Math.floor(Math.min(width / DESIGN_W, height / DESIGN_H) * 1000) / 1000;
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener('resize', onChange);
  return () => window.removeEventListener('resize', onChange);
}

/** Scale for this window; 1 where the browser cannot report zoom, since measured drawings would be misplaced. */
export function windowScale(): number {
  if (!('currentCSSZoom' in Element.prototype)) return 1;
  return scaleFor(window.innerWidth, window.innerHeight);
}

export function useUiScale(): number {
  return useSyncExternalStore(subscribe, windowScale, () => 1);
}

/** Factor between an element's on-screen and layout pixels; 1 where the browser does not report it. */
export function cssZoomOf(el: Element): number {
  const zoom = (el as Element & { currentCSSZoom?: number }).currentCSSZoom;
  return typeof zoom === 'number' && zoom > 0 ? zoom : 1;
}
