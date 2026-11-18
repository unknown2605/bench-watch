import { arc } from 'd3-shape';
import { useId, useRef } from 'react';
import { keyed } from '../../app/keys';
import { int, pct1, tokens as fmtTokens } from '../../format/format';
import { Reveal } from '../Reveal';
import { polar } from './geometry';
import {
  CountUp, Leaders, Piece, selectable, useActive, useLeaders, useRowRefs, vars, type LeaderSpec, type Selection,
} from './Piece';
import { FILL_BOX, FILL_TOP, fillsModel, leaderAnchors, type FillsInput, type FillTone } from './fills';
import styles from './pieces.module.css';
import own from './FillsPiece.module.css';

export const FILL_TONE: Record<FillTone, string> = {
  gold: 'var(--sh-accent-gold)',
  session: 'var(--sh-data-session)',
  warm: 'var(--sh-ink-warm)',
  live: 'var(--sh-status-live)',
  slate: 'color-mix(in srgb, var(--sh-ink-dim) 75%, var(--sh-surface-navy))',
  bronze: 'var(--sh-accent-gold-muted)',
  small: 'color-mix(in srgb, var(--sh-ink-dim) 38%, var(--sh-surface-navy))',
};

const RAD = Math.PI / 180;
const OUT = 148;
const IN = 112;
const CORE = 100;
const SMALL = 'small-tools';

const sector = (start: number, sweep: number) =>
  arc()({ innerRadius: IN, outerRadius: OUT, startAngle: start * RAD, endAngle: (start + sweep) * RAD,
    padAngle: sweep > 2 ? 0.008 : 0 }) ?? '';

const mTok = (n: number) => `${(n / 1e6).toFixed(2)}M`;

type Props = Selection & { data: FillsInput; label?: string };

