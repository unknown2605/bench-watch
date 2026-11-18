import { useEffect, useLayoutEffect, useRef } from 'react';
import { useReducedMotion } from './useReducedMotion';

const ease = (p: number) => 1 - (1 - p) ** 3;

type Paint = (values: readonly number[]) => void;

/** Eases each value to its target over `duration` ms and hands every frame to `paint`, without a React render.
 * The caller renders the targets; a new length, reduced motion or a hidden page leaves or paints them at once. */
export function useFrameTween(targets: readonly number[], paint: Paint, duration = 600): void {
  const reduced = useReducedMotion();
  const sig = targets.join(',');
  const shown = useRef<readonly number[]>(targets);
  const painter = useRef(paint);
  useLayoutEffect(() => {
    painter.current = paint;
    const at = shown.current;
    // React has just written the targets: put the values on screen back before the browser paints them.
    if (!reduced && at.length === targets.length && at.some((v, i) => v !== targets[i])) paint(at);
  });
  useEffect(() => {
    const goal = sig === '' ? [] : sig.split(',').map(Number);
    const from = shown.current;
    const finish = () => {
      shown.current = goal;
      painter.current(goal);
    };
    if (from.length === goal.length && from.every((v, i) => v === goal[i])) {
      shown.current = goal;
      return undefined;
    }
    if (reduced || duration <= 0 || document.hidden || from.length !== goal.length) {
      finish();
      return undefined;
    }
    let t0 = 0;
    let id = 0;
    const step = (now: number) => {
      t0 ||= now;
      const p = Math.min(1, (now - t0) / duration);
      if (p >= 1 || document.hidden) {
        finish();
        return;
      }
      shown.current = from.map((v, i) => v + ((goal[i] ?? v) - v) * ease(p));
      painter.current(shown.current);
      id = requestAnimationFrame(step);
    };
    id = requestAnimationFrame(step);
    return () => cancelAnimationFrame(id);
  }, [sig, duration, reduced]);
}
