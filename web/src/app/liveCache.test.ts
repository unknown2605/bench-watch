import { describe, expect, it } from 'vitest';
import type { Schemas } from '../api';
import { dockerShape, withDocker, withMachine, type DockerData, type MachineData } from './liveCache';

const snap = (at: string, cpu: number): Schemas['MachineSnapshot'] => ({
  at,
  cpuBusyPct: cpu,
  cores: [{ core: 0, kind: 'performance', busyPct: cpu }],
  performanceCores: 1,
  efficiencyCores: 0,
  load: { one: 1, five: 1, fifteen: 1 },
  memory: null,
  diskIo: { readBps: 5, writeBps: 6 },
  interfaces: [{ interface: 'en0', rxBps: 10, txBps: 20, mergedNames: 0 }],
  volumes: [],
  apps: [],
});

const base = (): MachineData => ({
  range: 'live', resolution: 'raw', detailIntervalSeconds: 2, coverageFrom: null, partial: false, historyPaused: false,
  now: null, series: [], coreSeries: [], interfaceSeries: [],
});

describe('withMachine', () => {
  it('appends the snapshot as the newest point and drops points older than five minutes', () => {
    const first = withMachine(base(), snap('2026-11-11T10:00:00Z', 10));
    const next = withMachine(first, snap('2026-11-11T10:06:00Z', 20));
    expect(next.series.map((p) => p.cpuBusyPct)).toEqual([20]);
    expect(next.now?.cpuBusyPct).toBe(20);
    expect(next.coreSeries[0]?.points).toHaveLength(1);
    expect(next.interfaceSeries[0]?.points[0]).toMatchObject({ rxBps: 10, txBps: 20 });
  });

  it('derives the interface peak from the retained points, so it ages out with them', () => {
    const rx = (at: string, rxBps: number) => {
      const s = snap(at, 10);
      return { ...s, interfaces: [{ interface: 'en0', rxBps, txBps: 1, mergedNames: 0 }] };
    };
    const spike = withMachine(base(), rx('2026-11-11T10:00:00Z', 900));
    const calm = withMachine(spike, rx('2026-11-11T10:02:00Z', 50));
    expect(calm.interfaceSeries[0]).toMatchObject({ peakRxBps: 900, peakRxAt: '2026-11-11T10:00:00Z' });
    const aged = withMachine(calm, rx('2026-11-11T10:06:00Z', 40));
    expect(aged.interfaceSeries[0]).toMatchObject({ peakRxBps: 50, peakRxAt: '2026-11-11T10:02:00Z' });
  });

  it('ignores a snapshot that is not newer', () => {
    const first = withMachine(base(), snap('2026-11-11T10:00:00Z', 10));
    expect(withMachine(first, snap('2026-11-11T10:00:00Z', 99))).toBe(first);
  });
});

describe('withDocker', () => {
  const docker = (at: string, state: 'running' | 'exited'): Schemas['DockerSnapshot'] => ({
    at,
    network: 'stack_full',
    vm: { memTotalBytes: 10, memUsedBytes: 5, cpuPct: 3, swapUsedBytes: 0, cpus: 4 },
    counts: { containers: 1, running: 1, healthy: 0, unhealthy: 0, starting: 0, noHealthcheck: 1 },
    containers: [{
      container: 'web', stack: 'app', state, health: 'none', cpuPct: 1, memBytes: 2, memLimitBytes: null,
      rxBps: 3, txBps: 4, restarts: 0, startedAt: null, mergedNames: 0,
    }],
    stacks: [],
    disk: null,
  });
  const empty = (): DockerData => ({
    range: 'live', resolution: 'raw', detailIntervalSeconds: 2, coverageFrom: null, partial: false, available: false,
    historyPaused: false, now: null, vmSeries: [], containerSeries: [], events: [],
  });

  it('puts the snapshot into the VM and container series', () => {
    const next = withDocker(empty(), docker('2026-11-11T10:00:00Z', 'running'));
    expect(next.vmSeries).toHaveLength(1);
    expect(next.containerSeries[0]?.points[0]).toMatchObject({ rxBps: 3, txBps: 4 });
    expect(next.available).toBe(true);
  });

  it('tells a container state change from a plain sample', () => {
    expect(dockerShape(docker('a', 'running'))).not.toBe(dockerShape(docker('a', 'exited')));
    expect(dockerShape(docker('a', 'running'))).toBe(dockerShape(docker('b', 'running')));
  });
});
