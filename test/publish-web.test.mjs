import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { test } from 'node:test';

const SCRIPT = new URL('../bin/publish-web.mjs', import.meta.url).pathname;

function setup() {
  const base = mkdtempSync(path.join(tmpdir(), 'cm-publish-'));
  const dist = path.join(base, 'dist');
  mkdirSync(path.join(dist, 'assets'), { recursive: true });
  writeFileSync(path.join(dist, 'index.html'), '<html>new</html>');
  writeFileSync(path.join(dist, 'assets', 'app-NEW.js'), 'new');
  return { base, dist, out: path.join(base, 'out', 'next') };
}
const run = (dist, out) =>
  spawnSync(process.execPath, [SCRIPT, dist], { env: { ...process.env, BENCHWATCH_WEB_DIR: path.dirname(out) },
    encoding: 'utf8' });

test('refuses with a clear message when BENCHWATCH_WEB_DIR is unset', () => {
  const { base, dist } = setup();
  try {
    const env = { ...process.env };
    delete env.BENCHWATCH_WEB_DIR;
    const result = spawnSync(process.execPath, [SCRIPT, dist], { env, encoding: 'utf8' });
    assert.equal(result.status, 2, result.stderr);
    assert.match(result.stderr, /BENCHWATCH_WEB_DIR is not set/);
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

test('publishes the build, replaces the page and prunes stale hashed assets', () => {
  const { base, dist, out } = setup();
  try {
    mkdirSync(path.join(out, 'assets'), { recursive: true });
    writeFileSync(path.join(out, 'assets', 'app-OLD00001.js'), 'old');
    const result = run(dist, out);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(readFileSync(path.join(out, 'index.html'), 'utf8'), '<html>new</html>');
    assert.deepEqual(readdirSync(path.join(out, 'assets')), ['app-NEW.js']);
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

test('refuses a symlinked target folder and writes nothing through it', () => {
  const { base, dist, out } = setup();
  try {
    const elsewhere = path.join(base, 'elsewhere');
    mkdirSync(elsewhere);
    mkdirSync(path.dirname(out), { recursive: true });
    symlinkSync(elsewhere, out);
    const result = run(dist, out);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /ESYMLINK/);
    assert.deepEqual(readdirSync(elsewhere), []);
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

test('refuses a link inside the build and a build without index.html', () => {
  const { base, dist, out } = setup();
  try {
    const target = path.join(base, 'outside.txt');
    writeFileSync(target, 'outside');
    symlinkSync(target, path.join(dist, 'assets', 'link.js'));
    assert.match(run(dist, out).stderr, /ESYMLINK/);
    assert.equal(existsSync(path.join(out, 'index.html')), false);
    rmSync(path.join(dist, 'assets', 'link.js'));
    rmSync(path.join(dist, 'index.html'));
    assert.match(run(dist, out).stderr, /ENOINDEX/);
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

const ASSET = 'app-AbCd1234.js';
const OLD = 'app-ZyXw9876.js';

test('never removes data.json or unhashed files, only old hashed assets', () => {
  const { base, dist, out } = setup();
  try {
    mkdirSync(path.join(out, 'assets'), { recursive: true });
    writeFileSync(path.join(out, 'data.json'), '{"keep":true}');
    writeFileSync(path.join(out, 'notes.txt'), 'keep');
    writeFileSync(path.join(out, 'assets', 'readme.txt'), 'keep');
    writeFileSync(path.join(out, 'assets', OLD), 'old');
    writeFileSync(path.join(dist, 'assets', ASSET), 'new');
    const result = run(dist, out);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(readFileSync(path.join(out, 'data.json'), 'utf8'), '{"keep":true}');
    assert.equal(existsSync(path.join(out, 'notes.txt')), true);
    assert.deepEqual(readdirSync(path.join(out, 'assets')).sort(), [ASSET, 'app-NEW.js', 'readme.txt'].sort());
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

test('a directory named like a build file stops the publish with the old page and assets intact', () => {
  for (const blocked of [['assets', 'app-NEW.js'], ['index.html']]) {
    const { base, dist, out } = setup();
    try {
      mkdirSync(path.join(out, 'assets'), { recursive: true });
      writeFileSync(path.join(out, 'index.html'), '<html>old</html>');
      writeFileSync(path.join(out, 'assets', OLD), 'old');
      if (blocked.length === 1) rmSync(path.join(out, 'index.html'));
      mkdirSync(path.join(out, ...blocked));
      if (blocked.length === 1) writeFileSync(path.join(out, 'index.html', 'x'), 'x');
      const result = run(dist, out);
      assert.equal(result.status, 1);
      assert.match(result.stderr, /EISDIR/);
      assert.equal(readFileSync(path.join(out, 'assets', OLD), 'utf8'), 'old');
      if (blocked.length === 2) assert.equal(readFileSync(path.join(out, 'index.html'), 'utf8'), '<html>old</html>');
    } finally {
      rmSync(base, { recursive: true, force: true });
    }
  }
});

test('refuses a symlinked build folder', () => {
  const { base, dist, out } = setup();
  try {
    const link = path.join(base, 'dist-link');
    symlinkSync(dist, link);
    const result = run(link, out);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /ESYMLINK/);
    assert.equal(existsSync(path.join(out, 'index.html')), false);
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

const FAIL_UNLINK = `import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
fs.unlinkSync = () => { throw Object.assign(new Error('blocked'), { code: 'EPERM' }); };
syncBuiltinESMExports();
`;

test('exits 3 when the page is published but an old asset cannot be removed', () => {
  const { base, dist, out } = setup();
  try {
    mkdirSync(path.join(out, 'assets'), { recursive: true });
    writeFileSync(path.join(out, 'assets', OLD), 'old');
    const preload = path.join(base, 'fail-unlink.mjs');
    writeFileSync(preload, FAIL_UNLINK);
    const result = spawnSync(process.execPath, ['--import', preload, SCRIPT, dist], {
      env: { ...process.env, BENCHWATCH_WEB_DIR: path.dirname(out) },
      encoding: 'utf8',
    });
    assert.equal(result.status, 3, result.stderr);
    assert.match(result.stderr, /published, but old hashed assets were not removed \(EPERM\)/);
    assert.equal(readFileSync(path.join(out, 'index.html'), 'utf8'), '<html>new</html>');
    assert.equal(existsSync(path.join(out, 'assets', OLD)), true);
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

test('prunes hashed .js.map files but keeps plain words and other extensions', () => {
  const { base, dist, out } = setup();
  try {
    const keep = ['my-document.pdf', 'my-document.js', 'notes-abcdefgh.js', 'app-ZyXw9876.json', 'app-ZyXw9876.js.txt'];
    const gone = [OLD, `${OLD}.map`, 'style-Zz9Yy8Xx.css'];
    mkdirSync(path.join(out, 'assets'), { recursive: true });
    for (const name of [...keep, ...gone]) writeFileSync(path.join(out, 'assets', name), 'x');
    const result = run(dist, out);
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(readdirSync(path.join(out, 'assets')).sort(), [...keep, 'app-NEW.js'].sort());
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

test('prunes an old build whose hash has only lowercase letters when the new build ships that name and type', () => {
  const { base, dist, out } = setup();
  try {
    writeFileSync(path.join(dist, 'assets', 'index-Qw3rTy12.js'), 'new');
    const keep = ['notes-abcdefgh.js', 'index-abcdefgh.css', 'index.js'];
    const gone = ['index-qwertyui.js', 'index-qwertyui.js.map', 'index-ab_cd-ef.js'];
    mkdirSync(path.join(out, 'assets'), { recursive: true });
    for (const name of [...keep, ...gone]) writeFileSync(path.join(out, 'assets', name), 'x');
    const result = run(dist, out);
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(readdirSync(path.join(out, 'assets')).sort(), [...keep, 'app-NEW.js', 'index-Qw3rTy12.js'].sort());
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

test('CM9-L8: an old all-lowercase hash goes only for the exact stem and type the new build ships', () => {
  const { base, dist, out } = setup();
  try {
    writeFileSync(path.join(dist, 'assets', 'theme-kLm4nOp5.css'), 'new');
    const keep = ['theme-abcdefgh.js', 'theme-abcdefgh.svg', 'other-abcdefgh.css'];
    const gone = ['theme-qwertyui.css', 'theme-qwertyui.css.map'];
    mkdirSync(path.join(out, 'assets'), { recursive: true });
    for (const name of [...keep, ...gone]) writeFileSync(path.join(out, 'assets', name), 'x');
    const result = run(dist, out);
    assert.equal(result.status, 0, result.stderr);
    const left = readdirSync(path.join(out, 'assets')).sort();
    assert.deepEqual(left, [...keep, 'app-NEW.js', 'theme-kLm4nOp5.css'].sort());
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});

const FAIL_FIRST_UNLINK = `import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
const unlink = fs.unlinkSync;
let failed = false;
fs.unlinkSync = (name) => {
  if (failed) return unlink(name);
  failed = true;
  throw Object.assign(new Error('blocked'), { code: 'EPERM' });
};
syncBuiltinESMExports();
`;

test('a failed delete does not stop the prune, and the run still exits 3', () => {
  const { base, dist, out } = setup();
  try {
    const old = ['app-AAAA0001.js', 'app-BBBB0002.js', 'app-CCCC0003.js', 'app-DDDD0004.js'];
    mkdirSync(path.join(out, 'assets'), { recursive: true });
    for (const name of old) writeFileSync(path.join(out, 'assets', name), 'old');
    const preload = path.join(base, 'fail-first-unlink.mjs');
    writeFileSync(preload, FAIL_FIRST_UNLINK);
    const result = spawnSync(process.execPath, ['--import', preload, SCRIPT, dist], {
      env: { ...process.env, BENCHWATCH_WEB_DIR: path.dirname(out) },
      encoding: 'utf8',
    });
    assert.equal(result.status, 3, result.stderr);
    assert.match(result.stderr, /old hashed assets were not removed \(EPERM\)/);
    const left = readdirSync(path.join(out, 'assets')).filter((n) => old.includes(n));
    assert.equal(left.length, 1, `left: ${left.length}`);
  } finally {
    rmSync(base, { recursive: true, force: true });
  }
});
