import { arc } from 'd3-shape';
import { useId } from 'react';
import { pctPoints } from '../../format/format';
import { morph } from '../morph';
import { Reveal } from '../Reveal';
import { CountUp, Piece, useActive, vars } from './Piece';
import { MACHINE_BOX, WEDGE_IN, WEDGE_OUT, machineModel, type MachineInput } from './machine';
import styles from './pieces.module.css';
import own from './MachinePiece.module.css';

const RAD = Math.PI / 180;
const KIND = { performance: 'var(--sh-accent-gold)', efficiency: 'var(--sh-data-session)' } as const;

export function MachinePiece({ data, label = 'CPU cores' }: { data: MachineInput; label?: string }) {
  const m = machineModel(data);
  const { on } = useActive();
  const maskId = useId();
  const c = MACHINE_BOX / 2;
  const pad = m.wedges.length > 1 ? Math.min(2.4, 60 / m.wedges.length) : 0;
  const load = data.load;
  return (
    <Piece name="machine" label={label} state={m.state} className={own.root}>
      <div className={styles.drawing} style={{ width: MACHINE_BOX, height: MACHINE_BOX }}>
        <svg className={styles.drift} width={MACHINE_BOX} height={MACHINE_BOX}
          viewBox={`0 0 ${MACHINE_BOX} ${MACHINE_BOX}`} aria-hidden="true">
          <circle className={own.dots} cx={c} cy={c} r={WEDGE_OUT + 12} />
        </svg>
        <svg width={MACHINE_BOX} height={MACHINE_BOX} viewBox={`0 0 ${MACHINE_BOX} ${MACHINE_BOX}`} aria-hidden="true">
          <defs>
            <Reveal id={maskId} rings={[{ r: (WEDGE_IN + WEDGE_OUT) / 2, width: WEDGE_OUT - WEDGE_IN + 20 }]}
              extent={MACHINE_BOX} />
          </defs>
          <g transform={`translate(${c} ${c})`}>
            {m.wedges.map((w) => (
              <path
                key={`t${w.index}`}
                className={own.wedgeTrack}
                d={arc()({ innerRadius: WEDGE_IN, outerRadius: WEDGE_OUT, startAngle: (w.start + pad / 2) * RAD,
                  endAngle: (w.start + w.sweep - pad / 2) * RAD }) ?? ''}
              />
            ))}
            <g mask={`url(#${maskId})`}>
              {m.wedges.map((w) => {
                const d = arc()({ innerRadius: WEDGE_IN, outerRadius: w.outer, startAngle: (w.start + pad / 2) * RAD,
                  endAngle: (w.start + w.sweep - pad / 2) * RAD }) ?? '';
                return (
                <path
                  key={w.index}
                  className={`${styles.mark} ${styles.slice}`}
                  d={d}
                  style={{ ...vars({ '--c': KIND[w.kind] }), ...morph(d) }}
                  tabIndex={0}
                  role="img"
                  aria-label={`${w.name}: ${pctPoints(w.busy)} busy`}
                  {...on(w.index)}
                />
                );
              })}
            </g>
            <circle className={own.hubOuter} r={WEDGE_IN - 8} />
            <circle className={own.hubInner} r={WEDGE_IN - 16} />
          </g>
        </svg>
        {m.wedges.map((w) => (
          <span key={w.index} className={own.label} style={vars({ left: `${w.label.x}px`, top: `${w.label.y}px`,
            '--i': w.index })} {...on(w.index)}>
            <span className={own.labelName}>{w.name}</span>
            <span>{pctPoints(w.busy)}</span>
          </span>
        ))}
        <div className={`${styles.hub} ${own.hub}`}>
          <span className={styles.hubLabel}>CPU</span>
          <CountUp text={data.cpuBusyPct === null ? '—' : pctPoints(data.cpuBusyPct)} className={styles.hubValue} />
          <span className={`${styles.hubLabel} ${own.cores}`}>{m.cores ? `${m.cores} cores` : 'no cores'}</span>
          <span className={styles.hubSub}>LOAD {load ? load.one.toFixed(2) : '—'}</span>
        </div>
      </div>
      <div className={own.foot}>
        <div className={own.load}>
          <p className={styles.caption}>Load average</p>
          <dl className={own.loadRow}>
            {(['one', 'five', 'fifteen'] as const).map((k, i) => (
              <div key={k}>
                <dd>{load ? load[k].toFixed(2) : '—'}</dd>
                <dt>{['1 min', '5 min', '15 min'][i]}</dt>
              </div>
            ))}
          </dl>
        </div>
        <p className={styles.note}>
          Gold = performance cores
          <br />
          Lilac = efficiency cores
        </p>
      </div>
    </Piece>
  );
}
