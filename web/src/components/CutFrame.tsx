import { useId } from 'react';
import styles from './CutFrame.module.css';

type Props = { width: number; height: number; cut?: number };

/** Panel outline with the top-left and bottom-right corners cut; colours come from the --tone custom property. */
export function CutFrame({ width, height, cut = 14 }: Props) {
  const id = useId();
  const tint = useId();
  if (width <= 0 || height <= 0) return null;
  const h = 0.5;
  const d = `M ${cut} ${h} L ${width - h} ${h} L ${width - h} ${height - cut} L ${width - cut} ${height - h} ` +
    `L ${h} ${height - h} L ${h} ${cut} Z`;
  return (
    <svg className={styles.frame} width={width} height={height} aria-hidden="true">
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" className={styles.stopTop} />
          <stop offset="1" className={styles.stopBottom} />
        </linearGradient>
        <linearGradient id={tint} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" className={styles.tintTop} />
          <stop offset="1" className={styles.tintBottom} />
        </linearGradient>
      </defs>
      <path d={d} className={styles.base} />
      <path d={d} fill={`url(#${tint})`} />
      <path d={d} fill={`url(#${id})`} />
      <path d={d} className={styles.stroke} />
    </svg>
  );
}
