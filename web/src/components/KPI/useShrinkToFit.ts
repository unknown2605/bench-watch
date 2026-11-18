import { useLayoutEffect, useState, type RefObject } from 'react';

/** Scale (at most 1) that fits `text`, drawn in the element's own font, into `room` px; measured on the final text. */
export function useShrinkToFit(ref: RefObject<HTMLElement | null>, text: string, room: number): number {
  const [scale, setScale] = useState(1);
  useLayoutEffect(() => {
    const el = ref.current;
    const ctx = document.createElement('canvas').getContext('2d');
    if (!el || !ctx || room <= 0) return;
    const style = getComputedStyle(el);
    ctx.font = style.font;
    const tracking = Number.parseFloat(style.letterSpacing) || 0;
    const natural = ctx.measureText(text).width + tracking * text.length;
    setScale(natural > room ? Math.max(0.2, room / natural) : 1);
  }, [ref, text, room]);
  return scale;
}
