import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useElementSize } from './useElementSize';
import styles from './Fit.module.css';

/** Marks the box while it changes size, so chart paths follow the axes instead of easing behind them. */
const RESIZE_SETTLE_MS = 600;

type Props = {
  /** Size used until the first measurement, and where no layout exists (tests). */
  fallback: readonly [number, number];
  /** Chart height in px, the box grows to its content; without it the box fills its positioned parent. */
  height?: number;
  className?: string | undefined;
  children: (width: number, height: number) => ReactNode;
};

export function Fit({ fallback, height, className, children }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const [w, h] = useElementSize(ref);
  const fixed = height !== undefined;
  const [seen, setSeen] = useState<[number, number]>([w, h]);
  const [resizing, setResizing] = useState(false);
  if (seen[0] !== w || seen[1] !== h) {
    setSeen([w, h]);
    setResizing(true);
  }
  useEffect(() => {
    if (!resizing) return undefined;
    const id = setTimeout(() => setResizing(false), RESIZE_SETTLE_MS);
    return () => clearTimeout(id);
  }, [resizing, seen]);
  return (
    <div
      ref={ref}
      {...(resizing ? { 'data-resizing': '' } : {})}
      className={[fixed ? styles.row : styles.fill, className].filter(Boolean).join(' ')}
      {...(fixed ? { style: { minHeight: height } } : {})}
    >
      {children(w || fallback[0], fixed ? height : h || fallback[1])}
    </div>
  );
}
