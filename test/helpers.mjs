import { spawnSync } from 'node:child_process';
import {
  accessSync, constants, existsSync, lstatSync, mkdirSync, mkdtempSync, readdirSync, realpathSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const PROJECT_ROOT = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const FIXTURE_PREFIX = 'benchwatch-test-';
const STALE_FIXTURE_MS = 60 * 60 * 1000;
const PROCESS_HEADROOM = 200;
const REAP_PASSES = 100;
const DEAD_LAUNCHCTL_EXIT = 97;

// A fixture can hold a read-only folder, which rmSync alone cannot empty.
function removeTree(dir) {
  try {
    rmSync(dir, { recursive: true, force: true });
  } catch {
    spawnSync('/bin/chmod', ['-R', 'u+rwX', dir]);
    rmSync(dir, { recursive: true, force: true });
  }
}

const created = [];
process.once('exit', () => {
  for (const dir of created) {
    try { removeTree(dir); } catch { /* the next run sweeps it */ }
  }
});
for (const [signal, code] of [['SIGTERM', 143], ['SIGINT', 130], ['SIGHUP', 129]])
  process.once(signal, () => process.exit(code));

// The exit handler above cannot run after SIGKILL, so the next run removes what such a run left behind.
for (const parent of new Set([tmpdir(), '/tmp'])) {
  for (const name of readdirSync(parent)) {
    if (!name.startsWith(FIXTURE_PREFIX)) continue;
    try {
      const dir = path.join(parent, name);
      if (Date.now() - lstatSync(dir).mtimeMs > STALE_FIXTURE_MS) removeTree(dir);
    } catch { /* another run owns or removed it */ }
  }
}

const lines = (text) => text.split('\n').filter(Boolean);
// The harness's own tools by absolute path: a fake ps first on the runner's PATH could raise the cap or hide a runaway.
const pidsWith = (marker) =>
  lines(spawnSync('/usr/bin/pgrep', ['-f', marker], { encoding: 'utf8' }).stdout).map(Number);

// Only downward from what the run started: its process group, anything naming its fixture, and their children.
function runawayPids(pgid, marker) {
  const table = lines(spawnSync('/bin/ps', ['-axo', 'pid=,ppid=,pgid=,stat='], { encoding: 'utf8' }).stdout)
    .map((row) => row.trim().split(/\s+/))
    .filter((row) => !row[3].startsWith('Z'))
    .map(([pid, ppid, group]) => ({ pid: Number(pid), ppid: Number(ppid), group: Number(group) }));
  const live = new Set(table.map((row) => row.pid));
  const found = new Set(pidsWith(marker).filter((pid) => live.has(pid)));
  for (const row of table) if (pgid > 1 && row.group === pgid) found.add(row.pid);
  for (let size = -1; size !== found.size;) {
    size = found.size;
    for (const row of table) if (found.has(row.ppid)) found.add(row.pid);
  }
  found.delete(process.pid);
  return [...found];
}

function reap(pgid, marker) {
  for (let pass = 0; pass < REAP_PASSES; pass += 1) {
    const pids = runawayPids(pgid, marker);
    if (pids.length === 0) return;
    for (const pid of pids) {
      try { process.kill(pid, 'SIGKILL'); } catch { /* already gone */ }
    }
    pause(20);
  }
  throw new Error(`processes of ${marker} would not die`);
}

// The job's name is the same in tests, so a run reaching the real launchctl would unload the user's real job.
// Every run is checked: a renamed copy or `bash -c 'bash "$S"'` shows no script name to match.
function assertStubbed(env, cwd) {
  // .native: the kernel takes a/link/.. through the link, while plain realpathSync and path.resolve drop link/..
  const kernelPath = (file) => realpathSync.native(file);
  const inFixture = (dir) => {
    if (!path.isAbsolute(dir)) return false;
    try {
      const rel = path.relative(kernelPath(tmpdir()), kernelPath(dir));
      return !path.isAbsolute(rel) && rel.split(path.sep)[0].startsWith(FIXTURE_PREFIX);
    } catch {
      return false;
    }
  };
  const [front] = (env.BENCHWATCH_SYSTEM_PATH ?? '').split(':');
  const launchctl = inFixture(front) ? path.join(kernelPath(front), 'launchctl') : null;
  const stub = launchctl ? lstatSync(launchctl, { throwIfNoEntry: false }) : null;
  const refusal = 'refusing to run the script: no stub launchctl at the front of BENCHWATCH_SYSTEM_PATH';
  if (!stub?.isFile()) throw new Error(refusal);
  // bash skips a file it cannot execute, so mode 0655 would leave the real launchctl next in line.
  try { accessSync(launchctl, constants.X_OK); } catch { throw new Error(refusal); }
  const home = env.HOME && !path.isAbsolute(env.HOME) ? `${cwd ?? process.cwd()}/${env.HOME}` : env.HOME;
  if (!home || !inFixture(home)) throw new Error('refusing to run the script: HOME is not a scratch folder');
}

let deadEnd = null;
function deadSeam() {
  if (deadEnd) return deadEnd;
  const { dir } = sandbox();
  const home = path.join(dir, 'home');
  const reached = path.join(dir, 'reached');
  mkdirSync(home);
  writeFileSync(
    path.join(dir, 'launchctl'),
    `#!/bin/sh\n: > "${reached}"\nexit ${DEAD_LAUNCHCTL_EXIT}\n`,
    { mode: 0o755 },
  );
  deadEnd = { dir, home, reached };
  return deadEnd;
}

// Node on a file starts no script by itself, so the callers that only run our Node programs need no seam of their own.
// A script it spawns anyway, or a launchctl it calls by name, gets one that does nothing and fails the run below.
function runEnv(argv, env) {
  const nodeOnFile = argv[0] === process.execPath && !String(argv[1] ?? '-').startsWith('-');
  if (!nodeOnFile || env.BENCHWATCH_SYSTEM_PATH) return env;
  const { dir, home } = deadSeam();
  const systemPath = '/usr/bin:/bin:/usr/sbin:/sbin';
  return {
    ...env,
    HOME: env.HOME ?? home,
    PATH: `${dir}:${env.PATH ?? systemPath}`,
    BENCHWATCH_SYSTEM_PATH: `${dir}:${systemPath}`,
  };
}

// $1 is the cap, $2 the marker.
const CAP_STEP = 'ulimit -u "$1" && [ "$(ulimit -u)" = "$1" ] && : > "$2" || exit 1';

// A timeout in spawnSync kills only the direct child, and a runaway script can outlive it and keep forking.
export function boundedRun(argv, marker, { headroom = PROCESS_HEADROOM, ...options }) {
  const env = runEnv(argv, options.env ?? process.env);
  assertStubbed(env, options.cwd);
  const running = lines(
    spawnSync('/bin/ps', ['-U', String(process.getuid()), '-o', 'pid='], { encoding: 'utf8' }).stdout,
  ).length;
  const capped = path.join(mkdtempSync(path.join(tmpdir(), FIXTURE_PREFIX)), 'cap-set');
  const script = `${CAP_STEP}; shift 2; exec "$@"`;
  let r;
  try {
    r = spawnSync('/bin/bash', ['-p', '-c', script, '_', String(running + headroom), capped, ...argv], {
      encoding: 'utf8', killSignal: 'SIGKILL', detached: true, ...options, env,
    });
    reap(r.pid, marker);
    if (!existsSync(capped)) {
      const cause = r.error ? `: ${r.error.message}` : '';
      throw new Error(`the process cap could not be set, so nothing was run${cause}`);
    }
  } finally {
    removeTree(path.dirname(capped));
  }
  if (deadEnd && existsSync(deadEnd.reached)) {
    rmSync(deadEnd.reached);
    throw new Error('a Node program reached launchctl with no seam of its own: pass project().env');
  }
  return r;
}

export function sandbox() {
  const dir = mkdtempSync(path.join(tmpdir(), FIXTURE_PREFIX));
  created.push(dir);
  return { dir };
}

export function pause(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}
