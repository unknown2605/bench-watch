import { int, pct } from '../../format/format';
import { arcPath } from './geometry';
import { CountUp, Piece, useActive, vars } from './Piece';
import { GUARD_BOX, guardModel, type GuardInput } from './guard';
import a11y from '../../styles/a11y.module.css';
import styles from './pieces.module.css';
import own from './GuardPiece.module.css';

const R = 150;
const W = 16;
const SHIELD = 'M 0 -112 L 92 -78 L 84 34 L 0 112 L -84 34 L -92 -78 Z';
const SHIELD_IN = 'M 0 -100 L 80 -70 L 73 29 L 0 98 L -73 29 L -80 -70 Z';

const hh = (h: number) => String(h).padStart(2, '0');
const VERDICT: Record<'true' | 'false' | 'null', string> = {
  true: 'Guard installed', false: 'No guard found', null: 'Guard state unknown',
};

export function GuardPiece({ data, label = 'Guard hours today' }: { data: GuardInput; label?: string }) {
  const m = guardModel(data);
  const { on } = useActive();
  const c = GUARD_BOX / 2;
  const n = m.segments.length;
  const gap = 2;
  const step = 360 / n;
  const calls = (v: number | null) => (v === null ? '—' : `${int(v)} calls`);
  const stat = (title: string, value: string, cls: string | undefined) => (
    <div className={`${own.stat} ${cls ?? ''}`}>
      <p className={own.statTitle}>{title}</p>
      <p className={own.statValue}>{value}</p>
    </div>
  );
  return (
    <Piece name="guard" label={label} state={m.state} className={own.root}>
      {stat('Bash', calls(data.bash), own.tl)}
      {stat('File', calls(data.file), own.tr)}
      {stat('Sub-agents', data.subShare === null ? '—' : `${pct(data.subShare)} of calls`, own.bl)}
      {stat('Last block', data.lastBlock ?? '—', own.br)}
      <div className={`${styles.drawing} ${own.draw}`} style={{ width: GUARD_BOX, height: GUARD_BOX }}>
        <svg
          className={styles.drift}
          width={GUARD_BOX}
          height={GUARD_BOX}
          viewBox={`0 0 ${GUARD_BOX} ${GUARD_BOX}`}
          aria-hidden="true"
        >
          <circle className={own.dots} cx={c} cy={c} r={R + 20} />
        </svg>
        <svg
          width={GUARD_BOX}
          height={GUARD_BOX}
          viewBox={`0 0 ${GUARD_BOX} ${GUARD_BOX}`}
          {...(m.waiting ? { 'aria-hidden': true } : { role: 'group', 'aria-label': 'Guard calls by hour' })}
        >
          <g transform={`translate(${c} ${c})`}>
            <circle className={styles.track} r={R} strokeWidth={W} />
            {m.segments.map((s) => (
              <g
                key={s.index}
                className={`${styles.mark} ${own[s.status]}`}
                {...(m.waiting
                  ? { 'aria-hidden': true }
                  : {
                    tabIndex: 0,
                    role: 'img',
                    'aria-label': `${hh(s.hour)}:00: ${int(s.calls)} calls, ${int(s.blocked)} blocked`,
                    ...on(s.index),
                  })}
              >
                <path
                  className={`${own.seg} ${styles.draw}`}
                  d={arcPath(R, s.index * step + gap / 2, step - gap)}
                  pathLength={1}
                  strokeWidth={W}
                  style={vars({ '--i': Math.min(s.index, 20) * 0.4 })}
                />
              </g>
            ))}
            <path className={`${own.shield} ${styles.fadeIn}`} d={SHIELD} />
            <path className={own.shieldIn} d={SHIELD_IN} />
          </g>
        </svg>
        <div className={`${styles.hub} ${own.hub}`}>
          <span className={`${styles.hubLabel} ${own.hubTitle}`}>Blocked</span>
          <CountUp text={data.blocked === null ? '—' : int(data.blocked)} className={styles.hubValueL} />
          <span className={own.verdict}>{VERDICT[`${data.active}` as keyof typeof VERDICT]}</span>
          {data.version ? <span className={styles.hubSub}>{data.version}</span> : null}
        </div>
      </div>
      <div className={own.legend}>
        <div className={own.status}>
          {data.mode ? (
            <p className={styles.note}>
              {`Guard mode ${data.mode}`}
              <span className={a11y.hidden}>. </span>
              <br />
              Configured in the mode file · not proof of what ran
            </p>
          ) : null}
          {data.log ? (
            <p className={data.log.warn ? `${styles.note} ${own.logWarn}` : styles.note} data-log-status>
              {data.log.text}
              {data.log.skipped ? (
                <>
                  <span className={a11y.hidden}>. </span>
                  <br />
                  {data.log.skipped}
                </>
              ) : null}
            </p>
          ) : null}
        </div>
        <p className={`${styles.note} ${own.key}`}>
          {m.waiting ? (
            'No hourly data'
          ) : (
            <>
              Each segment = one hour of today
              <br />
              Teal = calls · Coral = a block
            </>
          )}
        </p>
      </div>
    </Piece>
  );
}
