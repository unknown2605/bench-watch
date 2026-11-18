import { arc } from 'd3-shape';
import { scaleLinear } from 'd3-scale';
import { useId } from 'react';
import { ChartTable, percent } from './ChartTable';
import { TONE_VAR, type DataTone } from './colors';
import motion from './motion.module.css';
import { morph } from './morph';
import { Reveal } from './Reveal';
import styles from './charts.module.css';

export type RingSpec = { name: string; value: number; tone: DataTone };
type RingProps = { rings: readonly RingSpec[]; size?: number; thickness?: number; gap?: number; sweep?: number; ticks?: number; label: string };

const TAU = Math.PI * 2;

export function RingChart({ rings, size = 360, thickness = 12, gap = 6, sweep = 0.78, ticks = 36, label }: RingProps) {
  const c = size / 2;
  const outer = c - 36;
  const start = -TAU * sweep * 0.5;
  const track = arc();
  const tableId = useId();
  const maskId = useId();
  const tickLen = scaleLinear().domain([0, 1]).range([6, 18]);
  return (
    <>
      <svg className={styles.chart} width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img" aria-label={label} aria-describedby={tableId}>
        <defs>
          <Reveal
            id={maskId}
            rings={rings.map((_, i) => ({ r: outer - i * (thickness + gap) - thickness / 2, width: thickness + gap }))}
            start={start}
            sweep={TAU * sweep}
            extent={size}
          />
        </defs>
        <g transform={`translate(${c} ${c})`}>
          {Array.from({ length: ticks }, (_, i) => {
            const a = start + (i / (ticks - 1)) * TAU * sweep;
            const r1 = outer + 14;
            const r2 = r1 + tickLen(i % 3 === 0 ? 1 : 0);
            return (
              <line key={i} className={styles.tick} x1={Math.sin(a) * r1} y1={-Math.cos(a) * r1} x2={Math.sin(a) * r2} y2={-Math.cos(a) * r2} />
            );
          })}
          {rings.map((ring, i) => {
            const r1 = outer - i * (thickness + gap);
            const r0 = r1 - thickness;
            const end = start + TAU * sweep * Math.max(0, Math.min(1, ring.value));
            const trackPath = track({ innerRadius: r0, outerRadius: r1, startAngle: start, endAngle: start + TAU * sweep });
            const valuePath = arc()({ innerRadius: r0, outerRadius: r1, startAngle: start, endAngle: end });
            return (
              <g key={`${i}-${ring.name}`} mask={`url(#${maskId})`}>
                <path className={styles.track} d={trackPath ?? ''} />
                <path
                  className={`${styles.glow} ${motion.morph}`}
                  d={valuePath ?? ''}
                  style={morph(valuePath)}
                  fill="currentColor"
                  color={TONE_VAR[ring.tone]}
                />
              </g>
            );
          })}
        </g>
      </svg>
      <ChartTable id={tableId} caption={label} head={['Ring', 'Value']} rows={rings.map((r) => [r.name, percent(r.value)])} />
    </>
  );
}

type CoreRingProps = { values: readonly number[]; size?: number; label: string; efficiencyCores?: number };

export function CoreRing({ values, size = 220, label, efficiencyCores = 0 }: CoreRingProps) {
  const c = size / 2;
  const inner = c * 0.45;
  const outer = c - 6;
  const len = scaleLinear().domain([0, 1]).range([2, outer - inner]);
  const step = TAU / Math.max(values.length, 1);
  const tableId = useId();
  return (
    <>
      <svg className={styles.chart} width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img" aria-label={label} aria-describedby={tableId}>
        <g transform={`translate(${c} ${c})`}>
          <circle className={styles.hairline} r={inner - 4} />
          {values.map((v, i) => {
            const a0 = i * step + step * 0.12;
            const a1 = (i + 1) * step - step * 0.12;
            const path = arc()({ innerRadius: inner, outerRadius: inner + len(Math.max(0, Math.min(1, v))), startAngle: a0, endAngle: a1 });
            return <path key={i} className={styles.glow} d={path ?? ''} fill="currentColor" color={i < efficiencyCores ? TONE_VAR.session : TONE_VAR.gold} />;
          })}
        </g>
      </svg>
      <ChartTable id={tableId} caption={label} head={['Core', 'Load']} rows={values.map((v, i) => [`Core ${i + 1}`, percent(v)])} />
    </>
  );
}
