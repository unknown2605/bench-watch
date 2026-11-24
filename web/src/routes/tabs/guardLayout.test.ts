// @vitest-environment node
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

describe('Guard.module.css text, not the layout itself', () => {
  it('still has the rule below 1280 that keeps the content height of the rules and errors panels', () => {
    const css = readFileSync(new URL('./Guard.module.css', import.meta.url), 'utf8');
    const narrow = /@media\s*\(max-width:\s*1279\.98px\)\s*\{((?:[^{}]*\{[^{}]*\})*)/.exec(css)?.[1] ?? '';
    const kept = [...narrow.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
      .filter(([, , body]) => /(?:^|[;\s])flex:\s*1\s+0\s+auto\s*(?:;|$)/.test(body ?? ''))
      .flatMap(([, selectors]) => (selectors ?? '').split(',').map((s) => s.trim()));
    expect(kept).toEqual(expect.arrayContaining(['.rules', '.errors']));
  });
});
