// Test tool: measures what a live update does to each tab (animation restarts, remounts, per-frame movement, frame
// rate, Chrome CPU). Run against e2e/live-server.mjs; prints and writes one JSON report.
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { chromium } from 'playwright';

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:4173';
const OUT = path.resolve(process.env.OUT ?? 'test-results/live-proof.json');
const WINDOW_MS = Number(process.env.WINDOW_S ?? 12) * 1000;
const SETTLE_MS = Number(process.env.SETTLE_S ?? 7) * 1000;
const EVERY_MS = Number(process.env.EVERY_MS ?? 1000);
const REDUCED = process.env.REDUCED === '1';
const WIDTH = Number(process.env.WIDTH ?? 1440);
const HEIGHT = Number(process.env.HEIGHT ?? 900);
const TABS = [
  { id: 'overview', path: '/' }, { id: 'tokens', path: '/tokens' }, { id: 'context', path: '/context' },
  { id: 'sessions', path: '/sessions' }, { id: 'sub-agents', path: '/sub-agents' }, { id: 'fills', path: '/fills' },
  { id: 'guard', path: '/guard' }, { id: 'machine', path: '/machine' }, { id: 'docker', path: '/docker' },
].filter((t) => !process.env.TABS || process.env.TABS.split(',').includes(t.id));
mkdirSync(path.dirname(OUT), { recursive: true });

const browser = await chromium.launch({ channel: 'chrome', headless: process.env.HEADED !== '1' });
const context = await browser.newContext({
  viewport: { width: WIDTH, height: HEIGHT },
  deviceScaleFactor: 1,
  reducedMotion: REDUCED ? 'reduce' : 'no-preference',
});
await context.addInitScript(() => {
  const cm = { armed: false, starts: {}, where: {}, waapi: 0, sse: {}, added: {}, removed: {}, attrs: {}, frames: [] };
  window.__cm = cm;
  const bump = (bag, key) => {
    bag[key] = (bag[key] ?? 0) + 1;
  };
  const nameOf = (el) => `${el.tagName.toLowerCase()}.${String(el.getAttribute('class') ?? '').split(/[\s_]/)[1] ?? ''}`;
  document.addEventListener(
    'animationstart',
    (e) => {
      if (!cm.armed) return;
      bump(cm.starts, e.animationName.replace(/^_|_[A-Za-z0-9]+_\d+$/g, ''));
      bump(cm.where, nameOf(e.target));
    },
    true,
  );
  const animate = Element.prototype.animate;
  Element.prototype.animate = function patched(...args) {
    if (cm.armed) cm.waapi += 1;
    return animate.apply(this, args);
  };
  const Source = window.EventSource;
  window.EventSource = class extends Source {
    constructor(url) {
      super(url);
      for (const n of ['machine', 'docker', 'status', 'sessions', 'activity']) {
        this.addEventListener(n, () => bump(cm.sse, n));
      }
    }
  };
  cm.arm = () => {
    const main = document.querySelector('main');
    cm.main = main;
    cm.tracked = [...main.querySelectorAll('section, article, svg, li, tr, [class*="kpi"], [class*="panel"]')];
    cm.observer = new MutationObserver((records) => {
      for (const r of records) {
        if (r.type === 'attributes') bump(cm.attrs, `${r.target.tagName.toLowerCase()}[${r.attributeName}]`);
        for (const n of r.addedNodes) if (n.nodeType === 1) bump(cm.added, nameOf(n));
        for (const n of r.removedNodes) if (n.nodeType === 1) bump(cm.removed, nameOf(n));
      }
    });
    cm.observer.observe(main, { childList: true, subtree: true, attributes: true });
    cm.sse = {};
    cm.zeros = 0;
    cm.t0 = performance.now();
    const slides = () => [...main.querySelectorAll('svg g[transform^="translate("]')].filter((g) => g.parentElement?.hasAttribute('clip-path'));
    const values = () => [...main.querySelectorAll('[class*="value"], [class*="Value"]')];
    const settled = new Map(values().map((el) => [el, parseFloat(el.textContent.replace(/[^0-9.]/g, ''))]));
    const frame = (now) => {
      if (!cm.armed) return;
      const groups = slides();
      cm.frames.push({
        t: now,
        dx: groups.map((g) => Number(/translate\(([-0-9.]+)/.exec(g.getAttribute('transform'))?.[1] ?? 0)),
        w: groups.map((g) => Number(g.closest('svg')?.getAttribute('width') ?? 0)),
      });
      for (const [el, was] of settled) {
        if (was > 5 && el.isConnected && parseFloat(el.textContent.replace(/[^0-9.]/g, '')) === 0) cm.zeros += 1;
      }
      requestAnimationFrame(frame);
    };
    cm.armed = true;
    requestAnimationFrame(frame);
  };
  cm.read = (everyMs, windowMs) => {
    cm.armed = false;
    cm.observer.disconnect();
    const f = cm.frames;
    const gaps = f.slice(1).map((x, i) => x.t - f[i].t).sort((a, b) => a - b);
    const refresh = gaps[Math.floor(gaps.length / 2)] ?? 0;
    const dropped = gaps.reduce((n, g) => n + Math.max(0, Math.round(g / refresh) - 1), 0);
    const scroll = (f[0]?.dx ?? []).map((_, i) => {
      let worst = 0;
      let jumps = 0;
      let samples = 0;
      for (let k = 1; k < f.length; k += 1) {
        const a = f[k - 1];
        const b = f[k];
        if (a.dx.length !== b.dx.length) continue;
        const step = (everyMs * b.w[i]) / windowMs;
        const arrived = b.dx[i] - a.dx[i] > step / 2;
        if (arrived) samples += 1;
        const move = b.dx[i] - a.dx[i] - (arrived ? step : 0);
        worst = Math.max(worst, Math.abs(move));
        if (Math.abs(move) > 0.5) jumps += 1;
      }
      const stepPx = (everyMs * (f[0].w[i] ?? 0)) / windowMs;
      return { width: f[0].w[i], samples, stepPx: +stepPx.toFixed(2), worstFramePx: +worst.toFixed(2), framesOverHalfPx: jumps };
    });
    return {
      seconds: +((performance.now() - cm.t0) / 1000).toFixed(1),
      sse: cm.sse,
      animationStarts: cm.starts,
      animationTargets: cm.where,
      waapiCalls: cm.waapi,
      nodesAdded: cm.added,
      nodesRemoved: cm.removed,
      attributeWrites: cm.attrs,
      trackedElements: cm.tracked.length,
      trackedStillMounted: cm.tracked.filter((el) => el.isConnected).length,
      sameMain: cm.main === document.querySelector('main'),
      introAttribute: document.querySelector('main').hasAttribute('data-intro'),
      valueHitZeroFrames: cm.zeros,
      runningFiniteAnimations: document.getAnimations().filter((a) => a.effect?.getTiming().iterations !== Infinity).length,
      runningAnimations: document.getAnimations().length,
      frames: f.length,
      refreshMs: +refresh.toFixed(2),
      fps: +((f.length - 1) / ((f[f.length - 1].t - f[0].t) / 1000)).toFixed(1),
      droppedFrames: dropped,
      worstFrameMs: +(gaps[gaps.length - 1] ?? 0).toFixed(1),
      p99FrameMs: +(gaps[Math.floor(gaps.length * 0.99)] ?? 0).toFixed(1),
      scroll,
    };
  };
});

const page = await context.newPage();
const problems = [];
page.on('pageerror', (e) => problems.push(`pageerror: ${e.message.slice(0, 200)}`));
page.on('console', (m) => {
  if (m.type() === 'error') problems.push(`console: ${m.text().slice(0, 200)}`);
});
const cdp = await context.newCDPSession(page);
await cdp.send('Performance.enable');
const system = await browser.newBrowserCDPSession();
const metrics = async () => Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map((m) => [m.name, m.value]));
const processes = async () => {
  const { processInfo } = await system.send('SystemInfo.getProcessInfo');
  const byType = {};
  for (const p of processInfo) byType[p.type] = (byType[p.type] ?? 0) + p.cpuTime;
  return byType;
};

