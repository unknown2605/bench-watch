import { scaleLinear } from 'd3-scale';
import { area, curveMonotoneX, line } from 'd3-shape';
import { useId } from 'react';
import { keyed } from '../app/keys';
import { ChartTable } from './ChartTable';
import { MarkerShape, type MarkerKind } from '../components/Marker';
import { TONE_VAR, type DataTone } from './colors';
import motion from './motion.module.css';
import { morph } from './morph';
import styles from './charts.module.css';
import own from './LineArea.module.css';

export type Point = { x: number; y: number };
export type LineMarker = { x: number; kind: MarkerKind; name: string };
type Props = {
  data: readonly Point[];
  markers?: readonly LineMarker[];
  tone?: DataTone;
  width?: number;
  height?: number;
  yTicks?: number;
  /** The last point is today: a gold dot with a TODAY mark. */
  today?: boolean;
  formatY?: (v: number) => string;
  /** Names for the x values, used by the accessible table in place of the raw number. */
  xLabels?: readonly string[];
  /** A dashed reference line, e.g. the context limit; the y scale always reaches it. */
  limit?: { value: number; label: string } | null;
  /** Pixels between a marker and its point; 0 centres the marker on the line. */
  markerLift?: number;
  todayLabel?: boolean;
  label: string;
};

export function LineArea({
  data, markers = [], tone = 'gold', width = 640, height = 200, yTicks = 3, today = false, formatY = String, xLabels,
  limit = null, markerLift = 0, todayLabel = true, label,
}: Props) {
  const id = useId();
  const tableId = useId();
  const color = TONE_VAR[tone];
  const pad = { l: yTicks > 0 ? 44 : 12, r: 12, t: 12 + markerLift, b: 12 };
  let xMin = Infinity;
  let xMax = -Infinity;
  let yMax = 1;
  for (const d of data) {
    if (d.x < xMin) xMin = d.x;
    if (d.x > xMax) xMax = d.x;
    if (d.y > yMax) yMax = d.y;
  }
  if (limit && limit.value > yMax) yMax = limit.value;
  const x = scaleLinear().domain([xMin, xMax]).range([pad.l, width - pad.r]);
  const y = scaleLinear().domain([0, yMax]).nice().range([height - pad.b, pad.t]);
  const lineGen = line<Point>().x((d) => x(d.x)).y((d) => y(d.y)).curve(curveMonotoneX);
  const areaGen = area<Point>().x((d) => x(d.x)).y0(height - pad.b).y1((d) => y(d.y)).curve(curveMonotoneX);
  const nearest = (mx: number) =>
    data.reduce((best, d) => (Math.abs(d.x - mx) < Math.abs(best.x - mx) ? d : best), data[0] ?? { x: mx, y: 0 });
  const newest = data[data.length - 1];
  const markerAt = new Map<Point, string[]>();
  for (const m of markers) {
    const p = nearest(m.x);
    const names = markerAt.get(p);
    if (names) names.push(m.name);
    else markerAt.set(p, [m.name]);
  }
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
        <defs>
          <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor={color} stopOpacity="0.28" />
            <stop offset="1" stopColor={color} stopOpacity="0" />
          </linearGradient>
        </defs>
        {(yTicks > 0 ? y.ticks(yTicks) : []).map((t) => (
          <g key={`tick-${t}`}>
            <line className={styles.grid} x1={pad.l} x2={width - pad.r} y1={y(t)} y2={y(t)} />
            <text className={styles.axis} x={pad.l - 8} y={y(t) + 3} textAnchor="end">{formatY(t)}</text>
          </g>
        ))}
        {limit ? (
          <g className={own.limit}>
            <line x1={pad.l} x2={width - pad.r} y1={y(limit.value)} y2={y(limit.value)} />
            <text x={pad.l} y={y(limit.value) + 10}>{limit.label}</text>
          </g>
        ) : null}
        <path
          className={`${motion.fade} ${motion.morph}`}
          d={areaGen([...data]) ?? ''}
          style={morph(areaGen([...data]))}
          fill={`url(#${id})`}
        />
        <path
          className={`${styles.glowSoft} ${motion.trace} ${motion.morph}`}
          d={lineGen([...data]) ?? ''}
          style={morph(lineGen([...data]))}
          pathLength={1}
          fill="none"
          stroke={color}
          strokeWidth="1.5"
        />
        {data.length === 1 && data[0] ? <circle className={styles.glowSoft} cx={x(data[0].x)} cy={y(data[0].y)} r={3}
          fill={color} /> : null}
        {today && data.length > 1 && newest ? (
          <g className={styles.todayMark}>
            <circle cx={x(newest.x)} cy={y(newest.y)} r={3.5} />
            {todayLabel ? (
              <text x={x(newest.x) - 7} y={Math.max(9, y(newest.y) - 6)} textAnchor="end">TODAY</text>
            ) : null}
          </g>
        ) : null}
        {keyed(markers, (m) => `${m.kind}-${m.x}`).map(([key, m]) => {
          const p = nearest(m.x);
          return (
            <g
              key={key}
              transform={`translate(${x(p.x) - 8} ${y(p.y) - 8 - markerLift})`}
              className={styles.markerGlow}
            >
              <MarkerShape kind={m.kind} />
            </g>
          );
        })}
      </svg>
      <ChartTable
        id={tableId}
        caption={label}
        head={[xLabels ? 'Day' : 'X', 'Value', 'Markers']}
        rows={data.map((d, i) => {
          const notes = [today && i === data.length - 1 ? 'today' : '', ...(markerAt.get(d) ?? [])];
          return [xLabels?.[d.x] ?? d.x, formatY(d.y), notes.filter(Boolean).join(', ')];
        })}
      />
    </>
  );
}
