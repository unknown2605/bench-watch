import { describe, expect, it } from 'vitest';
import { dockerModel, type DockerContainer } from './docker';

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
