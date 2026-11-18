import { useId } from 'react';
import { bandLabel, int, pct1, tokens as fmtTokens } from '../../format/format';
import { arcPath } from './geometry';
import { CountUp, Piece, useActive, vars } from './Piece';
import { CONTEXT_LIMIT, CTX_BOX, CTX_HUB_R, CTX_STROKE, contextModel, ringRadius, type ContextInput } from './context';
import styles from './pieces.module.css';
import own from './ContextPiece.module.css';

const TONE = { live: 'var(--sh-status-live)', warn: 'var(--sh-status-warn)' } as const;

export function ContextPiece({ data, label = 'Context bands' }: { data: ContextInput; label?: string }) {
  const m = contextModel(data);
  const { on } = useActive();
  const glow = useId();
  const c = CTX_BOX / 2;
  const limit = data.limit ?? CONTEXT_LIMIT;
  const avg = m.avg === null || m.totalCalls === 0 ? '—' : fmtTokens(m.avg);
  return (
    <Piece name="context" label={label} state={m.state} className={own.root}>
      <header className={own.head}>
        <p className={styles.title}>Context bands · {data.rangeLabel}</p>
        <p className={styles.caption}>
          {int(m.totalCalls)} calls · {int(data.compactions)} compactions · avg {avg} / call
        </p>
      </header>
      <div className={own.body}>
        <div className={styles.drawing} style={{ width: CTX_BOX, height: CTX_BOX }}>
          <svg
            className={styles.drift}
            width={CTX_BOX}
            height={CTX_BOX}
            viewBox={`0 0 ${CTX_BOX} ${CTX_BOX}`}
            aria-hidden="true"
          >
            <circle className={own.decor} cx={c} cy={c} r={ringRadius(4) + 22} />
            <circle className={own.decorDash} cx={c} cy={c} r={ringRadius(4) + 38} />
          </svg>
          <svg width={CTX_BOX} height={CTX_BOX} viewBox={`0 0 ${CTX_BOX} ${CTX_BOX}`} aria-hidden="true">
            <defs>
              <radialGradient id={glow}>
                <stop offset="0.55" stopColor="var(--sh-accent-gold)" stopOpacity="0.2" />
                <stop offset="1" stopColor="var(--sh-accent-gold)" stopOpacity="0" />
              </radialGradient>
            </defs>
            <g transform={`translate(${c} ${c})`}>
              <circle r={CTX_HUB_R + 18} fill={`url(#${glow})`} />
              <circle className={own.hub} r={CTX_HUB_R} />
              {m.rings.map((ring) => (
                <circle key={`t${ring.index}`} className={styles.track} r={ring.r} strokeWidth={CTX_STROKE} />
              ))}
              <circle className={own.limit} r={m.limitR} />
              <line className={own.limitTick} x1={0} y1={-m.limitR} x2={0} y2={-(c - 14)} />
              {m.rings.map((ring) => (
                <g
                  key={ring.index}
                  className={styles.mark}
                  style={vars({ '--c': TONE[ring.tone] })}
                  tabIndex={0}
                  role="img"
                  aria-label={`${bandLabel(ring.band)}: ${pct1(ring.share)} of calls`}
                  {...on(ring.index)}
                >
                  <path
                    className={`${styles.arc} ${own.flat} ${styles.draw}`}
                    d={arcPath(ring.r, 0, ring.sweep)}
                    pathLength={1}
                    strokeWidth={CTX_STROKE}
                    style={vars({ '--i': ring.index })}
                  />
                </g>
              ))}
            </g>
          </svg>
          <p className={own.limitLabel}>{fmtTokens(limit)} limit</p>
          <div className={styles.hub}>
            <CountUp text={avg} className={styles.hubValue} />
            <span className={styles.hubLabel}>Avg / call</span>
            <span className={own.calls}>{m.totalCalls ? `${int(m.totalCalls)} calls` : 'no calls'}</span>
          </div>
        </div>
        <div className={own.side}>
          <p className={styles.caption}>Context bands</p>
          <ul className={own.list}>
            {m.rings.map((ring) => (
              <li
                key={ring.index}
                className={own.item}
                style={vars({ '--c': TONE[ring.tone], '--i': ring.index })}
                {...on(ring.index)}
              >
                <span className={styles.dot} />
                <span className={own.band}>{bandLabel(ring.band)}</span>
                <span className={styles.num}>{pct1(ring.share)}</span>
                <span className={own.meta}>
                  {int(ring.calls)} · re-read {ring.reread === null ? '—' : pct1(ring.reread)}
                </span>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </Piece>
  );
}
