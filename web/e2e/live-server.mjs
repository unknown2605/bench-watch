// Test tool: serves a built page on 127.0.0.1 with a synthetic /api/live at EVERY_MS, so live motion can be measured
// without touching the installed agent. Other GETs are read from the agent; numbers and labels only, nothing stored.
import { createReadStream, statSync } from 'node:fs';
import { createServer } from 'node:http';
import path from 'node:path';

const DIST = path.resolve(process.env.DIST ?? 'dist');
const PORT = Number(process.env.PORT ?? 4173);
const EVERY_MS = Number(process.env.EVERY_MS ?? 1000);
const AGENT = process.env.AGENT ?? 'http://127.0.0.1:8790';
const DRIFT_MS = Number(process.env.DRIFT_MS ?? 3000);
const LATE = process.env.LATE === '1';
const WINDOW_MS = 300_000;
const CSP =
  "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; font-src 'self'";
const TYPES = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json',
  '.svg': 'image/svg+xml', '.woff2': 'font/woff2', '.woff': 'font/woff', '.png': 'image/png', '.ico': 'image/x-icon',
};
const TRANSCRIPT = ['/api/overview', '/api/tokens', '/api/context', '/api/sessions', '/api/subagents', '/api/fills'];
const FIXED_KEY = /limit|version|interval|hour|day|index|pct|share|ratio|cores|cpus/i;

const agentJson = async (url) => {
  const res = await fetch(`${AGENT}${url}`, { signal: AbortSignal.timeout(10_000) });
  return { status: res.status, body: await res.json() };
};
const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));
const wave = (t, period, phase = 0) => Math.sin((t / period) * Math.PI * 2 + phase);
const round1 = (v) => Math.round(v * 10) / 10;

let machineBase = null;
let dockerBase = null;
const machineRing = [];
const dockerRing = [];

function machineAt(ms) {
  const t = ms / 1000;
  const now = machineBase.now;
  const cores = now.cores.map((c, i) => ({
    ...c,
    busyPct: round1(clamp(38 + 30 * wave(t, 11 + i, i) + 12 * wave(t, 3.1, i * 2), 1, 99)),
  }));
  const interfaces = now.interfaces.map((n, i) => ({
    ...n,
    rxBps: Math.round(2_400_000 * (1.1 + wave(t, 17, i)) + 300_000 * (1 + wave(t, 2.3, i))),
    txBps: Math.round(600_000 * (1.1 + wave(t, 13, i + 1)) + 90_000 * (1 + wave(t, 2.9, i))),
  }));
  const apps = now.apps
    .map((a, i) => ({ ...a, cpuPct: round1(clamp(a.cpuPct + (i < 2 ? 25 * wave(t, 12, i * Math.PI) : wave(t, 5, i)), 0, 800)) }))
    .sort((a, b) => b.cpuPct - a.cpuPct);
  return {
    ...now,
    at: new Date(ms).toISOString(),
    cores,
    interfaces,
    apps,
    cpuBusyPct: round1(cores.reduce((s, c) => s + c.busyPct, 0) / Math.max(1, cores.length)),
    diskIo: now.diskIo && {
      readBps: Math.round(5_000_000 * (1.05 + wave(t, 9))),
      writeBps: Math.round(9_000_000 * (1.05 + wave(t, 14, 1))),
    },
    load: now.load && { ...now.load, one: round1(clamp(now.load.one + wave(t, 20), 0, 99)) },
    memory: now.memory && { ...now.memory, usedBytes: Math.round(now.memory.usedBytes * (1 + 0.03 * wave(t, 40))) },
  };
}

function dockerAt(ms) {
  const t = ms / 1000;
  const now = dockerBase.now;
  const containers = now.containers.map((c, i) => ({
    ...c,
    cpuPct: round1(clamp(4 + 3.5 * wave(t, 8 + (i % 5), i) + 1.5 * wave(t, 2.7, i), 0, 100)),
    memBytes: Math.round(c.memBytes * (1 + 0.04 * wave(t, 30, i))),
    rxBps: Math.round(40_000 * (1.1 + wave(t, 10, i))),
    txBps: Math.round(25_000 * (1.1 + wave(t, 12, i))),
  }));
  const vm = {
    ...now.vm,
    cpuPct: round1(containers.reduce((s, c) => s + c.cpuPct, 0) / Math.max(1, now.vm.cpus || 1)),
    memUsedBytes: containers.reduce((s, c) => s + c.memBytes, 0),
  };
  return { ...now, at: new Date(ms).toISOString(), containers, vm };
}

function push(ring, snap) {
  ring.push(snap);
  const from = Date.parse(snap.at) - WINDOW_MS;
  while (ring.length && Date.parse(ring[0].at) < from) ring.shift();
}

