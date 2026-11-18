import { arc } from 'd3-shape';
import { useId, useRef } from 'react';
import { deltaPct, pct1, tokens as fmtTokens, usd } from '../../format/format';
import { Reveal } from '../Reveal';
import { polar } from './geometry';
import { CountUp, Leaders, Piece, useActive, useLeaders, useRowRefs, vars, type LeaderSpec } from './Piece';
import { TOK_BOX, tokensModel, type TokensInput, type TokenTone } from './tokens';
import styles from './pieces.module.css';
import own from './TokensPiece.module.css';

export const TOKEN_TONE: Record<TokenTone, string> = {
  gold: 'var(--sh-accent-gold)',
  session: 'var(--sh-data-session)',
  warm: 'var(--sh-ink-warm)',
  live: 'var(--sh-status-live)',
};

const RAD = Math.PI / 180;
const OUT = 148;
const IN = 112;
const ZOOM_OUT = 104;
const ZOOM_IN = 90;
const CORE = 82;

const sector = (r0: number, r1: number, start: number, sweep: number) =>
  arc()({ innerRadius: r0, outerRadius: r1, startAngle: start * RAD, endAngle: (start + sweep) * RAD,
    padAngle: sweep > 2 ? 0.006 : 0 }) ?? '';

export function TokensPiece({ data, label = 'Token mix' }: { data: TokensInput; label?: string }) {
  const m = tokensModel(data);
  const { on } = useActive();
  const root = useRef<HTMLElement>(null);
  const drawing = useRef<SVGSVGElement>(null);
  const { rows, bind } = useRowRefs();
  const maskId = useId();
  const zoomMask = useId();
  const c = TOK_BOX / 2;
  const mid = (s: { span: { start: number; sweep: number } }, r: number) =>
    polar(r, s.span.start + s.span.sweep / 2, c, c);
  const specs: LeaderSpec[] = [
    ...m.big.map((s) => ({ key: s.key, from: mid(s, OUT + 4), side: 'left' as const })),
    ...m.small.filter((s) => s.tokens > 0).map((s) => ({ key: s.key, from: mid(s, OUT + 4), side: 'right' as const })),
  ];
  const paths = useLeaders(root, drawing, rows, specs);
  const delta = data.costUsd === null ? null : deltaPct(data.costUsd, data.previousCost, data.days);
  return (
    <Piece name="tokens" label={label} state={m.state} className={own.root} rootRef={root}>
      <Leaders paths={paths} />
      <div className={own.left}>
        {m.big.map((s, i) => (
          <div key={s.key} ref={bind(s.key)} className={styles.callout}
            style={vars({ '--c': TOKEN_TONE[s.tone], '--i': i })} {...on(s.index)}>
            <span className={own.bigName}>
              {s.name} {pct1(s.share)}
            </span>
            <span className={styles.calloutMeta}>
              {fmtTokens(s.tokens)} tok · {usd(s.cost)}
            </span>
            <span className={styles.calloutMeta}>{pct1(s.costShare)} of cost</span>
          </div>
        ))}
        <p className={styles.note}>Ring = share of tokens · a tiny share shows as a 1° mark</p>
      </div>
      <div className={`${styles.drawing} ${own.draw}`} style={{ width: TOK_BOX, height: TOK_BOX }}>
        <svg className={styles.drift} width={TOK_BOX} height={TOK_BOX} viewBox={`0 0 ${TOK_BOX} ${TOK_BOX}`}
          aria-hidden="true">
          <circle className={own.dial} cx={c} cy={c} r={OUT + 18} />
        </svg>
        <svg ref={drawing} width={TOK_BOX} height={TOK_BOX} viewBox={`0 0 ${TOK_BOX} ${TOK_BOX}`} aria-hidden="true">
          <defs>
            <Reveal id={maskId} rings={[{ r: (OUT + IN) / 2, width: OUT - IN + 16 }]} extent={TOK_BOX} />
            <Reveal id={zoomMask} rings={[{ r: (ZOOM_OUT + ZOOM_IN) / 2, width: ZOOM_OUT - ZOOM_IN + 12 }]}
              extent={TOK_BOX} />
          </defs>
          <g transform={`translate(${c} ${c})`}>
            <circle className={own.ringTrack} r={(OUT + IN) / 2} strokeWidth={OUT - IN} />
            <circle className={own.ringTrack} r={(ZOOM_OUT + ZOOM_IN) / 2} strokeWidth={ZOOM_OUT - ZOOM_IN} />
            <g mask={`url(#${maskId})`}>
              {m.slices.map((s) =>
                s.span.sweep > 0 ? (
                  <path
                    key={s.key}
                    className={`${styles.mark} ${styles.slice}`}
                    d={sector(IN, OUT, s.span.start, s.span.sweep)}
                    style={vars({ '--c': TOKEN_TONE[s.tone] })}
                    tabIndex={0}
                    role="img"
                    aria-label={`${s.name}: ${pct1(s.share)} of tokens`}
                    {...on(s.index)}
                  />
                ) : null,
              )}
            </g>
            {m.zoom ? (
              <g mask={`url(#${zoomMask})`}>
                {m.slices.map((s, i) => {
                  const span = m.zoom?.spans[i];
                  return span && span.sweep > 0 ? (
                    <path key={s.key} className={`${styles.mark} ${styles.slice}`}
                      d={sector(ZOOM_IN, ZOOM_OUT, span.start,
                      span.sweep)} style={vars({ '--c': TOKEN_TONE[s.tone] })} {...on(s.index)} />
                  ) : null;
                })}
              </g>
            ) : null}
            <circle className={own.core} r={CORE} />
          </g>
        </svg>
        <div className={styles.hub}>
          <span className={styles.hubLabel}>Est. cost · {data.rangeLabel}</span>
          <CountUp text={data.costUsd === null ? '—' : usd(data.costUsd)} className={styles.hubValue} />
          <span className={styles.hubSub}>{data.tokens === null ? '— tokens'
            : `${fmtTokens(data.tokens)} tokens`}</span>
          <span className={own.delta}>{delta ?? '—'}</span>
        </div>
      </div>
      <div className={own.right}>
        <div>
          <p className={styles.title}>Small slices</p>
          <p className={styles.caption}>Under 5% of tokens</p>
        </div>
        {m.small.length ? (
          <ul className={own.smallList}>
            {m.small.map((s, i) => (
              <li key={s.key} ref={bind(s.key)} className={styles.callout} title={s.name}
                style={vars({ '--c': TOKEN_TONE[s.tone], '--i': i })} {...on(s.index)}>
                <span className={styles.calloutHead}>
                  <span className={`${styles.calloutName} ${own.smallName}`}>
                    <span className={own.swatch} /> {s.name}
                  </span>
                  <span className={styles.calloutShare}>{s.tokens > 0 && s.share < 0.001 ? '<0.1%'
                    : pct1(s.share)}</span>
                </span>
                <span className={styles.calloutMeta}>
                  {fmtTokens(s.tokens)} tok · {usd(s.cost)}
                </span>
                <span className={styles.bar}>
                  <span className={styles.barFill} style={vars({ '--w': s.bar, '--i': i })} />
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <p className={styles.empty}>{m.total > 0 ? 'Every kind is 5% or more' : 'No tokens in this range'}</p>
        )}
        <p className={styles.note}>Bar = tokens on a log scale, 100k to 1B</p>
        {m.zoom ? (
          <div>
            <p className={own.zoomTitle}>Inner ring · zoom ×{m.zoom.factor}</p>
            <p className={styles.note}>
              The {pct1(m.zoom.restShare)} outside {m.zoom.of.name}, redrawn to fill the circle
            </p>
          </div>
        ) : null}
      </div>
    </Piece>
  );
}
