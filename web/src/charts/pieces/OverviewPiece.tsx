import { useId, useRef } from 'react';
import { deltaPct, pct, tokens as fmtTokens } from '../../format/format';
import { useElementSize } from '../../components/useElementSize';
import { arcPath, polar } from './geometry';
import { CountUp, Piece, useActive, vars } from './Piece';
import { OV_BOX, OV_START, OV_TRACK, overviewModel, trendRuns, type OverviewInput } from './overview';
import styles from './pieces.module.css';
import own from './OverviewPiece.module.css';

const TONE = { gold: 'var(--sh-accent-gold)', live: 'var(--sh-status-live)', warn: 'var(--sh-status-warn)' } as const;
const TICKS = 60;
const LINE_H = 56;

function Trend({ values, today }: { values: readonly (number | null)[]; today: number | null }) {
  const ref = useRef<HTMLDivElement>(null);
  const [w] = useElementSize(ref);
  const fill = useId();
  const { runs } = trendRuns(values, Math.max(0, w - 8), LINE_H);
  const last = runs.at(-1)?.at(-1);
  const lastIsToday = last?.slot === values.length - 1;
  return (
    <div className={own.trend}>
      <div ref={ref} className={own.plot}>
        {w > 0 ? (
          <svg width={w} height={LINE_H + 8} aria-hidden="true">
            <defs>
              <linearGradient id={fill} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" stopColor="var(--sh-accent-gold)" stopOpacity="0.28" />
                <stop offset="1" stopColor="var(--sh-accent-gold)" stopOpacity="0" />
              </linearGradient>
            </defs>
            <line className={own.base} x1={0} x2={w} y1={LINE_H + 4} y2={LINE_H + 4} />
            <g transform="translate(4 4)">
              {runs.map((run, i) => {
                const first = run[0];
                const end = run.at(-1);
                if (!first || !end) return null;
                const line = run.map((p, j) => `${j ? 'L' : 'M'} ${p.x} ${p.y}`).join(' ');
                return (
                  <g key={i}>
                    {run.length > 1 ? (
                      <>
                        <path
                          className={`${own.area} ${styles.fadeIn}`}
                          d={`${line} L ${end.x} ${LINE_H} L ${first.x} ${LINE_H} Z`}
                          fill={`url(#${fill})`}
                        />
                        <path className={`${own.line} ${styles.draw}`} d={line} pathLength={1} />
                      </>
                    ) : (
                      <circle className={own.point} cx={first.x} cy={first.y} r={2.5} />
                    )}
                  </g>
                );
              })}
              {last ? (
                <circle
                  className={`${own.todayDot} ${styles.pop}`}
                  cx={last.x}
                  cy={last.y}
                  r={4.5}
                  style={vars({ '--i': 6 })}
                />
              ) : null}
            </g>
          </svg>
        ) : null}
        {lastIsToday && today !== null ? <span className={own.todayTag}>Today · {fmtTokens(today)}</span> : null}
        {!runs.length ? <span className={own.noTrend}>No tokens in the last 14 days</span> : null}
      </div>
      <div className={own.axis}>
        <span>14 d ago</span>
        <span className={own.axisTitle}>Token flow · last 14 d</span>
        <span>Today</span>
      </div>
    </div>
  );
}

export function OverviewPiece({ data, label = 'Token overview' }: { data: OverviewInput; label?: string }) {
  const m = overviewModel(data);
  const { on } = useActive();
  const c = OV_BOX / 2;
  const glow = useId();
  const delta = data.tokens === null ? null : deltaPct(data.tokens, data.previous, data.days);
  const main = data.mainShare;
  return (
    <Piece name="overview" label={label} state={m.state} className={own.root}>
      <div className={`${styles.drawing} ${own.dial}`} style={{ width: OV_BOX, height: OV_BOX }}>
        <svg
          className={styles.drift}
          width={OV_BOX}
          height={OV_BOX}
          viewBox={`0 0 ${OV_BOX} ${OV_BOX}`}
          aria-hidden="true"
        >
          <g transform={`translate(${c} ${c})`}>
            {Array.from({ length: TICKS }, (_, i) => {
              const a = (360 / TICKS) * i;
              const long = i % 5 === 0;
              const p0 = polar(184, a);
              const p1 = polar(long ? 198 : 192, a);
              return (
                <line
                  key={i}
                  className={long ? own.tickLong : own.tick}
                  x1={p0.x}
                  y1={p0.y}
                  x2={p1.x}
                  y2={p1.y}
                />
              );
            })}
          </g>
        </svg>
        <svg width={OV_BOX} height={OV_BOX} viewBox={`0 0 ${OV_BOX} ${OV_BOX}`} aria-hidden="true">
          <defs>
            <radialGradient id={glow}>
              <stop offset="0.5" stopColor="var(--sh-accent-gold)" stopOpacity="0.1" />
              <stop offset="1" stopColor="var(--sh-accent-gold)" stopOpacity="0" />
            </radialGradient>
          </defs>
          <g transform={`translate(${c} ${c})`}>
            <circle r={150} fill={`url(#${glow})`} />
            <circle className={own.inner} r={118} />
            <circle className={own.innerDash} r={126} />
            {m.arcs.map((a) => (
              <path
                key={`t-${a.key}`}
                className={styles.track}
                d={arcPath(a.r, OV_START, OV_TRACK)}
                strokeWidth={a.key === 'reread' ? 10 : 8}
                strokeLinecap="round"
              />
            ))}
            {m.arcs.map((a, i) => {
              const end = polar(a.r, OV_START + a.sweep);
              return a.sweep > 0 ? (
                <g
                  key={a.key}
                  className={styles.mark}
                  style={vars({ '--c': TONE[a.tone] })}
                  tabIndex={0}
                  role="img"
                  aria-label={`${a.name}: ${pct(a.value ?? 0)}`}
                  {...on(i)}
                >
                  <path
                    className={`${styles.arc} ${styles.draw}`}
                    d={arcPath(a.r, OV_START, a.sweep)}
                    pathLength={1}
                    strokeWidth={a.key === 'reread' ? 10 : 8}
                    style={vars({ '--i': i })}
                  />
                  <circle
                    className={`${own.end} ${styles.pop}`}
                    cx={end.x}
                    cy={end.y}
                    r={3.5}
                    style={vars({ '--i': 8 + i })}
                  />
                </g>
              ) : null;
            })}
          </g>
        </svg>
        <div className={styles.hub}>
          <span className={styles.hubLabel}>Tokens · {data.rangeLabel}</span>
          <CountUp text={data.tokens === null ? '—' : fmtTokens(data.tokens)} className={styles.hubValueL} />
          <span className={styles.hubSub}>{delta ?? '—'}</span>
          <span className={styles.hubSub}>
            {main === null ? 'main — · sub —' : `MAIN ${pct(main)} · SUB ${pct(1 - main)}`}
          </span>
        </div>
        <ul className={own.legend}>
          {[...m.arcs].reverse().map((a) => (
            <li key={a.key} style={vars({ '--c': TONE[a.tone] })} {...on(m.arcs.indexOf(a))}>
              {a.name} {a.value === null ? '—' : pct(a.value)}
            </li>
          ))}
        </ul>
      </div>
      <Trend values={m.trend} today={m.trend.at(-1) ?? null} />
    </Piece>
  );
}
