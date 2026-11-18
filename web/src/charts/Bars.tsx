import { scaleBand, scaleLinear } from 'd3-scale';
import { useId, type CSSProperties } from 'react';
import { ChartTable } from './ChartTable';
import { TONE_VAR, type DataTone } from './colors';
import motion from './motion.module.css';
import styles from './tabcharts.module.css';

export type BarDatum = { key: string; label: string; value: number; display: string;
  /** No measurement for this day: drawn as a dashed stub, unlike a measured zero. */
  absent?: boolean;
  /** Part of the value drawn in coral at the top of the bar, such as the denied calls of a day. */
  cap?: number;
};
type Props = {
  data: readonly BarDatum[];
  width: number;
  height: number;
  tone?: DataTone;
  /** Draws the last bar as an open, dashed slot: a day that is still running. */
  lastOpen?: boolean;
  average?: { value: number; label: string } | null;
  /** The last bar is today: drawn bright with a gold marker above it. */
  today?: boolean;
  /** One flag per bar: a small dot under it when true. */
  dots?: readonly boolean[] | null;
  peak?: { index: number; label: string } | null;
  /** A row under the bars: the first day, an optional note in the middle and the last day (teal when `now`). */
  axis?: { start: string; end: string; note?: string; now?: boolean } | null;
  label: string;
};

export function Bars({
  data, width, height, tone = 'gold', lastOpen = false, today = false, average = null, peak = null, dots = null,
  axis = null, label,
}: Props) {
  const tableId = useId();
  const gradId = useId();
  const top = peak || today ? 16 : 2;
  const dotRoom = dots ? 10 : 0;
  let max = 0;
  for (const d of data) if (d.value > max) max = d.value;
  const x = scaleBand<string>().domain(data.map((d) => d.key)).range([0, width]).paddingInner(0.32).paddingOuter(0.1);
  const y = scaleLinear().domain([0, max || 1]).range([height - dotRoom, top]);
  const color = TONE_VAR[tone];
  const base = height - dotRoom;
  const last = data.length - 1;
  const lastBar = data[last];
  return (
    <>
      <svg className={styles.svg} width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img"
        aria-label={label} aria-describedby={tableId}>
        <defs>
          <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor={color} stopOpacity="0.95" />
            <stop offset="1" stopColor={color} stopOpacity="0.35" />
          </linearGradient>
        </defs>
        {data.map((d, i) => {
          const bx = x(d.key) ?? 0;
          const bw = x.bandwidth();
          const by = y(d.value);
          if (lastOpen && i === last) {
            const slot = Math.max(base - by, 18);
            const cls = `${styles.today} ${motion.fade}`;
            return <rect key={d.key} className={cls} x={bx} y={base - slot} width={bw} height={slot} />;
          }
          if (d.absent) return <rect key={d.key} className={styles.absentDay} x={bx} y={base - 3} width={bw}
            height={3} />;
          if (d.value <= 0) return <rect key={d.key} className={styles.emptyDay} x={bx} y={base - 2} width={bw}
            height={2} />;
          const capH = d.cap && d.cap > 0 ? Math.min(Math.max(2, base - y(d.cap)), base - by) : 0;
          const isPeak = peak?.index === i;
          const isToday = today && i === last;
          return (
            <g key={d.key}>
            <rect
              className={isPeak || isToday ? `${styles.glow} ${motion.grow}` : motion.grow}
              style={{ '--i': i } as CSSProperties}
              color={color}
              fill={isPeak || isToday ? 'currentColor' : `url(#${gradId})`}
              x={bx}
              y={by}
              width={bw}
              height={Math.max(0, base - by)}
            />
            {capH > 0 ? <rect className={styles.cap} x={bx} y={by} width={bw} height={capH} /> : null}
            </g>
          );
        })}
        {today && lastBar ? (
          <path
            className={styles.todayMark}
            d={`M${(x(lastBar.key) ?? 0) + x.bandwidth() / 2 - 3.5} ${y(lastBar.value) - 7}h7l-3.5 5z`}
          />
        ) : null}
        {average && max > 0 ? (
          <g>
            <line className={styles.avg} x1={0} x2={width} y1={y(average.value)} y2={y(average.value)} />
            <text className={styles.axis} x={2} y={y(average.value) - 5}>{average.label}</text>
          </g>
        ) : null}
        {peak && peak.index >= 0 && data[peak.index] ? (
          <text className={styles.peakLabel} x={(x(data[peak.index]?.key ?? '') ?? 0) + x.bandwidth() / 2} y={10}
            textAnchor="end">
            {peak.label}
          </text>
        ) : null}
        {dots
          ? data.map((d, i) => (dots[i] ? <circle key={`dot-${d.key}`} className={styles.dot2}
            cx={(x(d.key) ?? 0) + x.bandwidth() / 2} cy={height - 3} r={1.8} /> : null))
          : null}
      </svg>
      {axis ? (
        <div className={styles.barAxis} style={{ width }} aria-hidden="true">
          <span>{axis.start.toUpperCase()}</span>
          <span>{axis.note?.toUpperCase()}</span>
          <span className={axis.now ? styles.barAxisNow : undefined}>{axis.end.toUpperCase()}</span>
        </div>
      ) : null}
      <ChartTable
        id={tableId}
        caption={label}
        head={dots ? ['Item', 'Value', 'Check passed'] : ['Item', 'Value']}
        rows={data.map((d, i) => {
          const name = today && i === last ? `${d.label} (today)` : d.label;
          return dots ? [name, d.display, dots[i] ? 'yes' : 'no'] : [name, d.display];
        })}
      />
    </>
  );
}
