import { forceCenter, forceCollide, forceSimulation, forceX, forceY, type SimulationNodeDatum } from 'd3-force';
import { scaleSqrt } from 'd3-scale';
import { useId, useMemo } from 'react';
import { PlanetDefs, PlanetGlyph, usePlanetIds, type PlanetKind } from '../components/Planet';
import { ChartTable } from './ChartTable';
import { extent } from './extent';
import styles from './charts.module.css';
import { keyed } from '../app/keys';

export type StarNode = { name: string; value: number; kind: PlanetKind };
type Props = { nodes: readonly StarNode[]; width?: number; height?: number; label: string };
type Placed = StarNode & SimulationNodeDatum & { r: number };

export function layoutStars(nodes: readonly StarNode[], width: number, height: number): Placed[] {
  const max = Math.max(extent(nodes.map((n) => n.value))[1], 1);
  const size = scaleSqrt().domain([0, max]).range([30, 64]);
  const placed: Placed[] = nodes.map((n, i) => {
    const a = i * 2.399963;
    const d = 14 * Math.sqrt(i + 1);
    return { ...n, r: size(n.value) / 2, x: width / 2 + Math.cos(a) * d, y: height / 2 + Math.sin(a) * d };
  });
  const sim = forceSimulation(placed)
    .force('collide', forceCollide<Placed>((n) => n.r + 10).strength(1))
    .force('x', forceX<Placed>(width / 2).strength(0.06))
    .force('y', forceY<Placed>(height / 2).strength(0.1))
    .force('center', forceCenter(width / 2, height / 2))
    .stop();
  sim.tick(300);
  for (const n of placed) {
    n.x = Math.max(n.r + 4, Math.min(width - n.r - 4, n.x ?? width / 2));
    n.y = Math.max(n.r + 4, Math.min(height - n.r - 18, n.y ?? height / 2));
  }
  return placed;
}

export function StarMap({ nodes, width = 420, height = 260, label }: Props) {
  const ids = usePlanetIds();
  const placed = useMemo(() => layoutStars(nodes, width, height), [nodes, width, height]);
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
        <PlanetDefs ids={ids} />
        <ellipse className={styles.hairline} cx={width / 2} cy={height / 2} rx={width * 0.42} ry={height * 0.32} />
        <ellipse className={styles.hairline} cx={width / 2} cy={height / 2} rx={width * 0.28} ry={height * 0.2} />
        {keyed(placed, (n) => n.name).map(([key, n]) => (
          <g key={key}>
            <PlanetGlyph kind={n.kind} cx={n.x ?? 0} cy={n.y ?? 0} size={n.r * 2} ids={ids} />
            <text className={styles.caption} x={n.x ?? 0} y={(n.y ?? 0) + n.r + 12} textAnchor="middle">
              {n.name}
            </text>
          </g>
        ))}
      </svg>
      <ChartTable
        id={tableId}
        caption={label}
        head={['Name', 'Value', 'State']}
        rows={nodes.map((n) => [n.name, n.value, n.kind])}
      />
    </>
  );
}
