// @vitest-environment node
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

describe('docker hex health dot', () => {
  it('draws the finished dot as a square, so it differs from the healthy dot by more than its hue', () => {
    const css = readFileSync(new URL('./DockerPiece.module.css', import.meta.url), 'utf8');
    expect(css).toMatch(/\.finished \.health\s*\{[^}]*border-radius:\s*0/);
  });
});
