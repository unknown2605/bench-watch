import { scaleSqrt } from 'd3-scale';
import { useId } from 'react';
import { ChartTable } from './ChartTable';
import { extent } from './extent';
import { TONE_VAR, type DataTone } from './colors';
import styles from './charts.module.css';

export type DiscSpec = { name: string; value: number; tone: DataTone };
type Props = { discs: readonly DiscSpec[]; width?: number; label: string };

const THICK = 12;

export function DiscTower({ discs, width = 220, label }: Props) {
  const max = Math.max(extent(discs.map((d) => d.value))[1], 1);
  const rx = scaleSqrt().domain([0, max]).range([18, width / 2 - 4]);
  const height = discs.length * (THICK + 6) + 40;
  const tableId = useId();
  return (
    <>
      <svg
        className={styles.chart}
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={label}
        aria-describedby={tableId}
      >
        {discs.map((d, i) => {
          const r = rx(d.value);
          const cy = height - 24 - i * (THICK + 6);
          const color = TONE_VAR[d.tone];
          return (
            <g key={`${i}-${d.name}`} className={styles.glowSoft}>
              <path
                d={`M ${width / 2 - r} ${cy} v ${THICK} a ${r} ${r * 0.28} 0 0 0 ${r * 2} 0 v ${-THICK}`}
                fill={color}
                fillOpacity="0.35"
              />
              <ellipse cx={width / 2} cy={cy} rx={r} ry={r * 0.28} fill={color} fillOpacity="0.85" stroke={color} />
            </g>
          );
        })}
      </svg>
      <ChartTable
        id={tableId}
        caption={label}
        head={['Disc', 'Value']}
        rows={[...discs].reverse().map((d) => [d.name, d.value])}
      />
    </>
  );
}
