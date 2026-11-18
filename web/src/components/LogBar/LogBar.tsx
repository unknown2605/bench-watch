import type_ from '../../styles/type.module.css';
import styles from './LogBar.module.css';

type Props = { segments: readonly string[]; spoken?: readonly string[]; nextScan: string | null; progress: number };

export function LogBar({ segments, spoken, nextScan, progress }: Props) {
  const join = (s: readonly string[]) => s.join('   ·   ');
  const line = join(segments);
  const full = join(spoken ?? segments);
  const pct = Math.max(0, Math.min(1, progress)) * 100;
  return (
    <footer className={styles.bar}>
      <span className={styles.edge} aria-hidden="true" />
      <p className={`${type_.mono} ${styles.log}`} tabIndex={0} role="region" aria-label="Activity log">
        <span className={styles.compact} aria-hidden="true" title={full}>{line}</span>
        <span className={styles.spoken}>{full}</span>
      </p>
      {nextScan ? <span className={`${type_.label} ${styles.next}`}>{nextScan}</span> : null}
      <span className={styles.track} aria-hidden="true">
        <span className={styles.fill} style={{ width: `${pct}%` }} />
      </span>
    </footer>
  );
}
