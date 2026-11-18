#!/usr/bin/env node
// Refuses to let bin/generate.sh run code from web/codegen/node_modules unless package-lock.json pins it exactly.
import { readFileSync, realpathSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

// npm-install-checks semantics: `!x` excludes x; with any plain entry the value must be one of them.
function listAllows(list, value) {
  if (list === undefined) return true;
  const entries = typeof list === 'string' ? [list] : list;
  if (entries.length === 1 && entries[0] === 'any') return true;
  let negated = 0;
  let match = false;
  for (const entry of entries) {
    if (entry.startsWith('!')) {
      negated += 1;
      if (entry.slice(1) === value) return false;
    } else if (entry === value) {
      match = true;
    }
  }
  return match || negated === entries.length;
}

export function currentPlatform() {
  const glibc = process.platform === 'linux' && Boolean(process.report?.getReport().header.glibcVersionRuntime);
  const libc = process.platform === 'linux' ? (glibc ? 'glibc' : 'musl') : null;
  return { os: process.platform, cpu: process.arch, libc };
}

// npm skips an optional package built for another platform, so its absence is what a clean `npm ci` leaves.
export function skippedOnPlatform(entry, platform) {
  if (!entry.optional && !entry.devOptional) return false;
  const libcOk = entry.libc === undefined || (platform.libc !== null && listAllows(entry.libc, platform.libc));
  return !(listAllows(entry.os, platform.os) && listAllows(entry.cpu, platform.cpu) && libcOk);
}

export function mismatches(web, platform = currentPlatform()) {
  const lock = JSON.parse(readFileSync(join(web, 'package-lock.json'), 'utf8')).packages;
  let installed;
  try {
    installed = JSON.parse(readFileSync(join(web, 'node_modules/.package-lock.json'), 'utf8')).packages;
  } catch {
    return null;
  }
  const bad = [];
  for (const [key, want] of Object.entries(lock)) {
    if (key === '') continue;
    const have = installed[key];
    let version = null;
    try {
      version = JSON.parse(readFileSync(join(web, key, 'package.json'), 'utf8')).version;
    } catch {}
    if (!have && version === null && skippedOnPlatform(want, platform)) continue;
    if (!have || have.integrity !== want.integrity || have.version !== want.version || version !== want.version) {
      bad.push(key);
    }
  }
  for (const key of Object.keys(installed)) if (!(key in lock)) bad.push(key);
  return bad;
}

// Node resolves links in its own module URL, so argv[1] is resolved too before they are compared.
const started = (() => {
  try {
    return realpathSync(process.argv[1]) === fileURLToPath(import.meta.url);
  } catch {
    return false;
  }
})();

if (started) {
  const bad = mismatches(process.argv[2]);
  if (bad === null) {
    console.error('generate: web/node_modules is missing; run npm ci --ignore-scripts in web/');
    process.exit(1);
  }
  if (bad.length) {
    const shown = bad.slice(0, 5).join(', ');
    console.error(`generate: web/node_modules differs from package-lock.json (${bad.length}): ${shown}`);
    console.error('generate: run npm ci --ignore-scripts in web/');
    process.exit(1);
  }
}
