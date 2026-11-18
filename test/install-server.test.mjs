import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  chmodSync, copyFileSync, cpSync, existsSync, mkdirSync, readFileSync, symlinkSync, writeFileSync,
} from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { boundedRun, PROJECT_ROOT, sandbox } from './helpers.mjs';

const LABEL = 'com.benchwatch.server';
const RUN_TIMEOUT_MS = 60000;
const escapeRe = (text) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// launchctl print exits 113 for an unknown service, as the real one does; STUB_PRINT_RC fakes any other failure.
const LAUNCHCTL = `#!/bin/bash
echo "$*" >> "$STUB_DIR/calls"
case "$1" in
  print)
    if [ -n "\${STUB_PRINT_RC:-}" ]; then exit "$STUB_PRINT_RC"; fi
    [ -e "$STUB_DIR/loaded" ] || exit 113 ;;
  bootout) rm -f "$STUB_DIR/loaded" ;;
  bootstrap) exit 5 ;;
esac
`;

function serverProject() {
  const box = sandbox();
  const home = path.join(box.dir, 'home');
  const source = path.join(home, 'workspace/tools/benchwatch');
  const stubs = path.join(box.dir, 'stubs');
  for (const dir of [source, stubs, path.join(home, 'shared/web'),
    path.join(home, 'shared/config/pm2'),
    path.join(home, 'shared/config')]) mkdirSync(dir, { recursive: true });
  writeFileSync(path.join(home, 'shared/config/custom_mysql8.cnf'), '');
  cpSync(path.join(PROJECT_ROOT, 'bin'), path.join(source, 'bin'), { recursive: true });
  mkdirSync(path.join(source, 'server'));
  for (const f of ['go.mod', 'go.sum', 'tools.mod', 'tools.sum']) {
    copyFileSync(path.join(PROJECT_ROOT, 'server', f), path.join(source, 'server', f));
  }
  writeFileSync(path.join(stubs, 'launchctl'), LAUNCHCTL);
  chmodSync(path.join(stubs, 'launchctl'), 0o755);
  const base = path.join(home, 'Library/Application Support/benchwatch/server');
  const goDir = path.dirname(spawnSync('/bin/bash', ['-c', 'type -P go'], { encoding: 'utf8' }).stdout.trim() || '/');
  const roots = [path.join(home, 'shared/web'), path.join(home, 'shared/config/pm2'),
    path.join(home, 'shared/config/custom_mysql8.cnf')];
  const env = (extra = {}) => ({
    HOME: home, BENCHWATCH_CONTAINER_PATHS: roots.join(':'), PATH: `${stubs}:${goDir}:${path.dirname(process.execPath)}:/usr/bin:/bin:/usr/sbin`, STUB_DIR: stubs,
    ...extra, BENCHWATCH_SYSTEM_PATH: `${stubs}:/usr/bin:/bin:/usr/sbin:/sbin`,
  });
  const run = (name, extra) => boundedRun(['bash', path.join(source, 'bin', name)], box.dir,
    { timeout: RUN_TIMEOUT_MS, env: env(extra), stdio: ['ignore', 'pipe', 'pipe'] });
  return { home, stubs, base, run, roots, plist: path.join(home, 'Library/LaunchAgents', `${LABEL}.plist`), goDir };
}

function installed(p) {
  mkdirSync(p.base, { recursive: true });
  mkdirSync(path.dirname(p.plist), { recursive: true });
  writeFileSync(path.join(p.base, 'benchwatch'), 'binary');
  writeFileSync(p.plist, 'plist');
  writeFileSync(path.join(p.stubs, 'loaded'), 'plist');
}

test('server uninstall: an unloaded agent loses its plist and binary', () => {
  const p = serverProject();
  installed(p);
  const r = p.run('uninstall-server.sh');
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /removed com\.benchwatch\.server and its binary/);
  assert.equal(existsSync(p.plist), false);
  assert.equal(existsSync(path.join(p.base, 'benchwatch')), false);
});

test('server uninstall: a launchd that cannot be asked is not taken for an unloaded agent', () => {
  const p = serverProject();
  installed(p);
  const r = p.run('uninstall-server.sh', { STUB_PRINT_RC: '5' });
  assert.equal(r.status, 1);
  assert.match(r.stderr, /launchd could not be asked/);
  assert.equal(existsSync(path.join(p.base, 'benchwatch')), true, 'the binary of a server that may run stays');
});

