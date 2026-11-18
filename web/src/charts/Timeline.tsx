import { useId, type CSSProperties } from 'react';
import { MarkerShape } from '../components/Marker';
import { clock } from '../format/format';
import { timeFraction } from '../format/scale';
import { ChartTable } from './ChartTable';
import { hourTicks } from './layout';
import motion from './motion.module.css';
import styles from './tabcharts.module.css';

export type Span = { row: number; start: string; end: string; live: boolean; name: string };
export type Mark = { row: number; at: string; name: string };
type Props = { rows: number; spans: readonly Span[]; marks: readonly Mark[]; start: number; end: number; width: number; height: number; label: string };

export function Timeline({ rows, spans, marks, start, end, width, height, label }: Props) {
  const tableId = useId();
  const left = 30;
  const right = 12;
  const axis = 16;
  const plotW = width - left - right;
  const rowH = rows > 0 ? (height - axis - 4) / rows : 0;
  const xAt = (iso: string) => left + timeFraction(iso, start, end) * plotW;
  const rowY = (r: number) => 4 + r * rowH + rowH / 2;
  return (
    <>
      <svg className={styles.svg} width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label} aria-describedby={tableId}>
        {Array.from({ length: rows }, (_, r) => (
          <g key={r}>
            <line className={styles.grid} x1={left} x2={width - right} y1={rowY(r)} y2={rowY(r)} />
            <text className={styles.axis} x={0} y={rowY(r) + 3}>{String(r + 1).padStart(2, '0')}</text>
          </g>
        ))}
        {spans.map((s, i) => {
          const x0 = xAt(s.start);
          const x1 = Math.max(xAt(s.end), x0 + 3);
          const cls = `${s.live ? styles.barLive : styles.bar} ${motion.growX}`;
          return <rect key={i} className={cls} style={{ '--i': i } as CSSProperties} x={x0} y={rowY(s.row) - 4} width={x1 - x0} height={8} />;
        })}
        {marks.map((m, i) => (
          <g key={`m-${i}`} className={styles.star} transform={`translate(${xAt(m.at) - 6} ${rowY(m.row) - 6}) scale(0.75)`}>
            <MarkerShape kind="event" />
          </g>
        ))}
        <line stroke="var(--sh-status-live)" strokeWidth={1} x1={width - right} x2={width - right} y1={2} y2={height - axis} />
        {hourTicks(start, end, 6).map((t) =>
          t.at < 1 - 48 / plotW ? (
            <text key={t.label} className={styles.axis} x={left + t.at * plotW} y={height - 2} textAnchor="middle">{t.label}</text>
          ) : null,
        )}
        <text className={styles.axisNow} x={width - right} y={height - 2} textAnchor="end">NOW</text>
      </svg>
      <ChartTable
        id={tableId}
        caption={label}
        head={['Row', 'Item', 'From', 'To']}
        rows={[
          ...spans.map((s) => [String(s.row + 1), s.name, clock(s.start), s.live ? 'now' : clock(s.end)]),
          ...marks.map((m) => [String(m.row + 1), m.name, clock(m.at), '']),
        ]}
      />
    </>
  );
}
