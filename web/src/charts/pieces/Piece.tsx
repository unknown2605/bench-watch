import {
  useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore,
  type CSSProperties, type KeyboardEvent, type ReactNode, type Ref, type RefObject,
} from 'react';
import { cssZoomOf } from '../../app/uiScale';
import { AnimatedNumber } from '../../motion/AnimatedNumber';
import { formatLike, parseNumber } from '../../motion/number';
import { useReducedMotion } from '../../motion/useReducedMotion';
import { stateAttr, type PieceState, type Pt } from './geometry';
import styles from './pieces.module.css';
import pick from './select.module.css';

function subscribeHidden(onChange: () => void): () => void {
  document.addEventListener('visibilitychange', onChange);
  return () => document.removeEventListener('visibilitychange', onChange);
}

export function useDocumentHidden(): boolean {
  return useSyncExternalStore(subscribeHidden, () => document.hidden, () => false);
}

type PieceProps = {
  name: string;
  label: string;
  state: PieceState;
  className?: string | undefined;
  style?: CSSProperties;
  rootRef?: Ref<HTMLElement>;
  children: ReactNode;
};

/** Root of every centre piece: names its edge state and pauses its loops while the page is hidden. */
export function Piece({ name, label, state, className, style, rootRef, children }: PieceProps) {
  const hidden = useDocumentHidden();
  return (
    <figure
      ref={rootRef}
      className={[styles.piece, className].filter(Boolean).join(' ')}
      style={style}
      data-piece={name}
      data-state={stateAttr(state)}
      data-paused={hidden ? '' : undefined}
      aria-label={label}
    >
      {children}
    </figure>
  );
}

/** Counts up from zero on first paint, then tweens between values; reduced motion shows the value at once. */
export function CountUp({ text, className }: { text: string; className?: string | undefined }) {
  const reduced = useReducedMotion();
  const [started, setStarted] = useState(false);
  useEffect(() => {
    const id = requestAnimationFrame(() => setStarted(true));
    return () => cancelAnimationFrame(id);
  }, []);
  const parsed = parseNumber(text);
  const shown = started || reduced || !parsed ? text : `${parsed.pre}${formatLike(0, parsed)}${parsed.post}`;
  return <AnimatedNumber text={shown} className={className} />;
}

/** `elbow` routes out level from the start, then along the list edge: leaders sharing a start never cross. */
export type LeaderSpec = { key: string; from: Pt; side: 'left' | 'right'; elbow?: boolean };

/** Leader lines from points of a drawing (its own pixel coordinates) to the first line of their list rows. */
export function useLeaders(
  root: RefObject<HTMLElement | null>,
  drawing: RefObject<Element | null>,
  rows: RefObject<Map<string, HTMLElement>>,
  specs: readonly LeaderSpec[],
): string[] {
  const [paths, setPaths] = useState<string[]>([]);
  const key = specs.map((s) => `${s.key}:${s.from.x},${s.from.y}:${s.side}:${s.elbow ? 1 : 0}`).join('|');
  const specsRef = useRef(specs);
  useLayoutEffect(() => {
    specsRef.current = specs;
  });
  useLayoutEffect(() => {
    const el = root.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const measure = () => {
      const draw = drawing.current;
      if (!draw) return;
      const base = el.getBoundingClientRect();
      const box = draw.getBoundingClientRect();
      const zoom = cssZoomOf(el);
      const next: string[] = [];
      for (const spec of specsRef.current) {
        const row = rows.current.get(spec.key);
        if (!row) continue;
        const r = row.getBoundingClientRect();
        const sx = (box.left - base.left) / zoom + spec.from.x;
        const sy = (box.top - base.top) / zoom + spec.from.y;
        const ty = (r.top - base.top) / zoom + 8;
        const right = spec.side === 'right';
        const ex = right ? (r.left - base.left) / zoom - 6 : (r.right - base.left) / zoom + 6;
        // A row that wrapped under the drawing gets no leader rather than one crossing the marks.
        if (right ? ex <= sx + 8 : ex >= sx - 8) continue;
        const [x0, y0, y1, x2] = [sx, sy, ty, ex].map((n) => n.toFixed(1));
        if (spec.elbow) {
          const bx = (right ? Math.max(sx, ex - 10) : Math.min(sx, ex + 10)).toFixed(1);
          next.push(`M ${x0} ${y0} L ${bx} ${y0} L ${bx} ${y1} L ${x2} ${y1}`);
          continue;
        }
        const bend = (right ? Math.max(sx, ex - 28) : Math.min(sx, ex + 28)).toFixed(1);
        next.push(`M ${x0} ${y0} L ${bend} ${y1} L ${x2} ${y1}`);
      }
      setPaths((prev) => (prev.join('|') === next.join('|') ? prev : next));
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [root, drawing, rows, key]);
  return paths;
}

/** Keeps a map of list rows by key, for leaders and for linking a row to its mark. */
export function useRowRefs() {
  const rows = useRef(new Map<string, HTMLElement>());
  const bind = (key: string) => (el: HTMLElement | null) => {
    if (el) rows.current.set(key, el);
    else rows.current.delete(key);
  };
  return { rows, bind };
}

export function Leaders({ paths }: { paths: readonly string[] }) {
  if (!paths.length) return null;
  return (
    <svg className={styles.leaders} aria-hidden="true">
      {paths.map((d, i) => (
        <path key={i} d={d} className={styles.leader} style={{ '--i': i } as CSSProperties} pathLength={1} />
      ))}
    </svg>
  );
}

/** Index of the mark or row under the pointer or focus; both sides glow together. */
export function useActive() {
  const [active, setActive] = useState<number | null>(null);
  const on = (i: number) => ({
    onPointerEnter: () => setActive(i),
    onPointerLeave: () => setActive((a) => (a === i ? null : a)),
    onFocus: () => setActive(i),
    onBlur: () => setActive((a) => (a === i ? null : a)),
    'data-active': active === i ? '' : undefined,
  });
  return { active, on };
}

/** Optional click-to-select: the picked mark's key and the callback each mark or row calls with its own key. */
export type Selection = { selected?: string | null | undefined; onSelect?: ((key: string) => void) | undefined };

/** Pressed state and activation for one selectable mark; nothing at all when the piece has no onSelect. */
export function selectable({ selected, onSelect }: Selection, key: string, native = false) {
  if (!onSelect) return {};
  const pressed = selected === key;
  const own = { 'aria-pressed': pressed, 'data-selected': pressed ? '' : undefined, onClick: () => onSelect(key) };
  if (native) return own;
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    e.preventDefault();
    if (e.repeat) return;
    onSelect(key);
  };
  return { ...own, role: 'button', tabIndex: 0, onKeyDown };
}

/** A list row keeps its `li` semantics; the pressed state and click live on a real button inside it. */
export function RowButton({ sel, id, children }: { sel: Selection; id: string; children: ReactNode }) {
  if (!sel.onSelect) return <>{children}</>;
  const { onSelect } = sel;
  const pressed = sel.selected === id;
  return (
    <button
      type="button"
      className={pick.button}
      aria-pressed={pressed}
      data-selected={pressed ? '' : undefined}
      onClick={() => onSelect(id)}
    >
      {children}
    </button>
  );
}

/** Marks the `li` itself so its highlight follows the button inside. */
export const rowSelected = (sel: Selection, id: string) =>
  (sel.onSelect && sel.selected === id ? { 'data-selected': '' } : {});

export const vars = (v: Record<string, string | number>) => v as CSSProperties;
