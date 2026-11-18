import { useRef } from 'react';
import { AnimatedNumber } from '../../motion/AnimatedNumber';
import { extent } from '../../charts/extent';
import { CutFrame } from '../CutFrame';
import type { Tone } from '../tone';
import tones from '../tones.module.css';
import { useElementSize } from '../useElementSize';
import { useShrinkToFit } from './useShrinkToFit';
import type_ from '../../styles/type.module.css';
import styles from './KPI.module.css';

type Props = {
  label: string;
  value: string;
  delta?: string;
  trend?: readonly number[];
  /** The last trend point is today: it gets a gold dot and a TODAY mark. */
  trendToday?: boolean;
  tone?: Tone;
  className?: string | undefined;
};

const TREND_W = 90;
const TREND_H = 32;
const VALUE_INSET = 32;

function trendXY(values: readonly number[]): [number, number][] {
  const [lo, hi] = extent(values);
  const span = hi - lo || 1;
  return values.map((v, i) => [(i / Math.max(values.length - 1, 1)) * TREND_W, TREND_H - ((v - lo) / span) * TREND_H]);
}

function TodayMark({ point }: { point: [number, number] | undefined }) {
  if (!point) return null;
  return (
    <>
      <circle className={styles.todayDot} cx={point[0]} cy={point[1]} r={3} />
      <text className={styles.todayText} x={TREND_W} y={TREND_H + 11} textAnchor="end">TODAY</text>
    </>
  );
}

export function KPI({ label, value, delta, trend, trendToday = false, tone = 'gold', className }: Props) {
  const ref = useRef<HTMLElement>(null);
  const [w, h] = useElementSize(ref);
  const valueRef = useRef<HTMLParagraphElement>(null);
  const fit = useShrinkToFit(valueRef, value, w - VALUE_INSET);
  const trendColor = tone === 'default' ? 'var(--sh-ink-dim)' : 'var(--accent)';
  return (
    <section
      ref={ref}
      className={[styles.kpi, tones[tone], className].filter(Boolean).join(' ')}
      data-tone={tone}
      aria-label={label}
    >
      <CutFrame width={w} height={h} />
      <span className={styles.bar} />
      <h2 className={`${type_.heading} ${styles.label}`}>{label}</h2>
      <p
        ref={valueRef}
        className={`${type_.number} ${styles.value}`}
        {...(fit < 1 ? { style: { transform: `scale(${fit})` } } : {})}
      >
        <AnimatedNumber text={value} />
      </p>
      {delta ? <p className={`${type_.mono} ${styles.delta}`} title={delta}>{delta}</p> : null}
      {trend && trend.length > 1 ? (
        <svg className={styles.trend} width={TREND_W} height={TREND_H} aria-hidden="true">
          <polyline
              className={styles.trendLine}
              pathLength={1}
              points={trendXY(trend).map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(' ')}
              fill="none"
              stroke={trendColor}
              strokeWidth="1.5"
              strokeLinecap="square"
            />
          {trendToday ? <TodayMark point={trendXY(trend).at(-1)} /> : null}
        </svg>
      ) : null}
    </section>
  );
}
