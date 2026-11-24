import { describe, expect, it } from 'vitest';
import { dockerModel, groupHealth, type DockerContainer } from './docker';

const box = (name: string, stack: string): DockerContainer => ({ name, stack, memBytes: 1, health: 'ok', restarts: 0 });

describe('dockerModel group order', () => {
  it('lists named stacks by plain string order and the other group last', () => {
    const containers = [
      box('a1', 'Zeta'), box('a2', 'Zeta'), box('a3', 'Zeta'),
      box('b1', 'alpha'), box('b2', 'alpha'),
      box('c1', 'Beta'),
      box('loose', 'none'),
    ];
    const m = dockerModel({ containers, stacks: [] });
    expect(m.groups.map((g) => g.key)).toEqual(['Beta', 'Zeta', 'alpha', 'other']);
  });
});

describe('dockerModel finished containers', () => {
  const states = (...health: string[]) => health.map((h, i) => ({ ...box(`c${i}`, 'one'), health: h }));

  it('counts a finished container apart from healthy, unchecked and warning ones', () => {
    const m = dockerModel({ containers: states('healthy', 'none', 'unhealthy', 'finished', 'finished'), stacks: [] });
    expect([m.total, m.healthy, m.noCheck, m.warn, m.finished]).toEqual([5, 1, 1, 1, 2]);
  });

  it.each([
    [['finished', 'finished'], 'finished'], [['finished', 'healthy'], 'none'], [['finished', 'unhealthy'], 'warn'],
  ])('reads a folded hex of %j as %s', (folded, want) => {
    const shown = Array.from({ length: 40 }, (_, i) => ({ ...box(`big${i}`, 'one'), memBytes: 100 - i }));
    const all = dockerModel({ containers: shown, stacks: [] });
    const fit = all.groups[0]?.hexes.filter((h) => h.count === 1).length ?? 0;
    expect(fit).toBeLessThan(40);
    const top = shown.slice(0, fit);
    const rest = folded.map((h, i) => ({ ...box(`small${i}`, 'one'), memBytes: 0, health: h }));
    const hexes = dockerModel({ containers: [...top, ...rest], stacks: [] }).groups[0]?.hexes ?? [];
    expect(hexes.find((h) => h.count === 2)?.health).toBe(want);
  });
});

describe('groupHealth', () => {
  const of = (...health: string[]) => groupHealth(health.map((h, i) => ({ ...box(`c${i}`, 'one'), health: h })));

  it('reads an empty list as none, not finished', () => {
    expect(of()).toBe('none');
  });

  it('warns on any warning, reads finished only for all finished, and none for a mix', () => {
    expect(of('finished', 'finished')).toBe('finished');
    expect(of('finished', 'healthy')).toBe('none');
    expect(of('healthy', 'unhealthy', 'finished')).toBe('warn');
    expect(of('finished')).toBe('finished');
  });
});
