import { useEffect, useLayoutEffect, useRef, type RefObject } from 'react';
import { useReducedMotion } from '../motion/useReducedMotion';

/** A late sample stops the slide here, in sample intervals, instead of drawing past the newest point. */
const MAX_LAG = 2;
const STEP_PX = 0.25;

export type TimeScroll = { latest: number; every: number; pxPerMs: number };

const ease = (p: number) => 1 - (1 - p) ** 3;

/** `debt` is the slide a stall held back; it is paid off over the next interval instead of in one hop. */
function shift(every: number, pxPerMs: number, elapsed: number, debt = 0): number {
  const owed = debt * (1 - ease(Math.min(1, elapsed / every)));
  const px = (every + owed - Math.min(elapsed, every * MAX_LAG)) * pxPerMs;
  return Math.round(px / STEP_PX) * STEP_PX;
}

/** Draws the newest sample one interval past the right edge and slides left until the next arrives, so a live chart
 * scrolls at a steady speed; reduced motion or a hidden page leaves it still. */
export function useTimeScroll(ref: RefObject<SVGGElement | null>, scroll: TimeScroll | null): void {
  const reduced = useReducedMotion();
  const arrived = useRef(0);
  const debt = useRef(0);
  const prev = useRef<{ latest: number; every: number } | null>(null);
  const on = scroll !== null && !reduced && scroll.pxPerMs > 0 && scroll.every > 0;
  const latest = scroll?.latest ?? 0;
  const every = scroll?.every ?? 0;
  const pxPerMs = scroll?.pxPerMs ?? 0;
  useLayoutEffect(() => {
    const now = performance.now();
    const last = prev.current;
    const late = last !== null && on && last.every === every && latest > last.latest;
    const held = late ? now - arrived.current - every * MAX_LAG : 0;
    debt.current = Math.min(Math.max(0, held), every * MAX_LAG);
    prev.current = on ? { latest, every } : null;
    arrived.current = now;
    const g = ref.current;
    if (g && on) g.setAttribute('transform', `translate(${shift(every, pxPerMs, 0, debt.current)} 0)`);
  }, [ref, on, latest, every, pxPerMs]);
  useEffect(() => {
    const g = ref.current;
    if (!g) return undefined;
    if (!on) {
      g.removeAttribute('transform');
      return undefined;
    }
    let id = 0;
    let last = Number.NaN;
    const frame = () => {
      const dx = shift(every, pxPerMs, performance.now() - arrived.current, debt.current);
      if (dx !== last) {
        g.setAttribute('transform', `translate(${dx} 0)`);
        last = dx;
      }
      id = requestAnimationFrame(frame);
    };
    const restart = () => {
      cancelAnimationFrame(id);
      if (!document.hidden) id = requestAnimationFrame(frame);
    };
    restart();
    document.addEventListener('visibilitychange', restart);
    return () => {
      cancelAnimationFrame(id);
      document.removeEventListener('visibilitychange', restart);
    };
  }, [ref, on, every, pxPerMs]);
}

/** Median gap between consecutive times, or `fallback` with fewer than two. */
export function medianGap(times: readonly number[], fallback: number): number {
  const gaps: number[] = [];
  for (let i = Math.max(1, times.length - 30); i < times.length; i += 1) {
    const gap = (times[i] ?? 0) - (times[i - 1] ?? 0);
    if (gap > 0) gaps.push(gap);
  }
  if (!gaps.length) return fallback;
  gaps.sort((a, b) => a - b);
  return gaps[Math.floor(gaps.length / 2)] ?? fallback;
}
