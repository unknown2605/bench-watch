import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from 'react';

/** epoch counts the intros of a tab; active is true while its load motion plays. */
export type Intro = { epoch: number; active: boolean };

/** Shortest intro; it lasts longer while a load animation of the tab still runs, up to INTRO_MAX_MS. */
export const INTRO_MS = 2_400;
/** Set on the tab's root while its intro is active; the load-motion CSS keys on it. */
export const INTRO_ATTR = 'data-intro';
const INTRO_MAX_MS = 6_000;
const RECHECK_MS = 250;

function busy(root: HTMLElement | null): boolean {
  if (!root || typeof root.getAnimations !== 'function' || typeof CSSAnimation === 'undefined') return false;
  return root
    .getAnimations({ subtree: true })
    .some(
      (a) => a instanceof CSSAnimation && a.playState === 'running' && a.effect?.getTiming().iterations !== Infinity,
    );
}

/** A scene is a tab with its range and project or stack; a live update of the same scene never replays its intro. */
export function useIntroScene(scene: string, ready: boolean, root: RefObject<HTMLElement | null>): Intro {
  const [state, setState] = useState(() =>
    ready ? { scene, epoch: 1, active: true } : { scene: '', epoch: 0, active: false },
  );
  if (ready && state.scene !== scene) setState({ scene, epoch: state.epoch + 1, active: true });
  const { epoch, active } = state;
  const playing = useRef(0);
  useLayoutEffect(() => {
    const el = root.current;
    // A new scene inside a running intro keeps the attribute on, so finished CSS animations would not replay.
    if (active && el && playing.current !== 0 && playing.current !== epoch && el.hasAttribute(INTRO_ATTR)) {
      el.removeAttribute(INTRO_ATTR);
      void el.offsetWidth;
      el.setAttribute(INTRO_ATTR, '');
    }
    playing.current = active ? epoch : 0;
  }, [epoch, active, root]);
  useEffect(() => {
    if (!active) return undefined;
    const started = performance.now();
    let id: ReturnType<typeof setTimeout>;
    const check = () => {
      if (busy(root.current) && performance.now() - started < INTRO_MAX_MS) {
        id = setTimeout(check, RECHECK_MS);
        return;
      }
      setState((s) => (s.epoch === epoch ? { ...s, active: false } : s));
    };
    id = setTimeout(check, INTRO_MS);
    return () => clearTimeout(id);
  }, [epoch, active, root]);
  return { epoch, active };
}
