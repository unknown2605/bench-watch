#!/usr/bin/env node
// Usage: node bin/publish-web.mjs [dist-folder]
// Exit 0 published and cleaned, 1 nothing published, 3 the new page is live but old hashed assets were not all removed.
import { lstatSync, readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import {
  codeOf, enterRealDir, failure, nextDir, printable, pruneHashedFiles, publishTogether,
} from './publish-lib.mjs';

const DIST = path.resolve(process.argv[2] ?? new URL('../web/dist', import.meta.url).pathname);
const ASSETS = 'assets';

// The build folder is ours, but a link in it would still be followed by readFileSync, so it is refused.
function collect(dir, depth = 0) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const full = path.join(dir, name);
    const st = lstatSync(full);
    if (st.isSymbolicLink()) throw failure('ESYMLINK');
    if (st.isDirectory()) {
      if (depth >= 1) throw failure('ETOODEEP');
      out.push(...collect(full, depth + 1));
    } else if (st.isFile()) {
      out.push([path.relative(DIST, full), readFileSync(full)]);
    }
  }
  return out;
}

let root;
try {
  root = nextDir();
} catch {
  console.error('publish refused: BENCHWATCH_WEB_DIR is not set (the published web folder; next/ sits under it)');
  process.exit(2);
}

let swapped = false;
try {
  if (lstatSync(DIST).isSymbolicLink()) throw failure('ESYMLINK');
  const files = collect(DIST);
  if (!files.some(([name]) => name === 'index.html')) throw failure('ENOINDEX');
  const inDir = (sub) =>
    files.filter(([name]) => path.dirname(name) === sub).map(([name, body]) => [path.basename(name), body]);
  const subdirs = [...new Set(files.map(([name]) => path.dirname(name)).filter((d) => d !== '.'))];
  // New files land first and index.html is renamed last, so a failure leaves the old page working.
  for (const sub of subdirs) {
    enterRealDir(path.join(root, sub));
    publishTogether(inDir(sub));
  }
  enterRealDir(root);
  const top = inDir('.');
  publishTogether(top.filter(([n]) => n !== 'index.html'));
  publishTogether(top.filter(([n]) => n === 'index.html'));
  swapped = true;
  console.log(`published ${files.length} files to ${printable(root)}`);
  if (subdirs.includes(ASSETS)) {
    enterRealDir(path.join(root, ASSETS), { create: false });
    pruneHashedFiles(new Set(inDir(ASSETS).map(([name]) => name)));
  }
} catch (error) {
  if (swapped) {
    const why = `old hashed assets were not removed (${codeOf(error)})`;
    console.error(`published, but ${why}; the new page is live, clean assets/ by hand`);
    process.exitCode = 3;
  } else {
    const left = error.replaced
      ? `only ${error.replaced} files of one folder were replaced`
      : 'nothing was replaced in the failing folder';
    console.error(`publish failed: ${codeOf(error)}; ${left}`);
    process.exitCode = 1;
  }
}
