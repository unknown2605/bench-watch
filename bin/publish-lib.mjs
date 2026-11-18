import { randomBytes } from 'node:crypto';
import {
  chmodSync, closeSync, fchmodSync, lstatSync, mkdirSync, openSync, readdirSync, renameSync, rmSync, unlinkSync,
  writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import path from 'node:path';

export const failure = (code) => Object.assign(new Error(code), { code });
export const codeOf = (error) => {
  const code = String(error?.code ?? error?.name ?? 'ERROR');
  return /^[A-Za-z][A-Za-z0-9_]{0,40}$/.test(code) ? code : 'ERROR';
};
export const printable = (text) => String(text).replace(/[^ -~]/g, '?');

// The published web folder comes from the environment, so no machine-specific path is built in; next/ sits under it.
export const nextDir = () => {
  const web = process.env.BENCHWATCH_WEB_DIR;
  if (!web) throw failure('EWEBDIR');
  return path.join(path.resolve(web), 'next');
};

// macOS's own /tmp and /var: root-owned links directly under /, which neither a container nor this user can plant.
const isSystemLink = (dir, st) => st.isSymbolicLink() && st.uid === 0 && path.dirname(dir) === path.parse(dir).root;

function lstatOrNull(name) {
  try {
    return lstatSync(name);
  } catch (error) {
    if (error.code === 'ENOENT') return null;
    throw error;
  }
}

// Containers can swap any part of this path at any moment, so a check by name followed by a write by name can be raced.
// Walking down pins the working directory to each checked folder; getcwd then proves no link was followed on the way.
export function enterRealDir(dir, { create = true } = {}) {
  const home = homedir();
  const base = dir.startsWith(`${home}${path.sep}`) ? home : path.parse(dir).root;
  process.chdir(base);
  for (const part of path.relative(base, dir).split(path.sep).filter(Boolean)) {
    const expected = path.join(process.cwd(), part);
    let st = lstatOrNull(part);
    const created = st === null;
    if (created) {
      if (!create) throw failure('ENOENT');
      mkdirSync(part, { mode: 0o755 });
      st = lstatSync(part);
    }
    const trusted = isSystemLink(expected, st);
    if (st.isSymbolicLink() && !trusted) throw failure('ESYMLINK');
    process.chdir(part);
    if (!trusted && process.cwd() !== expected) throw failure('ESYMLINK');
    if (created) chmodSync('.', 0o755);
  }
}

function writeTemp(file, text, mode) {
  const tmp = `${file}.${randomBytes(8).toString('hex')}.tmp`;
  try {
    const fd = openSync(tmp, 'wx', mode);
    try {
      fchmodSync(fd, mode);
      writeFileSync(fd, text);
    } finally {
      closeSync(fd);
    }
    return tmp;
  } catch (error) {
    try { rmSync(tmp, { force: true }); } catch { /* the caller's original error matters more */ }
    throw error;
  }
}

// Every temp file lands before the first rename, so only a failing rename can leave a mixed page; error.replaced says.
export function publishTogether(entries) {
  const staged = [];
  let replaced = 0;
  try {
    for (const [name, content] of entries) {
      if (lstatOrNull(name)?.isDirectory()) throw failure('EISDIR');
      staged.push([writeTemp(name, content, 0o644), name]);
    }
    for (const [tmp, name] of staged) {
      renameSync(tmp, name);
      replaced += 1;
    }
  } catch (error) {
    for (const [tmp] of staged.slice(replaced)) {
      try { rmSync(tmp, { force: true }); } catch { /* the original error and the count matter more */ }
    }
    throw Object.assign(error, { replaced });
  }
}

// Vite names built files <name>-<8 chars of hash>.<ext>. A hash with a digit or capital marks an old build; one of
// only lowercase letters could be a word ("-document"), so it goes only when the new build ships that name and type.
const HASHED = /^(.+)-([A-Za-z0-9_-]{8})\.(js|css|woff2?|svg|png|jpe?g|webp|avif|gif|ico)(?:\.map)?$/;
const MARKED = /[A-Z0-9]/;

const stemOf = (hit) => `${hit[1]}\0${hit[3]}`;

export function hashedStems(keep) {
  const stems = new Set();
  for (const k of keep) {
    const hit = HASHED.exec(k);
    if (hit) stems.add(stemOf(hit));
  }
  return stems;
}

// stems is hashedStems(keep), passed in when one keep list serves a whole folder.
export function staleHashed(name, keep, stems = hashedStems(keep)) {
  const hit = HASHED.exec(name);
  if (!hit || keep.has(name)) return false;
  return MARKED.test(hit[2]) || stems.has(stemOf(hit));
}

// Only after enterRealDir into assets/: removes regular hashed files that the new build no longer lists. A failed
// delete does not stop the others; the first error is thrown at the end.
export function pruneHashedFiles(keep) {
  let first = null;
  const stems = hashedStems(keep);
  for (const name of readdirSync('.')) {
    try {
      if (!staleHashed(name, keep, stems) || !lstatOrNull(name)?.isFile()) continue;
      unlinkSync(name);
    } catch (error) {
      first ??= error;
    }
  }
  if (first) throw first;
}