export function FillsPiece({ data, label = 'Tool result fills', selected, onSelect }: Props) {
  const m = fillsModel(data);
  const { on } = useActive();
  const root = useRef<HTMLElement>(null);
  const drawing = useRef<SVGSVGElement>(null);
  const { rows, bind } = useRowRefs();
  const maskId = useId();
  const c = FILL_BOX / 2;
  const sel = { selected, onSelect };
  const marks = onSelect ? { role: 'group', 'aria-label': 'Tool slices' } : { 'aria-hidden': true };
  const items = m.slices.map((s) => ({ key: s.tool, span: s.span, side: s.side }));
  if (m.small) items.push({ key: SMALL, span: m.small.span, side: 'left' });
  const anchors = leaderAnchors(items);
  const specs: LeaderSpec[] = anchors.map((a, i) => ({
    key: a.key, from: polar(OUT + 4, a.angle, c, c), side: items[i]?.side ?? 'left', elbow: a.shared,
  }));
  const paths = useLeaders(root, drawing, rows, specs);
  const midOf = (s: (typeof m.slices)[number]) => s.span.start + s.span.sweep / 2;
  const right = m.slices.filter((s) => s.side === 'right').sort((a, b) => midOf(a) - midOf(b));
  const left = m.slices.filter((s) => s.side === 'left').sort((a, b) => midOf(b) - midOf(a));
  const callout = ([key, s]: [string, (typeof m.slices)[number]]) => (
    <div
      key={key}
      ref={bind(s.tool)}
      className={`${styles.callout} ${own.callout}`}
      style={vars({ '--c': FILL_TONE[s.tone], '--i': s.rank })}
      title={s.tool}
      {...on(s.rank - 1)}
      {...selectable(sel, s.tool)}
    >
      <span className={styles.calloutHead}>
        <span className={styles.calloutName}>{s.tool}</span>
        <span className={styles.calloutShare}>{pct1(s.share)}</span>
      </span>
      <span className={styles.calloutMeta}>
        {mTok(s.tokens)} tok · {int(s.results)} results
      </span>
    </div>
  );
  return (
    <Piece name="fills" label={label} state={m.state} className={own.root} rootRef={root}>
      <Leaders paths={paths} />
      <div className={own.left}>
        {m.small ? (
          <div ref={bind(SMALL)} className={own.tail}>
            <p className={styles.title}>Small tools</p>
            <p className={styles.caption}>
              {m.small.tools} tools · {pct1(m.small.share)} · below the top {FILL_TOP}
            </p>
            <ul className={own.tailList}>
              {keyed(m.tail, (t) => t.tool).map(([key, t], i) => (
                <li key={key} className={own.tailRow} style={vars({ '--i': i })} title={t.tool}>
                  <span className={styles.calloutHead}>
                    <span className={styles.calloutName}>{t.tool}</span>
                    <span className={own.tailShare}>{pct1(t.share)}</span>
                  </span>
                  <span className={styles.calloutMeta}>{mTok(t.tokens)} tok</span>
                  <span className={styles.bar}>
                    <span className={styles.barFill} style={vars({ '--w': t.bar, '--i': i, '--c': FILL_TONE.slate })} />
                  </span>
                </li>
              ))}
            </ul>
            <p className={styles.note}>
              {m.more > 0 ? `+ ${m.more} more · ` : ''}bar = share, scale 0 to 5%
            </p>
          </div>
        ) : null}
        <div className={own.leftCallouts}>{keyed(left, (s) => s.tool).map(callout)}</div>
        <p className={styles.note}>Ring = share of result tokens, weighted by times re-sent</p>
      </div>
      <div className={`${styles.drawing} ${own.draw}`} style={{ width: FILL_BOX, height: FILL_BOX }}>
        <svg className={styles.drift} width={FILL_BOX} height={FILL_BOX} viewBox={`0 0 ${FILL_BOX} ${FILL_BOX}`}
          aria-hidden="true">
          <circle className={own.dial} cx={c} cy={c} r={OUT + 18} />
        </svg>
        <svg ref={drawing} width={FILL_BOX} height={FILL_BOX} viewBox={`0 0 ${FILL_BOX} ${FILL_BOX}`} {...marks}>
          <defs>
            <Reveal id={maskId} rings={[{ r: (OUT + IN) / 2, width: OUT - IN + 16 }]} extent={FILL_BOX} />
          </defs>
          <g transform={`translate(${c} ${c})`}>
            <circle className={own.ringTrack} r={(OUT + IN) / 2} strokeWidth={OUT - IN} />
            <g mask={`url(#${maskId})`}>
              {keyed(m.slices, (s) => s.tool).map(([key, s]) => (
                <path
                  key={key}
                  className={`${styles.mark} ${styles.slice} ${own.pick}`}
                  d={sector(s.span.start, s.span.sweep)}
                  style={vars({ '--c': FILL_TONE[s.tone] })}
                  tabIndex={0}
                  role="img"
                  aria-label={`Rank ${s.rank}: ${s.tool}, ${pct1(s.share)}`}
                  {...on(s.rank - 1)}
                  {...selectable(sel, s.tool)}
                />
              ))}
              {m.small ? <path className={`${styles.mark} ${styles.slice}`}
                d={sector(m.small.span.start, m.small.span.sweep)} style={vars({ '--c': FILL_TONE.small })} /> : null}
            </g>
            <circle className={own.core} r={CORE} />
          </g>
        </svg>
        <div className={styles.hub}>
          <span className={styles.hubLabel}>From tool results</span>
          <CountUp text={data.contextShare === null ? '—' : pct1(data.contextShare)} className={styles.hubValue} />
          <span className={styles.hubSub}>
            {data.resultTokens === null ? '—' : `${mTok(data.resultTokens)} result tok`} · {data.rangeLabel}
          </span>
          <span className={styles.hubSub}>weighted {m.total ? fmtTokens(m.total) : '—'} (times sent)</span>
        </div>
      </div>
      <div className={own.right}>
        {keyed(right, (s) => s.tool).map(callout)}
        {!m.slices.length ? <p className={styles.empty}>No tool results in this range</p> : null}
      </div>
    </Piece>
  );
}
