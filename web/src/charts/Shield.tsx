import { useId } from 'react';
import { ChartTable } from './ChartTable';
import { TONE_VAR, type DataTone } from './colors';
import styles from './charts.module.css';

type Props = { value: number; display: string; caption: string; tone?: DataTone; size?: number; label: string };

const OUTLINE = 'M60 4 L112 22 V70 C112 104 88 128 60 140 C32 128 8 104 8 70 V22 Z';

export function Shield({ value, display, caption, tone = 'live', size = 140, label }: Props) {
  const id = useId();
  const color = TONE_VAR[tone];
  const v = Math.max(0, Math.min(1, value));
  const top = 140 - v * 136;
  const tableId = useId();
  return (
    <>
      <svg className={styles.chart} width={size * (120 / 144)} height={size} viewBox="0 0 120 144" role="img" aria-label={label} aria-describedby={tableId}>
        <defs>
          <clipPath id={id}>
            <rect x="0" y={top} width="120" height="144" />
          </clipPath>
        </defs>
        <path d={OUTLINE} fill="none" stroke={color} strokeOpacity="0.8" strokeWidth="1.5" />
        <g clipPath={`url(#${id})`} className={styles.glowSoft}>
          <path d={OUTLINE} fill={color} fillOpacity="0.28" />
        </g>
        <path d="M60 18 L100 32 V70 C100 96 82 114 60 124 C38 114 20 96 20 70 V32 Z" fill="none" stroke={color} strokeOpacity="0.3" />
        <text className={styles.shieldValue} x="60" y="72" textAnchor="middle">{display}</text>
        <text className={styles.caption} x="60" y="92" textAnchor="middle">{caption}</text>
      </svg>
      <ChartTable id={tableId} caption={label} head={['Measure', 'Value']} rows={[[caption, display]]} />
    </>
  );
}
