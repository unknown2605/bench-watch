import type { CSSProperties } from 'react';

/** Chrome interpolates a changed `d` between paths of the same shape, so a poll moves a mark instead of redrawing it. */
export function morph(d: string | null | undefined): CSSProperties | undefined {
  return d ? ({ d: `path("${d}")` } as CSSProperties) : undefined;
}
