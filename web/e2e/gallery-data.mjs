// Copies recorded API responses into the git-ignored web/gallery-data for the gallery build.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';

const src = process.argv[2];
if (!src) {
  console.error('usage: node e2e/gallery-data.mjs <folder with recorded responses>');
  process.exit(2);
}
const OUT = path.resolve('gallery-data');
mkdirSync(OUT, { recursive: true });

const RANGES = ['today', '7d', '30d', 'all'];
const FILES = [
  ...['tokens', 'context', 'sessions', 'subagents', 'fills', 'guard'].flatMap((k) => RANGES.map((r) => `${k}-${r}`)),
  'machine-live', 'machine-1h', 'machine-24h', 'machine-7d', 'docker-live', 'docker',
];
// No centre piece draws series, per-result rows or hashes, so they stay out of the gallery bundle.
const DROP = new Set([
  'coreSeries', 'interfaceSeries', 'series', 'containerSeries', 'vmSeries', 'events', 'top', 'byProject', 'groups',
  'recent',
]);

const prune = (v) => {
  if (Array.isArray(v)) return v.map(prune);
  if (v && typeof v === 'object') {
    return Object.fromEntries(Object.entries(v).filter(([k]) => !DROP.has(k)).map(([k, x]) => [k, prune(x)]));
  }
  return v;
};

let n = 0;
for (const name of FILES) {
  let text;
  try {
    text = readFileSync(path.join(src, `${name}.json`), 'utf8');
  } catch {
    console.error(`missing ${name}.json`);
    continue;
  }
  writeFileSync(path.join(OUT, `${name}.json`), `${JSON.stringify(prune(JSON.parse(text)))}\n`);
  n += 1;
}
console.log(`wrote ${n} of ${FILES.length} files to ${OUT}`);
