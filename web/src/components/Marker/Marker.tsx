import styles from './Marker.module.css';

export type MarkerKind = 'compaction' | 'peak' | 'event';
type Props = { kind: MarkerKind; label: string; className?: string | undefined };

export function Marker({ kind, label, className }: Props) {
  return (
    <svg className={[styles.marker, className].filter(Boolean).join(' ')} width="16" height="16" viewBox="0 0 16 16" role="img" aria-label={label}>
      <MarkerShape kind={kind} />
    </svg>
  );
}

export function MarkerShape({ kind }: { kind: MarkerKind }) {
  if (kind === 'peak') return <circle cx="8" cy="8" r="5" />;
  if (kind === 'event') return <rect x="3.5" y="3.5" width="9" height="9" transform="rotate(45 8 8)" />;
  return <path d="M8 1 L9.39 6.61 L15 8 L9.39 9.39 L8 15 L6.61 9.39 L1 8 L6.61 6.61 Z" />;
}
