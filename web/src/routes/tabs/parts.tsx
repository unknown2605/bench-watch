import { useRef, type ReactNode } from 'react';
import { TONE_VAR, type DataTone } from '../../charts/colors';
import { Meter } from '../../components';
import { useShrinkToFit } from '../../components/KPI/useShrinkToFit';
import type { MeterTone } from '../../components/Meter/Meter';
import { useElementSize } from '../../components/useElementSize';
import { pct } from '../../format/format';
import { segments } from '../../format/scale';
import { AnimatedNumber } from '../../motion/AnimatedNumber';
import type_ from '../../styles/type.module.css';
import styles from './parts.module.css';

export type MixPart = { name: string; value: number; tone: DataTone };

/** Stacked share bar with a legend; the legend carries the numbers, so colour is never the only cue. */
export function MixBar({ parts, label, legend = true, height = 12 }: {
  parts: readonly MixPart[];
  label: string;
  legend?: boolean;
  height?: number;
}) {
  const segs = segments(parts, (p) => p.value, 0.005);
  const total = parts.reduce((s, p) => s + Math.max(0, p.value), 0);
  return (
    <div className={styles.mix}>
      <div
        className={styles.mixBar}
        style={{ height }}
        role="img"
        aria-label={`${label}: ${parts.map((p) => `${p.name} ${pct(total ? p.value / total : 0)}`).join(', ')}`}
      >
        {segs.map((s) => (
          <span
            key={s.item.name}
            className={styles.mixSeg}
            style={{
              left: `${s.start * 100}%`,
              width: `${(s.end - s.start) * 100}%`,
              background: TONE_VAR[s.item.tone],
            }}
          />
        ))}
      </div>
      {legend ? (
        <ul className={styles.legend} aria-hidden="true">
          {parts.map((p) => (
            <li key={p.name} className={type_.label}>
              <span className={styles.swatch} style={{ background: TONE_VAR[p.tone] }} />
              {p.name} {pct(total ? p.value / total : 0)}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

type Pair = { label: string; share: number; tone: MeterTone };

/** A named row with two labelled meters, e.g. share of tokens against share of cost. */
export function PairRow({ name, value, tone, pairs }: {
  name: string;
  value: string;
  tone: DataTone;
  pairs: readonly Pair[];
}) {
  return (
    <div className={styles.pair}>
      <span className={styles.square} style={{ background: TONE_VAR[tone] }} aria-hidden="true" />
      <span className={`${type_.body} ${styles.pairName}`}>{name}</span>
      <span className={`${type_.numberS} ${styles.pairValue}`}>{value}</span>
      {pairs.map((p) => (
        <div key={p.label} className={styles.pairLine}>
          <span className={`${type_.monoS} ${styles.pairLabel}`}>{p.label}</span>
          <Meter className={styles.pairMeter} value={p.share} tone={p.tone} label={`${name} ${p.label} share`} />
          <span className={`${type_.monoS} ${styles.pairPct}`}>{pct(p.share)}</span>
        </div>
      ))}
    </div>
  );
}

export function Stat({ value, caption, tone = 'ink', large = false }: {
  value: string;
  caption: string;
  tone?: 'ink' | 'live' | 'warn';
  large?: boolean;
}) {
  return (
    <div className={styles.stat}>
      <span className={`${large ? styles.statLarge : styles.statValue} ${styles[tone]}`}>{value}</span>
      <span className={`${type_.monoS} ${styles.statCaption} ${tone === 'ink' ? '' : styles[tone]}`}>{caption}</span>
    </div>
  );
}

export function Heading({ children }: { children: ReactNode }) {
  return <h3 className={`${type_.label} ${styles.heading}`}>{children}</h3>;
}

export function Divider() {
  return <hr className={styles.divider} />;
}

export function LineRow({ cells, tone = 'normal' }: {
  cells: readonly [string, string, string];
  tone?: 'normal' | 'warn';
}) {
  return (
    <div className={`${styles.line} ${tone === 'warn' ? styles.lineWarn : ''}`}>
      <span className={`${type_.monoS} ${styles.lineWhen}`}>{cells[0]}</span>
      <span className={`${type_.body} ${styles.lineName}`} title={cells[1]}>{cells[1]}</span>
      <span className={`${type_.mono} ${styles.lineValue}`}>{cells[2]}</span>
    </div>
  );
}

/** One-line name cell: the CSS clips the overflow with an ellipsis and the title keeps the whole name. */
export function ClippedName({ name, full }: { name: string; full: string }) {
  return <span className={styles.clipName} title={full}>{name}</span>;
}

type StatValueProps = { text: string; scope?: string; animate?: boolean };

/** A figure in a stats grid cell; it shrinks to the cell's width instead of spilling over its neighbour. */
export function StatValue({ text, scope, animate = true }: StatValueProps) {
  const box = useRef<HTMLSpanElement>(null);
  const inner = useRef<HTMLSpanElement>(null);
  const [width] = useElementSize(box);
  const fit = useShrinkToFit(inner, text, width);
  return (
    <span ref={box} className={styles.statBox}>
      <span ref={inner} className={styles.statInner} {...(fit < 1 ? { style: { transform: `scale(${fit})` } } : {})}>
        {animate ? <AnimatedNumber text={text} {...(scope !== undefined ? { scope } : {})} /> : text}
      </span>
    </span>
  );
}

/** The range words a centre piece shows in its hub, from the answer's period length. */
export const pieceRange = (days: number) => (days === 1 ? 'today' : `${days} d`);
