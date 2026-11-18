import { useEffect, useRef, useState } from 'react';
import { useReducedMotion } from './useReducedMotion';

const ease = (p: number) => 1 - (1 - p) ** 3;

/** Eases to `target` over `duration` ms; a hidden page, reduced motion or a new `resetKey` (unit) jumps. */
export function useTween(target: number, duration = 600, resetKey = ''): number {
  const reduced = useReducedMotion();
  const [state, setState] = useState({ key: resetKey, value: target });
  const shown = useRef(target);
  const key = useRef(resetKey);
  useEffect(() => {
    const settle = () => {
      shown.current = target;
      setState({ key: resetKey, value: target });
    };
    const jump = key.current !== resetKey;
    key.current = resetKey;
    if (reduced || jump || duration <= 0 || document.hidden || shown.current === target) {
      if (shown.current !== target || jump) {
        const id = requestAnimationFrame(settle);
        return () => cancelAnimationFrame(id);
      }
      return undefined;
    }
    const start = shown.current;
    let t0 = 0;
    let id = 0;
    const step = (now: number) => {
      t0 ||= now;
      const p = Math.min(1, (now - t0) / duration);
      if (p >= 1 || document.hidden) {
        settle();
        return;
      }
      shown.current = start + (target - start) * ease(p);
      setState({ key: resetKey, value: shown.current });
      id = requestAnimationFrame(step);
    };
    id = requestAnimationFrame(step);
    const onHide = () => {
      if (document.hidden) {
        cancelAnimationFrame(id);
        settle();
      }
    };
    document.addEventListener('visibilitychange', onHide);
    return () => {
      cancelAnimationFrame(id);
      document.removeEventListener('visibilitychange', onHide);
    };
  }, [target, duration, resetKey, reduced]);
  return reduced || state.key !== resetKey ? target : state.value;
}
