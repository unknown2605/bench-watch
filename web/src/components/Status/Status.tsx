import type_ from '../../styles/type.module.css';
import styles from './Status.module.css';

export type StatusKind = 'live' | 'idle' | 'warn';
const LABEL: Record<StatusKind, string> = { live: 'LIVE', idle: 'IDLE', warn: 'WARN' };
type Props = { kind: StatusKind; label?: string; className?: string | undefined };

export function Status({ kind, label = LABEL[kind], className }: Props) {
  return (
    <span className={[styles.status, styles[kind], type_.tab, className].filter(Boolean).join(' ')}>
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.label}>{label}</span>
    </span>
  );
}
