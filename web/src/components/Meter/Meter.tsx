import styles from './Meter.module.css';

export type MeterTone = 'gold' | 'live' | 'warn' | 'session';
type Props = { value: number; tone?: MeterTone; label: string; className?: string | undefined };

export function Meter({ value, tone = 'gold', label, className }: Props) {
  const pct = Math.max(0, Math.min(1, value)) * 100;
  return (
    <div
      className={[styles.meter, styles[tone], className].filter(Boolean).join(' ')}
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(pct)}
    >
      <span className={styles.fill} style={{ width: `${pct}%` }} />
    </div>
  );
}
