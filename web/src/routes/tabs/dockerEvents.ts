import type { Schemas } from '../../api';
import { healthOf } from './dockerHealth';

type Event = Schemas['DockerEvent'];

const ALWAYS_BAD = new Set<Schemas['DockerEventKind']>(['oom', 'restart', 'health_unhealthy']);
// Docker may stamp `stop` just before or just after the `die` it belongs to.
const STOP_WINDOW_MS = 2000;
// The collector's label for unsafe names; it can stand for several containers.
const MERGED = 'other';

function stoppedDies(events: Event[]): Set<Event> {
  const dies = events.filter((e) => e.kind === 'die');
  const stopped = new Set<Event>();
  for (const stop of events) {
    if (stop.kind !== 'stop') continue;
    let nearest: Event | null = null;
    let best = STOP_WINDOW_MS;
    for (const die of dies) {
      const gap = Math.abs(Date.parse(die.at) - Date.parse(stop.at));
      if (die.container === stop.container && gap <= best) [nearest, best] = [die, gap];
    }
    if (nearest) stopped.add(nearest);
  }
  return stopped;
}

/** The events that point to a fault; a `kill` alone never does, its container's `die` decides. */
export function badEvents(events: Event[], containers: Schemas['ContainerNow'][]): Set<Event> {
  const stopped = stoppedDies(events);
  const finished = new Set(containers.filter((c) => healthOf(c) === 'finished').map((c) => c.container));
  return new Set(events.filter((e) => {
    if (e.kind !== 'die') return ALWAYS_BAD.has(e.kind);
    return e.container === MERGED || !(stopped.has(e) || finished.has(e.container));
  }));
}
