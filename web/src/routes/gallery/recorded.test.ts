import { describe, expect, it } from 'vitest';
import type { Schemas } from '../../api';
import { dockerInputOf } from './recorded';

const container = (name: string, patch: Record<string, unknown>) => ({
  container: name, stack: 'infra', state: 'running', health: 'none', exitCode: null, cpuPct: 1, memBytes: 10,
  memLimitBytes: null, rxBps: 0, txBps: 0, restarts: 0, startedAt: null, mergedNames: 0, ...patch,
});

describe('dockerFrom rule', () => {
  it('reads state and exit code like the Docker page, not the bare health string', () => {
    const containers = [
      container('done', { state: 'exited', exitCode: 0 }),
      container('crashed', { state: 'exited', exitCode: 137 }),
      container('up', { health: 'healthy' }),
      container('bare', {}),
      container('flaky', { health: 'healthy', restarts: 2 }),
    ];
    const d = { now: { containers, stacks: [] } } as unknown as Schemas['DockerResponse'];
    const health = Object.fromEntries((dockerInputOf(d)?.containers ?? []).map((c) => [c.name, c.health]));
    expect(health).toEqual({
      done: 'finished', crashed: 'unhealthy', up: 'healthy', bare: 'none', flaky: 'unhealthy',
    });
  });

  it('gives nothing without a snapshot', () => {
    expect(dockerInputOf(null)).toBeNull();
  });
});
