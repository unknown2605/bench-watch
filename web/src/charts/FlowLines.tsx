import { scaleLinear } from 'd3-scale';
import { area, curveMonotoneX, line } from 'd3-shape';
import { useId, useRef } from 'react';
import { useFrameTween } from '../motion/useFrameTween';
import { ChartTable } from './ChartTable';
import { TONE_VAR, type DataTone } from './colors';
import motion from './motion.module.css';
import { morph } from './morph';
import styles from './tabcharts.module.css';
import { medianGap, useTimeScroll } from './useTimeScroll';

export type FlowSeries = { name: string; tone: DataTone; values: readonly (number | null)[] };
type Props = {
  times: readonly number[];
  series: readonly FlowSeries[];
  width: number;
  height: number;
  label: string;
  format: (v: number) => string;
  /** Live window in ms: the chart scrolls with time instead of fitting the samples it has. */
  span?: number | undefined;
};

const LIVE_EVERY_MS = 1_000;
/** Bound on the peak tween's vertical scale, so a peak leaving or reaching near zero cannot blow the chart up. */
const MAX_STRETCH = 4;

/** Overlaid areas on one time axis and one scale; a null leaves a gap. */
export function FlowLines({ times, series, width, height, label, format, span }: Props) {
  const tableId = useId();
  const gradId = useId();
  const clipId = useId();
  const slide = useRef<SVGGElement>(null);
  const stretch = useRef<SVGGElement>(null);
  const pad = 3;
  let max = 0;
  for (const s of series) for (const v of s.values) if (v !== null && v > max) max = v;
  const spanMs = span ?? 0;
  const live = spanMs > 0 && times.length > 0;
  const top = max || 1;
  const base = height - pad;
  // The paths are drawn for the new peak; scaling from the baseline shows the peak still in use, with no path rewrite.
  // No target outside live mode, so a live flip sets the peak at once instead of tweening from or to nothing.
  useFrameTween(live ? [top] : [], ([shown]) => {
    const k = shown ? Math.min(MAX_STRETCH, Math.max(1 / MAX_STRETCH, top / shown)) : 1;
    for (const g of stretch.current?.querySelectorAll('[data-stretch]') ?? []) {
      if (k === 1) g.removeAttribute('transform');
      else g.setAttribute('transform', `translate(0 ${base}) scale(1 ${k}) translate(0 ${-base})`);
    }
  });
  const t0 = times[0] ?? 0;
  const t1 = times[times.length - 1] ?? t0 + 1;
  const x = live
    ? scaleLinear().domain([t1 - spanMs, t1]).range([0, width])
    : scaleLinear().domain([t0, t1 > t0 ? t1 : t0 + 1]).range([0, width]);
  const y = scaleLinear().domain([0, top]).range([base, pad]);
  useTimeScroll(slide, live ? { latest: t1, every: medianGap(times, LIVE_EVERY_MS), pxPerMs: width / spanMs } : null);
  const pts = (s: FlowSeries) => times.map((t, i) => ({ t, v: s.values[i] ?? null }));
  const moving = live ? '' : ` ${motion.morph}`;
  return (
    <>
      <svg className={live ? `${styles.svg} ${styles.liveLayer}` : styles.svg} width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label} aria-describedby={tableId}>
        <defs>
          <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor="currentColor" stopOpacity="0.3" />
            <stop offset="1" stopColor="currentColor" stopOpacity="0" />
          </linearGradient>
          {live ? (
            <clipPath id={clipId}>
              <rect x={0} y={-pad} width={width} height={height + pad * 2} />
            </clipPath>
          ) : null}
        </defs>
        <line className={styles.grid} x1={0} x2={width} y1={height - pad} y2={height - pad} />
        <g {...(live ? { clipPath: `url(#${clipId})` } : {})}>
          <g ref={slide}>
            <g ref={stretch}>
              {series.map((s) => {
                const data = pts(s);
                const stroke = line<{ t: number; v: number | null }>().defined((d) => d.v !== null).x((d) => x(d.t)).y((d) => y(d.v ?? 0)).curve(curveMonotoneX);
                const fill = area<{ t: number; v: number | null }>().defined((d) => d.v !== null).x((d) => x(d.t)).y0(height - pad).y1((d) => y(d.v ?? 0)).curve(curveMonotoneX);
                return (
                  <g key={s.name} color={TONE_VAR[s.tone]}>
                    <g data-stretch="">
                      <path
                        className={`${motion.fade}${moving}`}
                        d={fill(data) ?? ''}
                        style={live ? undefined : morph(fill(data))}
                        fill={`url(#${gradId})`}
                      />
                    </g>
                    {/* The glow sits outside the stretched group, so the peak tween leaves its blur round. */}
                    <g className={styles.glowSoft}>
                      <g data-stretch="">
                        <path
                          className={`${motion.trace}${moving}`}
                          d={stroke(data) ?? ''}
                          style={live ? undefined : morph(stroke(data))}
                          pathLength={1}
                          fill="none"
                          stroke="currentColor"
                          strokeWidth="1.5"
                          vectorEffect="non-scaling-stroke"
                        />
                      </g>
                    </g>
                  </g>
                );
              })}
            </g>
          </g>
        </g>
      </svg>
      <ChartTable
        id={tableId}
        caption={label}
        head={['Series', 'Latest', 'Peak']}
        rows={series.map((s) => {
          const vals = s.values.filter((v): v is number => v !== null);
          return [s.name, format(vals[vals.length - 1] ?? 0), format(Math.max(0, ...vals))];
        })}
      />
    </>
  );
}