// The wait reaches shell arithmetic, which would run a command substitution hidden in an array index.
for (const wait of ['1+1', 'x', '-5', '999999', 'a[$(touch PWNED)]']) {
  test(`server install: BENCHWATCH_INSTALL_WAIT=${JSON.stringify(wait)} is refused first`, () => {
    const p = serverProject();
    const r = p.run('install-server.sh', { BENCHWATCH_INSTALL_WAIT: wait });
    assert.equal(r.status, 2, r.stderr);
    assert.match(r.stderr, /BENCHWATCH_INSTALL_WAIT must be a whole number/);
    assert.equal(existsSync(p.base), false, 'nothing was created');
    assert.equal(existsSync(path.join(p.home, 'PWNED')), false);
  });
}

test('server install: a Go module cache inside www is refused before the build', (t) => {
  const p = serverProject();
  if (p.goDir === '/') return t.skip('go is not on PATH');
  const cache = path.join(p.home, 'shared/web/gomod');
  mkdirSync(cache);
  const r = p.run('install-server.sh', { GOMODCACHE: cache });
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, new RegExp(`Go's GOMODCACHE folder resolves inside ${escapeRe(p.roots[0])}`));
  assert.equal(existsSync(p.base), false, 'nothing was created');
});

const goEnv = spawnSync('/bin/bash', ['-c', 'go env GOMODCACHE GOCACHE'], { encoding: 'utf8' })
  .stdout.trim().split('\n');

// A server folder that builds cmd/benchwatch, so go gets as far as a real build before bw-db-role is missing.
function buildable(p) {
  const cmd = path.join(p.home, 'workspace/tools/benchwatch/server/cmd/benchwatch');
  mkdirSync(cmd, { recursive: true });
  writeFileSync(path.join(cmd, 'main.go'), 'package main\n\nfunc main() {}\n');
  return { GOMODCACHE: goEnv[0], GOCACHE: goEnv[1] };
}

// Builds every binary from stub sources, so the run reaches the plist without PostgreSQL: bw-db-role and the
// goose of tools.mod (replaced by a local module) do nothing, and the launchctl stub keeps what it was asked to load.
function stubbedBuild(p) {
  const server = path.join(p.home, 'workspace/tools/benchwatch/server');
  const main = 'package main\n\nfunc main() {}\n';
  for (const dir of ['cmd/benchwatch', 'cmd/bw-db-role', 'fakegoose/cmd/goose']) {
    mkdirSync(path.join(server, dir), { recursive: true });
    writeFileSync(path.join(server, dir, 'main.go'), main);
  }
  writeFileSync(path.join(server, 'fakegoose/go.mod'), 'module github.com/pressly/goose/v3\n\ngo 1.27\n');
  writeFileSync(path.join(server, 'tools.mod'), 'module benchwatch/server\n\ngo 1.27\n\n'
    + 'tool github.com/pressly/goose/v3/cmd/goose\n\nrequire github.com/pressly/goose/v3 v3.29.0\n\n'
    + 'replace github.com/pressly/goose/v3 => ./fakegoose\n');
  writeFileSync(path.join(server, 'tools.sum'), '');
  writeFileSync(path.join(p.stubs, 'launchctl'), `#!/bin/bash
echo "$*" >> "$STUB_DIR/calls"
case "$1" in
  print) exit 113 ;;
  bootstrap) cp "$3" "$STUB_DIR/plist-seen"; exit 5 ;;
esac
`);
  return { GOMODCACHE: goEnv[0], GOCACHE: goEnv[1] };
}

test('server install: Node is not needed on PATH and no node argument reaches the plist', (t) => {
  const p = serverProject();
  if (p.goDir === '/' || goEnv.length !== 2) return t.skip('go is not on PATH');
  // go and node often share a folder (Homebrew), so PATH gets a folder holding only a link to go.
  const onlyGo = path.join(p.stubs, '..', 'only-go');
  mkdirSync(onlyGo);
  symlinkSync(path.join(p.goDir, 'go'), path.join(onlyGo, 'go'));
  const noNode = `${p.stubs}:${onlyGo}:/usr/bin:/bin:/usr/sbin`;
  assert.notEqual(spawnSync('/bin/bash', ['-c', 'type -P node'], { env: { PATH: noNode } }).status, 0,
    'node is on the test PATH');
  assert.equal(spawnSync('go', ['version'], { env: { PATH: noNode, HOME: p.home } }).status, 0, 'linked go runs');
  const r = p.run('install-server.sh', { ...stubbedBuild(p), PATH: noNode });
  assert.equal(r.status, 1, r.stderr);
  assert.match(r.stderr, /launchctl could not load the new server/, 'the run must get as far as loading the plist');
  assert.doesNotMatch(r.stderr, /node/i);
  const seen = path.join(p.stubs, 'plist-seen');
  assert.equal(existsSync(seen), true, 'launchctl was never asked to load a plist');
  const plist = readFileSync(seen, 'utf8');
  const args = [...plist.match(/<key>ProgramArguments<\/key>\s*<array>([\s\S]*?)<\/array>/)[1]
    .matchAll(/<string>([^<]*)<\/string>/g)].map((m) => m[1]);
  assert.deepEqual(args.map((a) => path.basename(a)), ['benchwatch', '-passfile', 'pgpass', '-cache', 'cache']);
  assert.ok(args.every((a) => a !== '-node' && path.basename(a) !== 'node'), `a node argument: ${args}`);
  assert.doesNotMatch(readFileSync(path.join(PROJECT_ROOT, 'bin/install-server.sh'), 'utf8'), /-node|-shadow/);
});

