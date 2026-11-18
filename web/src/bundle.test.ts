// @vitest-environment node
import { mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { build } from 'vite';
import { describe, expect, it } from 'vitest';
import { containers, logSegments, sessions, stars } from './mock/data';

const MOCK_NAMES = [
  ...sessions.map((s) => s.name),
  ...stars.map((s) => s.name),
  ...containers.map((c) => c.name).filter((n) => n.length >= 6),
  ...logSegments,
  'NEXT SCAN 7 s',
];

async function bundleText(gallery: boolean) {
  const outDir = mkdtempSync(path.join(tmpdir(), 'cm-bundle-'));
  const before = process.env.VITE_GALLERY;
  process.env.VITE_GALLERY = gallery ? '1' : '';
  try {
    await build({ logLevel: 'silent', build: { outDir, emptyOutDir: true } });
    const assets = path.join(outDir, 'assets');
    return readdirSync(assets)
      .filter((f) => /\.(js|css)$/.test(f))
      .map((f) => readFileSync(path.join(assets, f), 'utf8'))
      .join('\n');
  } finally {
    if (before === undefined) delete process.env.VITE_GALLERY;
    else process.env.VITE_GALLERY = before;
    rmSync(outDir, { recursive: true, force: true });
  }
}

describe('production bundle', () => {
  it('holds none of the mock names, while the gallery build (the control) holds them', async () => {
    const gallery = await bundleText(true);
    expect(MOCK_NAMES.filter((n) => gallery.includes(n)).length).toBeGreaterThan(3);
    const prod = await bundleText(false);
    expect(MOCK_NAMES.filter((n) => prod.includes(n))).toEqual([]);
  }, 90_000);
});
