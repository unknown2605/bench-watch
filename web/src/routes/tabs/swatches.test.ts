// @vitest-environment node
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

describe('network legend swatches', () => {
  it('gives the DOWN swatch a box, because an empty inline element draws nothing', () => {
    const css = readFileSync(new URL('./views.module.css', import.meta.url), 'utf8');
    expect(css).toMatch(/\.netAxis \.swatchTeal\s*\{[^}]*display:\s*inline-block/);
  });
});