const report = { base: BASE, everyMs: EVERY_MS, reduced: REDUCED, viewport: [WIDTH, HEIGHT], headed: process.env.HEADED === '1', tabs: {}, problems };
for (const tab of TABS) {
  await page.goto(`${BASE}${tab.path}`);
  await page.waitForSelector('main[aria-busy="false"]', { timeout: 20_000 }).catch(() => problems.push(`${tab.id}: still busy`));
  await page.waitForTimeout(SETTLE_MS);
  const m0 = await metrics();
  const p0 = await processes();
  await page.evaluate(() => window.__cm.arm());
  await page.waitForTimeout(WINDOW_MS);
  const result = await page.evaluate(([every, span]) => window.__cm.read(every, span), [EVERY_MS, 300_000]);
  const m1 = await metrics();
  const p1 = await processes();
  const secs = WINDOW_MS / 1000;
  const pct = (a, b) => +(((b - a) / secs) * 100).toFixed(1);
  result.cpuPctOfCore = {
    mainThreadTasks: pct(m0.TaskDuration, m1.TaskDuration),
    script: pct(m0.ScriptDuration, m1.ScriptDuration),
    layout: pct(m0.LayoutDuration, m1.LayoutDuration),
    style: pct(m0.RecalcStyleDuration, m1.RecalcStyleDuration),
    rendererProcess: pct(p0.renderer ?? 0, p1.renderer ?? 0),
    gpuProcess: pct(p0.GPU ?? 0, p1.GPU ?? 0),
    browserProcess: pct(p0.browser ?? 0, p1.browser ?? 0),
  };
  report.tabs[tab.id] = result;
}
await browser.close();
writeFileSync(OUT, JSON.stringify(report, null, 1));
const brief = Object.fromEntries(
  Object.entries(report.tabs).map(([id, r]) => [
    id,
    {
      sse: r.sse, starts: r.animationStarts, waapi: r.waapiCalls, added: r.nodesAdded, removed: r.nodesRemoved,
      mounted: `${r.trackedStillMounted}/${r.trackedElements}`, intro: r.introAttribute, zeros: r.valueHitZeroFrames,
      finiteAnims: r.runningFiniteAnimations, fps: r.fps, dropped: r.droppedFrames, worstMs: r.worstFrameMs,
      cpu: r.cpuPctOfCore, scroll: r.scroll, attrs: r.attributeWrites,
    },
  ]),
);
console.log(JSON.stringify({ problems, tabs: brief }));
