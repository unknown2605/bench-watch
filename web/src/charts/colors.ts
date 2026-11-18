export type DataTone = 'gold' | 'live' | 'warn' | 'session' | 'dim';

export const TONE_VAR: Record<DataTone, string> = {
  gold: 'var(--sh-accent-gold)',
  live: 'var(--sh-status-live)',
  warn: 'var(--sh-status-warn)',
  session: 'var(--sh-data-session)',
  dim: 'var(--sh-ink-dim)',
};
