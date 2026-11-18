import { useLayoutEffect, useRef } from 'react';
import { HealthMark } from '../HealthMark';
import { AnimatedNumber } from '../../motion/AnimatedNumber';
import { Meter, type MeterTone } from '../Meter';
import a11y from '../../styles/a11y.module.css';
import type_ from '../../styles/type.module.css';
import styles from './ListRow.module.css';

type Props = {
  name: string;
  meta: string;
  value: string;
  /** Null leaves the bar out, for a figure with no limit to fill against. */
  fill: number | null;
  tone?: 'normal' | 'warn';
  meter?: MeterTone;
  className?: string | undefined;
};

export function ListRow({ name, meta, value, fill, tone = 'normal', meter = 'gold', className }: Props) {
  const warn = tone === 'warn';
  const row = useRef<HTMLDivElement>(null);
  const occupant = useRef(name);
  useLayoutEffect(() => {
    if (occupant.current === name) return;
    occupant.current = name;
    // A new occupant of this slot: its bar lands at once, like the number beside it, and keeps its element.
    const bar = row.current?.querySelector<HTMLElement>('[role="meter"] > span');
    if (!bar) return;
    bar.style.transition = 'none';
    void getComputedStyle(bar).width;
    bar.style.transition = '';
  }, [name]);
  return (
    <div ref={row} className={[styles.row, warn ? styles.warn : '', className].filter(Boolean).join(' ')}>
      <HealthMark className={styles.dot} health={warn ? 'warn' : 'healthy'} />
      {warn ? <span className={a11y.hidden}>Warning</span> : null}
      <span className={`${type_.body} ${styles.name}`} title={name}>{name}</span>
      <span className={`${type_.mono} ${styles.meta}`} title={meta}>{meta}</span>
      {fill === null ? <span className={styles.meter} aria-hidden="true" /> : (
        <Meter className={styles.meter} value={fill} tone={warn ? 'warn' : meter} label={`${name} fill`} />
      )}
      <span className={`${type_.mono} ${styles.value}`}><AnimatedNumber text={value} scope={name} /></span>
    </div>
  );
}
