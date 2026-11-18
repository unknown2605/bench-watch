import type { JsonOf, Schemas } from '../api';

export type MachineData = JsonOf<'/api/machine'>;
export type DockerData = JsonOf<'/api/docker'>;
type Machine = Schemas['MachineSnapshot'];
type Docker = Schemas['DockerSnapshot'];

/** The Live range covers 5 minutes at raw resolution; older points leave as new ones arrive. */
export const LIVE_WINDOW_MS = 300_000;

const ms = (iso: string) => Date.parse(iso);

function append<T extends { at: string }>(points: readonly T[], next: T, from: number): T[] {
  return [...points.filter((p) => ms(p.at) >= from), next];
}

/** A Live-range response with the snapshot put in as its newest sample; the same response when it is not newer. */
export function withMachine(old: MachineData, snap: Machine): MachineData {
  if (old.now && ms(snap.at) <= ms(old.now.at)) return old;
  const from = ms(snap.at) - LIVE_WINDOW_MS;
  const at = snap.at;
  const series = append(
    old.series,
    {
      at,
      cpuBusyPct: snap.cpuBusyPct,
      cpuBusyMaxPct: snap.cpuBusyPct,
      load1: snap.load?.one ?? null,
      memUsedBytes: snap.memory?.usedBytes ?? null,
      swapUsedBytes: snap.memory?.swapUsedBytes ?? null,
      diskReadBps: snap.diskIo?.readBps ?? null,
      diskWriteBps: snap.diskIo?.writeBps ?? null,
    },
    from,
  );
  const coreSeries = snap.cores.map((c) => {
    const had = old.coreSeries.find((s) => s.core === c.core);
    return { core: c.core, kind: c.kind, points: append(had?.points ?? [], { at, busyPct: c.busyPct }, from) };
  });
  const interfaceSeries = snap.interfaces.map((i) => {
    const had = old.interfaceSeries.find((s) => s.interface === i.interface);
    const points = append(had?.points ?? [], { at, rxBps: i.rxBps, txBps: i.txBps }, from);
    const top = points.reduce((best, p) => (p.rxBps >= best.rxBps ? p : best));
    return { interface: i.interface, peakRxBps: top.rxBps, peakRxAt: top.at, points };
  });
  return { ...old, now: snap, coverageFrom: old.coverageFrom ?? at, series, coreSeries, interfaceSeries };
}

/** What a container shows besides its numbers; a change there means Docker events the snapshot cannot carry. */
export function dockerShape(snap: Docker | null | undefined): string {
  if (!snap) return '';
  return snap.containers.map((c) => `${c.stack}/${c.container}:${c.state}:${c.health}:${c.restarts}`).sort().join('|');
}

/** Same for Docker; `snap` holds every stack, so it only goes into a response that was not filtered by stack. */
export function withDocker(old: DockerData, snap: Docker): DockerData {
  if (old.now && ms(snap.at) <= ms(old.now.at)) return old;
  const from = ms(snap.at) - LIVE_WINDOW_MS;
  const at = snap.at;
  const vmSeries = append(old.vmSeries, { at, cpuPct: snap.vm.cpuPct, memUsedBytes: snap.vm.memUsedBytes }, from);
  const live = snap.containers.map((c) => {
    const had = old.containerSeries.find((s) => s.container === c.container && s.stack === c.stack);
    const point = { at, cpuPct: c.cpuPct, memBytes: c.memBytes, rxBps: c.rxBps, txBps: c.txBps };
    return { container: c.container, stack: c.stack, points: append(had?.points ?? [], point, from) };
  });
  const gone = old.containerSeries
    .filter((s) => !snap.containers.some((c) => c.container === s.container && c.stack === s.stack))
    .map((s) => ({ ...s, points: s.points.filter((p) => ms(p.at) >= from) }))
    .filter((s) => s.points.length > 0);
  const containerSeries = [...live, ...gone];
  return { ...old, now: snap, coverageFrom: old.coverageFrom ?? at, vmSeries, containerSeries, available: true };
}