test('server install: GOCACHEPROG from the caller never runs during the build', (t) => {
  const p = serverProject();
  if (p.goDir === '/' || goEnv.length !== 2) return t.skip('go is not on PATH');
  const marker = path.join(p.stubs, 'cacheprog-ran');
  writeFileSync(path.join(p.stubs, 'cacheprog'), `#!/bin/bash\n: > '${marker}'\nexit 1\n`);
  chmodSync(path.join(p.stubs, 'cacheprog'), 0o755);
  const r = p.run('install-server.sh', { ...buildable(p), GOCACHEPROG: path.join(p.stubs, 'cacheprog') });
  assert.equal(r.status, 1, r.stderr);
  assert.match(r.stderr, /the server did not build/);
  assert.equal(existsSync(marker), false, 'the caller\'s cache program ran');
});

test('server install: GOTMPDIR from the caller is not where go builds', (t) => {
  const p = serverProject();
  if (p.goDir === '/' || goEnv.length !== 2) return t.skip('go is not on PATH');
  const r = p.run('install-server.sh', { ...buildable(p), GOTMPDIR: path.join(p.home, 'shared/web/missing') });
  assert.equal(r.status, 1, r.stderr);
  assert.doesNotMatch(r.stderr, /creating work dir/);
});

for (const [name, tmp, message] of [
  ['a TMPDIR inside a container path', 'shared/web/tmp', 'inside'],
  ['a relative TMPDIR', 'relative/tmp', /TMPDIR is not an absolute path/],
]) {
  test(`server install: ${name} is refused before go runs`, (t) => {
    const p = serverProject();
    if (p.goDir === '/') return t.skip('go is not on PATH');
    const dir = tmp.startsWith('relative') ? tmp : path.join(p.home, tmp);
    if (dir.startsWith('/')) mkdirSync(dir, { recursive: true });
    const r = p.run('install-server.sh', { TMPDIR: dir });
    assert.equal(r.status, 2, r.stderr);
    assert.match(r.stderr, message === 'inside'
      ? new RegExp(`the temporary folder \\(TMPDIR\\) resolves inside ${escapeRe(p.roots[0])}`) : message);
    assert.equal(existsSync(p.base), false, 'nothing was created');
  });
}

test('server install: a listed container path that does not exist is refused first', () => {
  const p = serverProject();
  const missing = path.join(p.home, 'workspace/missing');
  const r = p.run('install-server.sh', { BENCHWATCH_CONTAINER_PATHS: `${p.roots[0]}:${missing}` });
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, new RegExp(`refusing: ${escapeRe(missing)} does not exist`));
  assert.equal(existsSync(p.base), false, 'nothing was created');
});

test('server install: a source inside the second listed path is refused', () => {
  const p = serverProject();
  const r = p.run('install-server.sh', { BENCHWATCH_CONTAINER_PATHS: `${p.roots[1]}:${path.dirname(p.home)}` });
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, new RegExp(`the source resolves inside ${escapeRe(path.dirname(p.home))}`));
});

for (const [name, value] of [['unset', undefined], ['empty', ''], ['only a colon', ':'], ['only colons', '::']]) {
  test(`server install: a container path list that is ${name} is refused`, () => {
    const p = serverProject();
    const r = p.run('install-server.sh', { BENCHWATCH_CONTAINER_PATHS: value });
    assert.equal(r.status, 2, r.stderr);
    assert.match(r.stderr, /BENCHWATCH_CONTAINER_PATHS is unset or lists no path.*exactly none/);
    assert.equal(existsSync(p.base), false, 'nothing was created');
  });
}

