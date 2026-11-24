import type { Schemas } from '../../api';
import type { DockerContainer } from '../../charts/pieces/docker';

type Container = Schemas['ContainerNow'];

/** A null or absent exit code is unknown, never a clean exit. */
export function healthOf(c: Container): 'down' | 'warn' | 'healthy' | 'finished' {
  if (c.state === 'exited' && c.exitCode === 0) return 'finished';
  if (c.state !== 'running') return 'down';
  return c.health === 'unhealthy' || c.health === 'starting' || c.restarts > 0 ? 'warn' : 'healthy';
}

/** One rule for page and piece: whatever the page counts as a warning reads "unhealthy" in the piece. */
export function pieceHealth(c: Container): DockerContainer['health'] {
  const h = healthOf(c);
  if (h === 'finished') return 'finished';
  if (h !== 'healthy') return 'unhealthy';
  return c.health === 'healthy' ? 'healthy' : 'none';
}
