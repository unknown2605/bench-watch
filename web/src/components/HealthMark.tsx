export type Health = 'healthy' | 'warn' | 'down';

// Square when healthy, triangle on warn, outlined square when down, so the state does not rest on colour alone.
export const HEALTH_MARK: Record<Health, string> = { healthy: 'M0 0H8V8H0Z', warn: 'M4 0L8 8H0Z', down: 'M1 1H7V7H1ZM3 3V5H5V3Z' };

type Props = { health: Health; size?: number; className?: string | undefined };

export function HealthMark({ health, size = 7, className }: Props) {
  return (
    <svg className={className} width={size} height={size} viewBox="0 0 8 8" aria-hidden="true">
      <path d={HEALTH_MARK[health]} fill="currentColor" fillRule="evenodd" />
    </svg>
  );
}
