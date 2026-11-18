import { useId, type CSSProperties } from 'react';
import { HEALTH_MARK, type Health } from '../components/HealthMark';
import { keyed } from '../app/keys';
import { ChartTable } from './ChartTable';
import motion from './motion.module.css';
import styles from './charts.module.css';

export type HoneyCell = { name: string; value: string; health: Health };
type Props = { cells: readonly HoneyCell[]; columns?: number; radius?: number; label: string };

const SQRT3 = Math.sqrt(3);
const GLYPH = 0.58;
const BASE_SIZE = 11;
const MIN_SIZE = 8;

const MAX_LINES = 3;

/** Greedy wrap after `_` and `-`; a piece longer than a line is cut. */
function wrapName(name: string, chars: number): string[] {
  const lines: string[] = [];
  let line = '';
  for (const piece of name.split(/(?<=[_-])/)) {
    for (let at = 0; at < piece.length; at += chars) {
      const part = piece.slice(at, at + chars);
      if (line && line.length + part.length > chars) {
        lines.push(line);
        line = '';
      }
      line += part;
    }
  }
  return line ? [...lines, line] : lines;
}

/** Fits a name in a hexagon: one line, else two split at a separator, else up to three, the last cut with "…". */
export function fitName(name: string, room: number): { lines: string[]; size: number } {
  const width = (chars: number, size: number) => chars * size * GLYPH;
  if (width(name.length, BASE_SIZE) <= room) return { lines: [name], size: BASE_SIZE };
  const cut = Math.max(name.lastIndexOf('_', name.length - 2), name.lastIndexOf('-', name.length - 2));
  const two = cut > 0 ? [name.slice(0, cut + 1), name.slice(cut + 1)] : [name];
  let lines = two;
  if (width(Math.max(...two.map((l) => l.length)), MIN_SIZE) > room) {
    const chars = Math.floor(room / (MIN_SIZE * GLYPH));
    lines = wrapName(name, chars);
    if (lines.length > MAX_LINES) {
      lines = [...lines.slice(0, MAX_LINES - 1), `${(lines[MAX_LINES - 1] ?? '').slice(0, chars - 1)}…`];
    }
  }
  const longest = Math.max(...lines.map((l) => l.length));
  const size = Math.max(MIN_SIZE, Math.min(BASE_SIZE, room / (longest * GLYPH)));
  return { lines, size: Math.round(size * 10) / 10 };
}

export function Honeycomb({ cells, columns = 5, radius = 40, label }: Props) {
  const h = SQRT3 * radius;
  const rows = Math.ceil(cells.length / columns);
  const width = radius * (1.5 * (columns - 1) + 2) + 2;
  const height = h * (rows + 0.5) + 2;
  const tableId = useId();
  const hex = [0, 1, 2, 3, 4, 5]
    .map((k) => `${(radius * Math.cos((Math.PI / 3) * k)).toFixed(1)},${(radius * Math.sin((Math.PI / 3) * k)).toFixed(1)}`)
    .join(' ');
  return (
    <>
      <svg className={styles.chart} width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label} aria-describedby={tableId}>
        {/* DOM order by key, rank only in the transform: moving a node in the DOM cancels its glide. */}
        {keyed(cells, (c) => c.name)
          .map(([key, cell], i) => ({ key, cell, i }))
          .sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0))
          .map(({ key, cell, i }) => {
          const col = i % columns;
          const row = Math.floor(i / columns);
          const cx = 1 + radius + col * radius * 1.5;
          const cy = 1 + h / 2 + row * h + (col % 2 ? h / 2 : 0);
          const fit = fitName(cell.name, radius * 1.65);
          const first = -2 - (fit.lines.length - 1) * 6;
          return (
            <g key={key} className={styles.hexMove} style={{ transform: `translate(${cx}px, ${cy}px)` }}>
              <g className={`${styles[`hex_${cell.health}`]} ${motion.rise}`} style={{ '--i': i } as CSSProperties}>
                <title>{cell.name}</title>
                <polygon className={styles.hexFace} points={hex} />
                <path
                  className={styles.hexMark}
                  fillRule="evenodd"
                  d={HEALTH_MARK[cell.health]}
                  transform={`translate(-4 ${-26 - (fit.lines.length > 2 ? 4 : 0)})`}
                />
                <text className={styles.hexName} style={{ fontSize: fit.size }} x={0} y={first} textAnchor="middle">
                  {fit.lines.map((line, k) => <tspan key={k} x={0} dy={k === 0 ? 0 : 12}>{line}</tspan>)}
                </text>
                <text className={styles.hexValue} x={0} y={14 + (fit.lines.length - 1) * 6} textAnchor="middle">
                  {cell.value}
                </text>
              </g>
            </g>
          );
          })}
      </svg>
      <ChartTable id={tableId} caption={label} head={['Name', 'Value', 'Health']} rows={cells.map((c) => [c.name, c.value, c.health])} />
    </>
  );
}
