import { useId } from 'react';
import { keyed } from '../../app/keys';
import { int, pct1 } from '../../format/format';
import { arcPath } from './geometry';
import { CountUp, Piece, RowButton, rowSelected, selectable, useActive, vars, type Selection } from './Piece';
import { SPOKES, SUB_BOX, TIP_R, subagentsModel, type SubagentsInput } from './subagents';
import styles from './pieces.module.css';
import own from './SubagentsPiece.module.css';

type Props = Selection & { data: SubagentsInput; label?: string };

export function SubagentsPiece({ data, label = 'Sub-agent types', selected, onSelect }: Props) {
  const m = subagentsModel(data);
  const { on } = useActive();
  const glow = useId();
  const c = SUB_BOX / 2;
  const used = m.spokes.filter((s) => s.item);
  const sel = { selected, onSelect };
  const idOf = (it: { key?: string | undefined; name: string }) => it.key ?? it.name;
  const marks = onSelect ? { role: 'group', 'aria-label': 'Sub-agent spokes' } : { 'aria-hidden': true };
  return (
    <Piece name="subagents" label={label} state={m.state} className={own.root}>
      <div className={styles.drawing} style={{ width: SUB_BOX, height: SUB_BOX }}>
        <svg
          className={`${styles.drift} ${styles.driftBack}`}
          width={SUB_BOX}
          height={SUB_BOX}
          viewBox={`0 0 ${SUB_BOX} ${SUB_BOX}`}
          aria-hidden="true"
        >
          <circle className={styles.dashRing} cx={c} cy={c} r={86} />
        </svg>
        <svg width={SUB_BOX} height={SUB_BOX} viewBox={`0 0 ${SUB_BOX} ${SUB_BOX}`} {...marks}>
          <defs>
            <radialGradient id={glow}>
              <stop offset="0.6" stopColor="var(--sh-accent-gold)" stopOpacity="0.18" />
              <stop offset="1" stopColor="var(--sh-accent-gold)" stopOpacity="0" />
            </radialGradient>
          </defs>
          <circle cx={c} cy={c} r={96} fill={`url(#${glow})`} />
          <circle className={own.hub} cx={c} cy={c} r={64} />
          {m.mainShare !== null && m.mainShare > 0 ? (
            <path
              className={`${own.mainArc} ${styles.draw}`}
              d={arcPath(72, 0, m.mainShare * 360)}
              transform={`translate(${c} ${c})`}
              pathLength={1}
            />
          ) : null}
          {m.spokes.map((s) =>
            s.item ? (
              <g
                key={s.index}
                className={`${styles.mark} ${styles.growOut} ${own.fromCentre}`}
                style={vars({ '--i': s.index, '--c': 'var(--sh-status-live)' })}
                tabIndex={0}
                role="img"
                aria-label={`Rank ${s.item.rank}: ${s.item.name}, ${int(s.item.runs)} runs`}
                {...on(s.index)}
                {...selectable(sel, idOf(s.item))}
              >
                <line
                  className={own.spoke}
                  x1={s.start.x}
                  y1={s.start.y}
                  x2={s.end.x}
                  y2={s.end.y}
                  style={{ opacity: s.opacity }}
                />
                <circle className={own.tip} cx={s.tip.x} cy={s.tip.y} r={TIP_R} />
                <text className={styles.rankText} x={s.tip.x} y={s.tip.y}>{s.item.rank}</text>
              </g>
            ) : (
              <g key={s.index} className={own.unused}>
                <line x1={s.start.x} y1={s.start.y} x2={s.end.x} y2={s.end.y} />
                <circle cx={s.tip.x} cy={s.tip.y} r={TIP_R - 3} />
              </g>
            ),
          )}
        </svg>
        <div className={styles.hub}>
          <span className={styles.hubLabel}>Main session</span>
          <CountUp text={m.total ? int(m.total) : '—'} className={styles.hubValue} />
          <span className={styles.hubLabel}>Runs · {data.rangeLabel}</span>
        </div>
      </div>
      <div className={own.side}>
        <p className={styles.caption}>Sub-agent types</p>
        {used.length ? (
          <ul className={styles.list}>
            {keyed(used, (s) => s.item?.name ?? '').map(([key, s]) => {
              const it = s.item;
              if (!it) return null;
              return (
                <li
                  key={key}
                  className={`${styles.row} ${own.row}`}
                  style={vars({ '--i': s.index, '--c': 'var(--sh-accent-gold)' })}
                  title={it.name}
                  {...on(s.index)}
                  {...rowSelected(sel, idOf(it))}
                >
                  <RowButton sel={sel} id={idOf(it)}>
                    <span className={styles.rank}>{it.rank}</span>
                    <span className={styles.name}>{it.name}</span>
                    <span className={styles.num}>{int(it.runs)}</span>
                    <span className={`${styles.num} ${styles.dim}`}>{pct1(it.share)}</span>
                  </RowButton>
                </li>
              );
            })}
            {m.other ? (
              <li
                className={`${styles.row} ${own.row}`}
                style={vars({ '--i': SPOKES })}
                title={`${m.other.types} more types`}
              >
                <span className={styles.rank}>+</span>
                <span className={styles.name}>other · {m.other.types}</span>
                <span className={styles.num}>{int(m.other.runs)}</span>
                <span className={`${styles.num} ${styles.dim}`}>{pct1(m.other.share)}</span>
              </li>
            ) : null}
          </ul>
        ) : (
          <p className={styles.empty}>No sub-agent runs in this range</p>
        )}
      </div>
    </Piece>
  );
}