function machineLive() {
  const series = machineRing.map((s) => ({
    at: s.at, cpuBusyPct: s.cpuBusyPct, cpuBusyMaxPct: s.cpuBusyPct, load1: s.load?.one ?? null,
    memUsedBytes: s.memory?.usedBytes ?? null, swapUsedBytes: s.memory?.swapUsedBytes ?? null,
    diskReadBps: s.diskIo?.readBps ?? null, diskWriteBps: s.diskIo?.writeBps ?? null,
  }));
  const now = machineRing[machineRing.length - 1];
  const coreSeries = now.cores.map((c, i) => ({
    core: c.core, kind: c.kind, points: machineRing.map((s) => ({ at: s.at, busyPct: s.cores[i].busyPct })),
  }));
  const interfaceSeries = now.interfaces.map((n, i) => {
    const points = machineRing.map((s) => ({ at: s.at, rxBps: s.interfaces[i].rxBps, txBps: s.interfaces[i].txBps }));
    const top = points.reduce((best, p) => (p.rxBps >= best.rxBps ? p : best));
    return { interface: n.interface, peakRxBps: top.rxBps, peakRxAt: top.at, points };
  });
  return { ...machineBase, now, coverageFrom: machineRing[0].at, series, coreSeries, interfaceSeries };
}

function dockerLive() {
  const now = dockerRing[dockerRing.length - 1];
  const vmSeries = dockerRing.map((s) => ({ at: s.at, cpuPct: s.vm.cpuPct, memUsedBytes: s.vm.memUsedBytes }));
  const containerSeries = now.containers.map((c, i) => ({
    container: c.container,
    stack: c.stack,
    points: dockerRing.map((s) => {
      const k = s.containers[i];
      return { at: s.at, cpuPct: k.cpuPct, memBytes: k.memBytes, rxBps: k.rxBps, txBps: k.txBps };
    }),
  }));
  return { ...dockerBase, now, coverageFrom: dockerRing[0].at, vmSeries, containerSeries };
}

/** Scales the larger numbers of a transcript answer by a slowly moving factor, so every refetch shows a change. */
function drift(value, factor, key = '') {
  if (Array.isArray(value)) return value.map((v) => drift(v, factor, key));
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, drift(v, factor, k)]));
  }
  if (typeof value !== 'number' || value < 10 || FIXED_KEY.test(key)) return value;
  return Number.isInteger(value) ? Math.round(value * factor) : value * factor;
}

const clients = new Set();
function send(type, data) {
  const frame = `event: ${type}\ndata: ${JSON.stringify({ type, data })}\n\n`;
  for (const res of clients) res.write(frame);
}

let tick = 0;
function sample() {
  tick += 1;
  if (LATE && tick % 20 >= 18) return;
  const ms = Date.now();
  const machine = machineAt(ms);
  const docker = dockerAt(ms);
  push(machineRing, machine);
  push(dockerRing, docker);
  send('machine', machine);
  send('docker', docker);
}

function serveFile(req, res) {
  let clean;
  try {
    clean = path.normalize(decodeURIComponent(new URL(req.url, 'http://x').pathname));
  } catch {
    res.writeHead(400).end();
    return;
  }
  let file = path.join(DIST, clean);
  if (file !== DIST && !file.startsWith(DIST + path.sep)) {
    res.writeHead(400).end();
    return;
  }
  try {
    if (!statSync(file).isFile()) throw new Error('not a file');
  } catch {
    if (path.extname(clean)) {
      res.writeHead(404).end();
      return;
    }
    file = path.join(DIST, 'index.html');
  }
  res.writeHead(200, {
    'Content-Type': TYPES[path.extname(file)] ?? 'application/octet-stream',
    'Content-Security-Policy': CSP,
    'Cache-Control': 'no-store',
  });
  createReadStream(file).pipe(res);
}

async function serveApi(req, res) {
  const url = new URL(req.url, 'http://x');
  if (url.pathname === '/api/live') {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', Connection: 'keep-alive' });
    res.write(': open\n\n');
    clients.add(res);
    req.on('close', () => clients.delete(res));
    return;
  }
  const live = url.searchParams.get('range') === 'live' && !url.searchParams.get('stack');
  let status = 200;
  let body;
  if (live && url.pathname === '/api/machine') body = machineLive();
  else if (live && url.pathname === '/api/docker') body = dockerLive();
  else {
    ({ status, body } = await agentJson(`${url.pathname}${url.search}`));
    if (status === 200 && DRIFT_MS > 0 && TRANSCRIPT.includes(url.pathname)) {
      body = drift(body, 1 + 0.05 * wave(Date.now(), DRIFT_MS * 14));
    }
  }
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  res.end(JSON.stringify(body));
}

machineBase = (await agentJson('/api/machine?range=live')).body;
dockerBase = (await agentJson('/api/docker?range=live')).body;
if (!machineBase?.now || !dockerBase?.now) throw new Error('the agent gave no machine or docker snapshot to start from');
const started = Date.now();
for (let ms = started - WINDOW_MS + EVERY_MS; ms < started; ms += EVERY_MS) {
  push(machineRing, machineAt(ms));
  push(dockerRing, dockerAt(ms));
}

const server = createServer((req, res) => {
  if (req.method !== 'GET') {
    res.writeHead(405).end();
    return;
  }
  if (req.url.startsWith('/api/')) {
    serveApi(req, res).catch(() => {
      if (!res.headersSent) res.writeHead(502, { 'Content-Type': 'application/json' });
      res.end('{"code":"mock_upstream"}');
    });
    return;
  }
  serveFile(req, res);
});
server.listen(PORT, '127.0.0.1', () => {
  setInterval(sample, EVERY_MS);
  if (DRIFT_MS > 0) setInterval(() => send('activity', { at: new Date().toISOString() }), DRIFT_MS);
  console.log(`live-server: ${DIST} on http://127.0.0.1:${PORT}, samples every ${EVERY_MS} ms, pid ${process.pid}`);
});
