import type { ReactNode } from 'react';
import type_ from '../../styles/type.module.css';
import styles from './Badge.module.css';

export type BadgeKind = 'live' | 'warn' | 'gold';
type Props = { kind: BadgeKind; children: ReactNode; className?: string | undefined };

export function Badge({ kind, children, className }: Props) {
  return <span className={[styles.badge, styles[kind], type_.label, className].filter(Boolean).join(' ')}>{children}</span>;
}
