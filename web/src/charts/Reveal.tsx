import styles from './motion.module.css';

const TAU = Math.PI * 2;

export type RevealRing = { r: number; width: number };

function point(r: number, a: number): string {
  return `${(Math.sin(a) * r).toFixed(2)} ${(-Math.cos(a) * r).toFixed(2)}`;
}

export function arcLine(r: number, start: number, sweep: number): string {
  if (sweep >= TAU - 0.001) {
    const half = `A ${r} ${r} 0 1 1`;
    return `M ${point(r, start)} ${half} ${point(r, start + Math.PI)} ${half} ${point(r, start)}`;
  }
  return `M ${point(r, start)} A ${r} ${r} 0 ${sweep > Math.PI ? 1 : 0} 1 ${point(r, start + sweep)}`;
}

type Props = { id: string; rings: readonly RevealRing[]; start?: number; sweep?: number; extent: number };

/** A mask that uncovers arcs by drawing a stroke over them; apply it as `mask="url(#id)"` to the centred group. */
export function Reveal({ id, rings, start = 0, sweep = TAU, extent }: Props) {
  return (
    <mask id={id} maskUnits="userSpaceOnUse" x={-extent} y={-extent} width={extent * 2} height={extent * 2}>
      {rings.map((ring, i) => (
        <path
          key={i}
          className={styles.sweep}
          d={arcLine(ring.r, start, sweep)}
          pathLength={1}
          fill="none"
          stroke="#fff"
          strokeWidth={ring.width}
        />
      ))}
    </mask>
  );
}
