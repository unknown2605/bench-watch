import { scaleBand, scaleLinear } from 'd3-scale';
import { useId, useRef, type CSSProperties } from 'react';
import { ChartTable } from './ChartTable';
import { TONE_VAR, type DataTone } from './colors';
import motion from './motion.module.css';
import styles from './tabcharts.module.css';
import { useTimeScroll } from './useTimeScroll';

export type PairSeries = { name: string; tone: DataTone; values: readonly number[] };
/** Bars on a time axis: `starts` are the bucket start times in ms, aligned with the values. */
export type PairTimes = { starts: readonly number[]; bucket: number; span: number; latest: number; every: number };
type Props = {
  a: PairSeries;
  b: PairSeries;
  width: number;
  height: number;
  label: string;
  format: (v: number) => string;
  /** Scrolls the bars with time, as a live chart, instead of spreading them over the width. */
  times?: PairTimes | undefined;
};

export function PairBars({ a, b, width, height, label, format, times }: Props) {
  const tableId = useId();
  const clipId = useId();
  const slide = useRef<SVGGElement>(null);
  const n = Math.max(a.values.length, b.values.length);
  const keys = times ? times.starts.map(String) : Array.from({ length: n }, (_, i) => String(i));
  let max = 0;
  for (const v of [...a.values, ...b.values]) if (v > max) max = v;
  const k = times ? width / times.span : 0;
  const band = scaleBand<string>().domain(keys).range([0, width]).paddingInner(0.25);
  const xAt = (i: number) => (times ? width + ((times.starts[i] ?? 0) - times.latest) * k : (band(keys[i] ?? '') ?? 0));
  const w = (times ? times.bucket * k * 0.75 : band.bandwidth()) / 2;
  const y = scaleLinear().domain([0, max || 1]).range([height, 0]);
  useTimeScroll(slide, times ? { latest: times.latest, every: times.every, pxPerMs: k } : null);
  return (
    <>
      <svg className={times ? `${styles.svg} ${styles.liveLayer}` : styles.svg} width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label} aria-describedby={tableId}>
        {times ? (
          <defs>
            <clipPath id={clipId}>
              <rect x={0} y={0} width={width} height={height} />
            </clipPath>
          </defs>
        ) : null}
        <g {...(times ? { clipPath: `url(#${clipId})` } : {})}>
          <g ref={slide}>
            {keys.map((key, i) => {
              const bx = xAt(i);
              const va = a.values[i] ?? 0;
              const vb = b.values[i] ?? 0;
              return (
                <g key={key}>
                  <rect
                    className={motion.grow}
                    style={{ '--i': i } as CSSProperties}
                    x={bx}
                    y={y(va)}
                    width={w}
                    height={Math.max(1, height - y(va))}
                    fill={TONE_VAR[a.tone]}
                  />
                  <rect
                    className={motion.grow}
                    style={{ '--i': i } as CSSProperties}
                    x={bx + w}
                    y={y(vb)}
                    width={w}
                    height={Math.max(1, height - y(vb))}
                    fill={TONE_VAR[b.tone]}
                  />
                </g>
              );
            })}
          </g>
        </g>
      </svg>
      <ChartTable
        id={tableId}
        caption={label}
        head={['Series', 'Latest', 'Peak']}
        rows={[a, b].map((s) => [s.name, format(s.values[s.values.length - 1] ?? 0), format(Math.max(0, ...s.values))])}
      />
    </>
  );
}
