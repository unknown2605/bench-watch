import { useId } from 'react';
import { ChartTable } from '../ChartTable';
import { TONE_COLOR } from './SessionsPiece';
import { sessionHours, type HourSpan } from './sessions';
import own from './SessionsPieceHours.module.css';

type Props = {
  spans: readonly HourSpan[];
  marks: readonly number[];
  projects: readonly string[];
  dayStart: number;
  now: number;
  width: number;
  height: number;
  label: string;
};

const PAD = { left: 22, right: 8, top: 6, bottom: 18 };
const MAX_DOTS = 4;
const TICKS = [0, 6, 12, 18];
const two = (n: number) => String(n).padStart(2, '0');

export function SessionsPieceHours({ spans, marks, projects, dayStart, now, width, height, label }: Props) {
  const m = sessionHours(spans, marks, projects, dayStart, now);
  const id = useId();
  const plotW = Math.max(0, width - PAD.left - PAD.right);
  const plotH = Math.max(0, height - PAD.top - PAD.bottom);
  const slot = plotW / 24;
  const barW = Math.max(2, slot * 0.64);
  const base = PAD.top + plotH;
  const y = (v: number) => base - (v / m.top) * plotH;
  const xOf = (h: number) => PAD.left + h * slot + (slot - barW) / 2;
  const nowX = xOf(m.nowHour) + barW / 2;
  return (
    <div className={own.root}>
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={label}
        aria-describedby={id}
      >
        {[m.top, m.top / 2].map((v) => (
          <line key={v} className={own.grid} x1={PAD.left} x2={width - PAD.right} y1={y(v)} y2={y(v)} />
        ))}
        <line className={own.base} x1={PAD.left} x2={width - PAD.right} y1={base} y2={base} />
        {[m.top, m.top / 2, 0].map((v) => (
          <text key={v} className={own.axis} x={PAD.left - 8} y={y(v) + 3} textAnchor="end">{v}</text>
        ))}
        {m.bars.map((b) => {
          const x = xOf(b.hour);
          if (b.future) {
            return <line key={b.hour} className={own.future} x1={x} x2={x + barW} y1={base - 1} y2={base - 1} />;
          }
          let acc = 0;
          return (
            <g key={b.hour}>
              {b.stack.map((p) => {
                const top = y(acc + p.count);
                const h = y(acc) - top;
                acc += p.count;
                return (
                  <rect
                    key={p.tone}
                    x={x}
                    y={top + 1}
                    width={barW}
                    height={Math.max(0, h - 2)}
                    fill={TONE_COLOR[p.tone]}
                  />
                );
              })}
              {Array.from({ length: Math.min(MAX_DOTS, b.marks) }, (_, i) => (
                <circle
                  key={i}
                  className={own.mark}
                  r={2}
                  cx={x + (barW * (i + 1)) / (Math.min(MAX_DOTS, b.marks) + 1)}
                  cy={y(b.total) - 1}
                />
              ))}
            </g>
          );
        })}
        <line className={own.nowLine} x1={nowX} x2={nowX} y1={PAD.top - 4} y2={base + 4} />
        {TICKS.filter((h) => Math.abs(h - m.nowHour) > 1).map((h) => (
          <text key={h} className={own.axis} x={xOf(h) + barW / 2} y={height - 4} textAnchor="middle">{two(h)}</text>
        ))}
        <text className={own.now} x={nowX} y={height - 4} textAnchor="middle">NOW</text>
      </svg>
      <p className={own.foot}>colour = project · dot = compaction</p>
      <ChartTable
        id={id}
        caption={label}
        head={['Hour', 'Active sessions', 'Compactions']}
        rows={m.bars.filter((b) => !b.future).map((b) => [`${two(b.hour)}:00`, b.total, b.marks])}
      />
    </div>
  );
}
