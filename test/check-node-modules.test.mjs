import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { mismatches, skippedOnPlatform } from '../bin/check-node-modules.mjs';

const darwinArm = { os: 'darwin', cpu: 'arm64', libc: null };
const linuxX64 = { os: 'linux', cpu: 'x64', libc: 'glibc' };

function fixture(t, lock, installed, onDisk) {
  const web = mkdtempSync(join(tmpdir(), 'cm-node-modules-'));
  t.after(() => rmSync(web, { recursive: true, force: true }));
  writeFileSync(join(web, 'package-lock.json'), JSON.stringify({ packages: { '': {}, ...lock } }));
  mkdirSync(join(web, 'node_modules'));
  writeFileSync(join(web, 'node_modules/.package-lock.json'), JSON.stringify({ packages: installed }));
  for (const [key, version] of Object.entries(onDisk)) {
    mkdirSync(join(web, key), { recursive: true });
    writeFileSync(join(web, key, 'package.json'), JSON.stringify({ version }));
  }
  return web;
}

const tool = { version: '1.0.0', integrity: 'sha512-a', dev: true };
const linuxOnly = { version: '0.25.0', integrity: 'sha512-b', dev: true, optional: true, os: ['linux'], cpu: ['x64'] };

test('an optional package for another platform may be absent', (t) => {
  const web = fixture(t, { 'node_modules/tool': tool, 'node_modules/@esbuild/linux-x64': linuxOnly },
    { 'node_modules/tool': tool }, { 'node_modules/tool': '1.0.0' });
  assert.deepEqual(mismatches(web, darwinArm), []);
  assert.deepEqual(mismatches(web, linuxX64), ['node_modules/@esbuild/linux-x64'], 'needed on its own platform');
});

test('a required, changed, half-installed or extra package is refused', (t) => {
  const missing = fixture(t, { 'node_modules/tool': tool }, {}, {});
  assert.deepEqual(mismatches(missing, darwinArm), ['node_modules/tool']);
  const changed = fixture(t, { 'node_modules/tool': tool }, { 'node_modules/tool': { ...tool, integrity: 'sha512-x' } },
    { 'node_modules/tool': '1.0.0' });
  assert.deepEqual(mismatches(changed, darwinArm), ['node_modules/tool']);
  const onDiskOnly = fixture(t, { 'node_modules/@esbuild/linux-x64': linuxOnly }, {},
    { 'node_modules/@esbuild/linux-x64': '0.25.0' });
  assert.deepEqual(mismatches(onDiskOnly, darwinArm), ['node_modules/@esbuild/linux-x64']);
  const extra = fixture(t, {}, { 'node_modules/stray': tool }, {});
  assert.deepEqual(mismatches(extra, darwinArm), ['node_modules/stray']);
});

test('no hidden lockfile means no install', (t) => {
  const web = fixture(t, {}, {}, {});
  rmSync(join(web, 'node_modules/.package-lock.json'));
  assert.equal(mismatches(web, darwinArm), null);
});

test('platform rules follow npm', () => {
  const opt = (fields) => ({ optional: true, ...fields });
  assert.equal(skippedOnPlatform(opt({ os: ['!win32'] }), darwinArm), false);
  assert.equal(skippedOnPlatform(opt({ os: ['!darwin'] }), darwinArm), true);
  assert.equal(skippedOnPlatform(opt({ cpu: ['x64'] }), darwinArm), true);
  assert.equal(skippedOnPlatform(opt({ libc: ['glibc'] }), darwinArm), true, 'libc applies on Linux only');
  assert.equal(skippedOnPlatform(opt({ libc: ['musl'] }), linuxX64), true);
  assert.equal(skippedOnPlatform(opt({ os: ['linux'], libc: ['glibc'] }), linuxX64), false);
  assert.equal(skippedOnPlatform({ os: ['linux'] }, darwinArm), false, 'a required package is never skipped');
  assert.equal(skippedOnPlatform({ devOptional: true, os: ['linux'] }, darwinArm), true);
});

test('the check also runs when started through a symbolic link', (t) => {
  const web = fixture(t, { 'node_modules/tool': tool }, {}, {});
  rmSync(join(web, 'node_modules/.package-lock.json'));
  const links = mkdtempSync(join(tmpdir(), 'cm-node-modules-link-'));
  t.after(() => rmSync(links, { recursive: true, force: true }));
  const bin = fileURLToPath(new URL('../bin/', import.meta.url));
  symlinkSync(bin, join(links, 'bin'));
  symlinkSync(join(bin, 'check-node-modules.mjs'), join(links, 'check.mjs'));
  for (const script of [join(links, 'bin', 'check-node-modules.mjs'), join(links, 'check.mjs')]) {
    const run = spawnSync(process.execPath, [script, web], { encoding: 'utf8', timeout: 30000 });
    assert.equal(run.status, 1, 'a missing install must fail the check');
    assert.match(run.stderr, /node_modules is missing/);
  }
});