test('server install: none skips the container path check and says so', () => {
  const p = serverProject();
  const r = p.run('install-server.sh', { BENCHWATCH_CONTAINER_PATHS: 'none', BENCHWATCH_INSTALL_WAIT: 'x' });
  assert.match(r.stderr, /BENCHWATCH_CONTAINER_PATHS=none: no container-writable paths are checked/);
  assert.match(r.stderr, /BENCHWATCH_INSTALL_WAIT must be a whole number/, 'the run went past the list check');
  assert.equal(r.stderr.match(/no container-writable paths are checked/g).length, 1);
});

test('server install: a list with a newline is refused, not cut at the first line', () => {
  const p = serverProject();
  const r = p.run('install-server.sh', { BENCHWATCH_CONTAINER_PATHS: `${p.roots[0]}\n${p.roots[1]}` });
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, /BENCHWATCH_CONTAINER_PATHS holds a newline/);
  assert.equal(existsSync(p.base), false, 'nothing was created');
});

test('server install: a link to / is refused', () => {
  const p = serverProject();
  const link = path.join(p.home, 'root-link');
  symlinkSync('/', link);
  const r = p.run('install-server.sh', { BENCHWATCH_CONTAINER_PATHS: `${p.roots[0]}:${link}` });
  assert.equal(r.status, 2, r.stderr);
  assert.match(r.stderr, /lists \/ , which would cover the whole disk/);
});

for (const entry of ['/', '//', '/.', '/..']) {
  test(`server install: the entry ${entry} is refused`, () => {
    const p = serverProject();
    const r = p.run('install-server.sh', { BENCHWATCH_CONTAINER_PATHS: `${p.roots[0]}:${entry}` });
    assert.equal(r.status, 2, r.stderr);
    assert.match(r.stderr, /lists \/ , which would cover the whole disk/);
  });
}

// The listed root is a single file, as a bind of one config file is; a link to it must count as inside it.
test('server install: a temporary folder that resolves to a listed single file is refused', (t) => {
  const p = serverProject();
  if (p.goDir === '/') return t.skip('go is not on PATH');
  const file = p.roots[2];
  const link = path.join(p.home, 'tmp-link');
  symlinkSync(file, link);
  for (const tmp of [file, link]) {
    const r = p.run('install-server.sh', { BENCHWATCH_CONTAINER_PATHS: file, TMPDIR: tmp });
    assert.equal(r.status, 2, r.stderr);
    assert.match(r.stderr, new RegExp(`the temporary folder \\(TMPDIR\\) resolves inside ${escapeRe(file)}`));
  }
});

// Runs one function of install-server.sh as written there, with stubs first on PATH.
function shellFunction(name, args, env = {}) {
  const lines = readFileSync(path.join(PROJECT_ROOT, 'bin/install-server.sh'), 'utf8').split('\n');
  const first = lines.findIndex((l) => l.startsWith(`${name}() {`));
  assert.notEqual(first, -1, `install-server.sh has no ${name}()`);
  const last = lines[first].trimEnd().endsWith('}') ? first : lines.findIndex((l, i) => i > first && l === '}');
  const body = lines.slice(first, last + 1).join('\n');
  return spawnSync('/bin/bash', ['-c', `set -euo pipefail\n${body}\n${name} "$@"`, name, ...args],
    { encoding: 'utf8', env: { PATH: '/usr/bin:/bin', ...env } });
}

test('server install: a wait with leading zeros is read in base 10', () => {
  for (const [wait, want] of [['08', '8'], ['09', '9'], ['010', '10'], ['120', '120'], ['00000', '0']]) {
    const r = shellFunction('whole_seconds', [wait]);
    assert.equal(r.status, 0, `${wait}: ${r.stderr}`);
    assert.equal(r.stdout.trim(), want, wait);
  }
  for (const wait of ['', '8x', '123456', '-1']) {
    assert.notEqual(shellFunction('whole_seconds', [wait]).status, 0, wait);
  }
});

test('server install: the listener counts only when exactly one process listens', () => {
  const p = serverProject();
  writeFileSync(path.join(p.stubs, 'lsof'), '#!/bin/bash\nprintf "%b" "$STUB_PIDS"\n');
  chmodSync(path.join(p.stubs, 'lsof'), 0o755);
  for (const [pids, want] of [['4242\\n', '4242'], ['4242\\n4343\\n', ''], ['', '']]) {
    const r = shellFunction('listener_pid', [], { PATH: `${p.stubs}:/usr/bin:/bin`, STUB_PIDS: pids });
    assert.equal(r.status, 0, r.stderr);
    assert.equal(r.stdout.trim(), want, JSON.stringify(pids));
  }
});
