import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { chromium } from 'playwright';

// Usage: node e2e/pieces-shots.mjs <out-dir> [approved-frames-dir]; needs `npm run preview:gallery` on BASE.
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:4173';
const OUT = path.resolve(process.argv[2] ?? 'test-results/pieces');
const FRAMES = process.argv[3] ? path.resolve(process.argv[3]) : null;
mkdirSync(OUT, { recursive: true });

const PIECES = ['overview', 'tokens', 'context', 'sessions', 'subagents', 'fills', 'guard', 'machine', 'docker'];
const WIDTHS = [768, 1024, 1440, 1920, 2560, 3840];
const url = (piece, extra = '') => `${BASE}/_gallery?view=pieces&piece=${piece}${extra}`;
const problems = [];
const report = { audit: {}, scroll: {}, fps: {}, reduced: {}, hidden: {}, shots: [], problems };
const byWidth = {};

function listen(page) {
  page.on('console', (m) => {
    if (m.type() === 'error' || m.type() === 'warning') problems.push(`${m.type()}: ${m.text().slice(0, 200)}`);
  });
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message.slice(0, 200)}`));
  page.on('request', (r) => {
    const u = r.url();
    if (![BASE, 'data:', 'file:'].some((p) => u.startsWith(p))) problems.push(`external request: ${u.slice(0, 120)}`);
  });
}

async function settle(page) {
  await page.evaluate(async () => {
    await document.fonts.ready;
    const finite = document.getAnimations().filter((a) => a.effect?.getComputedTiming().iterations !== Infinity);
    await Promise.all(finite.map((a) => a.finished.catch(() => undefined)));
  });
  await page.waitForTimeout(150);
}

/** Text boxes from Range rects, so overlap means glyphs on glyphs, not block boxes. */
function audit() {
  const out = [];
  const rect = (r) => ({ l: r.left, t: r.top, r: r.right, b: r.bottom });
  for (const card of document.querySelectorAll('[data-shot]')) {
    const shot = card.getAttribute('data-shot');
    const fig = card.querySelector('[data-piece]');
    if (!fig) continue;
    const box = rect(fig.getBoundingClientRect());
    const stage = rect(card.getBoundingClientRect());
    const texts = [];
    const walker = document.createTreeWalker(fig, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (!n.textContent.trim()) continue;
      const el = n.parentElement;
      const cs = getComputedStyle(el);
      if (cs.visibility === 'hidden' || cs.display === 'none' || Number(cs.opacity) === 0) continue;
      const range = document.createRange();
      range.selectNodeContents(n);
      // Range rects ignore overflow clipping, so an ellipsised name is cut to its own box.
      const clip = cs.overflowX === 'hidden' ? rect(el.getBoundingClientRect()) : null;
      for (const r of range.getClientRects()) {
        if (r.width < 1 || r.height < 1) continue;
        const t = rect(r);
        if (clip) Object.assign(t, { l: Math.max(t.l, clip.l), r: Math.min(t.r, clip.r) });
        if (t.r - t.l < 1) continue;
        texts.push({ el, text: n.textContent.trim().slice(0, 24), ...t });
      }
    }
    const figCs = getComputedStyle(fig);
    const inset = Math.min(parseFloat(figCs.paddingLeft) || 0, parseFloat(figCs.paddingRight) || 0);
    if (inset < 12) out.push({ shot, kind: 'no-inset', text: `piece padding ${inset}px` });
    const edged = new Set();
    for (let i = 0; i < texts.length; i += 1) {
      const a = texts[i];
      if ((a.l - stage.l < inset - 0.5 || stage.r - a.r < inset - 0.5) && !edged.has(a.text)) {
        edged.add(a.text);
        out.push({ shot, kind: 'edge', text: a.text });
      }
      if (a.l < box.l - 1 || a.r > box.r + 1 || a.t < box.t - 1 || a.b > box.b + 1) {
        out.push({ shot, kind: 'spill', text: a.text });
      }
      for (let j = i + 1; j < texts.length; j += 1) {
        const b = texts[j];
        if (a.el === b.el) continue;
        const w = Math.min(a.r, b.r) - Math.max(a.l, b.l);
        const h = Math.min(a.b, b.b) - Math.max(a.t, b.t);
        if (w > 1.5 && h > 2.5) out.push({ shot, kind: 'overlap', text: `${a.text} / ${b.text}` });
      }
    }
    for (const el of fig.querySelectorAll('*')) {
      if (el instanceof SVGElement && !(el instanceof SVGSVGElement)) continue;
      const cs = getComputedStyle(el);
      if (el.scrollWidth > el.clientWidth + 1 && cs.overflowX !== 'visible' && el.clientWidth > 0) {
        const kind = cs.textOverflow === 'ellipsis' ? 'ellipsis' : 'clip';
        out.push({ shot, kind, text: el.textContent.trim().slice(0, 24) });
      }
    }
    for (const el of fig.querySelectorAll('svg, button, [class*="hex"]')) {
      if (el.closest('[class*="drift"]')) continue;
      const r = rect(el.getBoundingClientRect());
      if (r.r - r.l < 1) continue;
      if (r.l < stage.l - 1 || r.r > stage.r + 1 || r.t < stage.t - 1 || r.b > stage.b + 1) {
        out.push({ shot, kind: 'spill-card', text: el.getAttribute('class')?.slice(0, 24) ?? el.tagName });
      }
    }
  }
  return out;
}

const browser = await chromium.launch({ channel: 'chrome', headless: true });
try {
  for (const width of WIDTHS) {
    const context = await browser.newContext({ viewport: { width, height: 900 }, deviceScaleFactor: 1 });
    const page = await context.newPage();
    listen(page);
    for (const piece of PIECES) {
      await page.goto(url(piece));
      await page.waitForSelector(`[data-section="${piece}"] [data-piece]`);
      await settle(page);
      const section = page.locator(`[data-section="${piece}"]`);
      const file = `${piece}-${width}.png`;
      await section.screenshot({ path: path.join(OUT, file) });
      report.shots.push(file);
      report.audit[`${piece}-${width}`] = await page.evaluate(audit);
      report.scroll[`${piece}-${width}`] = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
      for (const shot of await page.locator('[data-shot]').evaluateAll((els) => els.map((e) => e.dataset.shot))) {
        const name = `${shot}-${width}.png`;
        await page.locator(`[data-shot="${shot}"]`).screenshot({ path: path.join(OUT, name) });
        report.shots.push(name);
        (byWidth[width] ??= []).push(name);
      }
    }
    const cells = byWidth[width].map((f) => `<figure><img src="${f}"><figcaption>${f}</figcaption></figure>`).join('');
    const sheet = path.join(OUT, `contact-${width}.html`);
    writeFileSync(sheet, `<!doctype html><style>body{margin:0;padding:8px;background:#05070f;color:#ccc;` +
      `font:10px monospace;` +
      `display:grid;grid-template-columns:repeat(6,1fr);gap:6px}img{width:100%}figure{margin:0}</style>${cells}`);
    await page.setViewportSize({ width: 2400, height: 900 });
    await page.goto(`file://${sheet}`);
    await page.waitForLoadState('load');
    await page.screenshot({ path: path.join(OUT, `contact-${width}.png`), fullPage: true });
    await context.close();
  }

  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  listen(page);
  for (const piece of PIECES) {
    await page.goto(url(piece));
    report.fps[piece] = await page.evaluate(
      () =>
        new Promise((done) => {
          const times = [];
          const step = (t) => {
            times.push(t);
            if (t - times[0] < 1500) requestAnimationFrame(step);
            else {
              const gaps = times.slice(1).map((x, i) => x - times[i]);
              const avg = gaps.reduce((a, g) => a + g, 0) / gaps.length;
              done({
                fps: Math.round(1000 / avg),
                worstMs: Math.round(Math.max(...gaps)),
                long: gaps.filter((g) => g > 25).length,
              });
            }
          };
          requestAnimationFrame(step);
        }),
    );
    await settle(page);
    const hubs = await page.locator('[data-piece]')
      .evaluateAll((els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
    report.hidden[piece] = await page.evaluate(async () => {
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
      document.dispatchEvent(new Event('visibilitychange'));
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
      const pieces = [...document.querySelectorAll('[data-piece]')];
      const loops = document.getAnimations().filter((a) => a.effect?.getComputedTiming().iterations === Infinity);
      const before = loops.map((a) => a.currentTime);
      await new Promise((r) => setTimeout(r, 400));
      return {
        pieces: pieces.length,
        allPaused: pieces.every((p) => p.hasAttribute('data-paused')),
        loops: loops.length,
        loopsStill: loops.every((a, i) => a.playState === 'paused' && a.currentTime === before[i]),
      };
    });
    report.reduced[piece] = { settled: hubs };
  }
  await context.close();

  const reducedCtx = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' });
  const rpage = await reducedCtx.newPage();
  listen(rpage);
  for (const piece of PIECES) {
    await rpage.goto(url(piece));
    await rpage.waitForSelector('[data-piece]');
    await rpage.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    const running = await rpage
      .evaluate(() => document.getAnimations().filter((a) => a.playState === 'running').length);
    const texts = await rpage.locator('[data-piece]')
      .evaluateAll((els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
    const settled = report.reduced[piece].settled;
    const sameAsSettled = texts.length === settled.length && texts.every((t, i) => t === settled[i]);
    report.reduced[piece] = { running, sameAsSettled };
  }
  await reducedCtx.close();

  if (FRAMES) {
    const vs = await browser.newPage({ viewport: { width: 1540, height: 560 } });
    for (const piece of PIECES) {
      const frame = readFileSync(path.join(FRAMES, `${piece}.png`)).toString('base64');
      const tag = piece === 'machine' || piece === 'docker' ? 'live' : 'today';
      const today = readFileSync(path.join(OUT, `${piece}-${tag}-1440.png`)).toString('base64');
      await vs.setContent(`<body style="margin:0;background:#05070f;display:flex;gap:20px;padding:10px;
        font:12px monospace;color:#ccc">
        <figure style="margin:0"><figcaption>approved frame (centre crop)</figcaption>
        <div style="width:760px;height:500px;
          background:url(data:image/png;base64,${frame}) -340px -95px no-repeat"></div></figure>
        <figure style="margin:0"><figcaption>gallery card</figcaption>
        <img style="max-width:740px;max-height:500px" src="data:image/png;base64,${today}"></figure></body>`);
      await vs.screenshot({ path: path.join(OUT, `${piece}-vs-figma.png`), fullPage: true });
      report.shots.push(`${piece}-vs-figma.png`);
    }
    await vs.close();
  }
} finally {
  await browser.close();
}

const rows = report.shots
  .map((f) => `<figure><img src="${f}" loading="lazy"><figcaption>${f}</figcaption></figure>`).join('\n');
writeFileSync(
  path.join(OUT, 'index.html'),
  `<!doctype html><meta charset="utf-8"><title>Pieces</title><style>body{background:#05070f;color:#ccc;` +
    `font:12px monospace;display:grid;grid-template-columns:repeat(auto-fill,minmax(420px,1fr));gap:12px}` +
    `img{width:100%;border:1px solid #333}figure{margin:0}</style>\n${rows}\n`,
);
writeFileSync(path.join(OUT, 'report.json'), `${JSON.stringify(report, null, 2)}\n`);
const issues = Object.values(report.audit).flat().filter((x) => x.kind !== 'ellipsis');
const scrolls = Object.entries(report.scroll).filter(([, v]) => v > 0);
console.log(
  `shots ${report.shots.length}, layout issues ${issues.length}, h-scroll ${scrolls.length}, ` +
    `problems ${problems.length}`,
);
