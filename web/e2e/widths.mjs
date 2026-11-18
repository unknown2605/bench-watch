// Test tool: screenshots every tab at six widths and checks horizontal scroll, clipping, overlap and the 2560 cap.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { chromium } from 'playwright';

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:4173';
const OUT = path.resolve(process.env.SHOT_DIR ?? 'test-results/widths');
const SIZES = [[768, 1024], [1024, 768], [1440, 900], [1920, 1080], [2560, 1440], [3840, 2160]].filter(
  ([w]) => !process.env.WIDTHS || process.env.WIDTHS.split(',').includes(String(w)),
);
const TABS = [
  { id: 'overview', path: '/' }, { id: 'tokens', path: '/tokens' }, { id: 'context', path: '/context' },
  { id: 'sessions', path: '/sessions' }, { id: 'sub-agents', path: '/sub-agents' }, { id: 'fills', path: '/fills' },
  { id: 'guard', path: '/guard' }, { id: 'machine', path: '/machine' }, { id: 'docker', path: '/docker' },
].filter((t) => !process.env.TABS || process.env.TABS.split(',').includes(t.id));
const CAP = 2560;
mkdirSync(OUT, { recursive: true });

const check = () => {
  const vw = document.documentElement.clientWidth;
  const out = { hscroll: [], clipped: [], outside: [], overlap: [], cap: null };
  const nameOf = (el) => `${el.tagName.toLowerCase()}.${String(el.getAttribute('class') ?? '').split(/[\s_]/)[1] ?? ''}`;
  const visible = (el) => {
    const st = getComputedStyle(el);
    const box = el.getBoundingClientRect();
    return st.visibility !== 'hidden' && st.display !== 'none' && box.width > 1 && box.height > 1;
  };
  const hidden = (el) => {
    for (let e = el; e && e !== document.body; e = e.parentElement) {
      const st = getComputedStyle(e);
      if (st.clip === 'rect(0px, 0px, 0px, 0px)' || st.clipPath === 'inset(50%)' || (st.position === 'absolute' && e.offsetWidth <= 1)) return true;
    }
    return false;
  };
  const decorative = (el) => el.closest('[aria-hidden="true"]') !== null;
  // Content is cut only when a real descendant, not a decorative full-bleed line, reaches past the clipping box.
  const cut = (el, box) =>
    [...el.querySelectorAll('*')].some((c) => {
      if (decorative(c) || !visible(c) || hidden(c)) return false;
      const r = c.getBoundingClientRect();
      return r.right > box.right + 1 || r.left < box.left - 1;
    });
  if (document.documentElement.scrollWidth > vw) out.hscroll.push(`html ${document.documentElement.scrollWidth}>${vw}`);
  for (const el of document.querySelectorAll('body *')) {
    if (el.closest('svg') && el.tagName !== 'svg') continue;
    if (!visible(el) || hidden(el)) continue;
    const st = getComputedStyle(el);
    const box = el.getBoundingClientRect();
    const over = el.scrollWidth - el.clientWidth;
    if (over > 1 && el.clientWidth > 0) {
      if (st.overflowX === 'auto' || st.overflowX === 'scroll') out.hscroll.push(`${nameOf(el)} ${el.scrollWidth}>${el.clientWidth}`);
      else if ((st.overflowX === 'hidden' || st.overflowX === 'clip') && st.textOverflow !== 'ellipsis' && cut(el, box)) {
        out.clipped.push(`${nameOf(el)} ${el.scrollWidth}>${el.clientWidth}`);
      }
    }
    if (el.tagName !== 'CANVAS' && !decorative(el) && (box.right > vw + 1 || box.left < -1)) out.outside.push(`${nameOf(el)} ${Math.round(box.left)}..${Math.round(box.right)}`);
  }
  const blocks = [...document.querySelectorAll('main [class*="_panel_"], main [class*="_kpi_"], header, footer')].filter(visible);
  for (let i = 0; i < blocks.length; i += 1) {
    for (let k = i + 1; k < blocks.length; k += 1) {
      const a = blocks[i];
      const b = blocks[k];
      if (a.contains(b) || b.contains(a)) continue;
      const ra = a.getBoundingClientRect();
      const rb = b.getBoundingClientRect();
      const w = Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left);
      const h = Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top);
      if (w > 2 && h > 2) out.overlap.push(`${nameOf(a)} x ${nameOf(b)} ${Math.round(w)}x${Math.round(h)}`);
    }
  }
  const span = (els) => {
    const boxes = els.filter(visible).map((el) => el.getBoundingClientRect());
    if (!boxes.length) return null;
    const left = Math.min(...boxes.map((r) => r.left));
    const right = Math.max(...boxes.map((r) => r.right));
    return { left: Math.round(left), right: Math.round(vw - right), width: Math.round(right - left) };
  };
  const main = document.querySelector('main');
  out.cap = {
    viewport: vw,
    bodyWidth: Math.round(document.body.getBoundingClientRect().width),
    backdropWidth: Math.round(document.querySelector('canvas')?.getBoundingClientRect().width ?? 0),
    content: span([...main.querySelectorAll('[class*="_panel_"], [class*="_kpi_"]')]),
    topBar: span([...document.querySelectorAll('[role="tablist"] [role="tab"], header *, [class*="_bar_"] > *')]),
    logBar: span([...document.querySelectorAll('footer *, [class*="_segment"]')]),
  };
  return out;
};

const browser = await chromium.launch({ channel: 'chrome', headless: true });
const report = { base: BASE, cells: {}, failures: [] };
for (const [width, height] of SIZES) {
  const context = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: 1 });
  const page = await context.newPage();
  for (const tab of TABS) {
    await page.goto(`${BASE}${tab.path}`);
    await page.waitForSelector('main[aria-busy="false"]', { timeout: 20_000 }).catch(() => report.failures.push(`${tab.id}@${width}: still busy`));
    await page.evaluate(() => document.fonts.ready);
    await page.waitForTimeout(3200);
    await page.screenshot({ path: path.join(OUT, `${width}-${tab.id}.png`) });
    const cell = await page.evaluate(check);
    const bad = [];
    if (cell.hscroll.length) bad.push(`hscroll ${cell.hscroll.slice(0, 3).join('; ')}`);
    if (cell.clipped.length) bad.push(`clipped ${cell.clipped.slice(0, 4).join('; ')}`);
    if (cell.outside.length) bad.push(`outside ${cell.outside.slice(0, 3).join('; ')}`);
    if (cell.overlap.length) bad.push(`overlap ${cell.overlap.slice(0, 3).join('; ')}`);
    if (cell.cap.bodyWidth !== width) bad.push(`body ${cell.cap.bodyWidth}`);
    for (const part of ['content', 'topBar', 'logBar']) {
      const s = cell.cap[part];
      if (!s) continue;
      if (s.width > CAP) bad.push(`${part} ${s.width} wide`);
      if (width > CAP && part === 'content' && Math.abs(s.left - s.right) > 2) bad.push(`${part} off centre ${s.left}/${s.right}`);
    }
    report.cells[`${width}-${tab.id}`] = { ...cell, bad };
    for (const b of bad) report.failures.push(`${tab.id}@${width}: ${b}`);
  }
  await context.close();
}

const sheet = await browser.newContext({ viewport: { width: 1800, height: 1200 }, deviceScaleFactor: 1 });
const sp = await sheet.newPage();
for (const [width, height] of SIZES) {
  const cols = width >= 1440 ? 3 : 5;
  const cellW = Math.floor(1780 / cols);
  const imgs = TABS.map((t) => `<figure><img src="data:image/png;base64,${readFileSync(path.join(OUT, `${width}-${t.id}.png`)).toString('base64')}" width="${cellW - 8}"><figcaption>${t.id} ${width}x${height}</figcaption></figure>`).join('');
  await sp.setContent(`<body style="margin:4px;background:#333;color:#fff;font:11px monospace;display:flex;flex-wrap:wrap;gap:4px">${imgs}<style>figure{margin:0}img{display:block}</style></body>`);
  await sp.waitForTimeout(400);
  await sp.screenshot({ path: path.join(OUT, `sheet-${width}.png`), fullPage: true });
}
await browser.close();
writeFileSync(path.join(OUT, 'widths-report.json'), JSON.stringify(report, null, 1));
const matrix = {};
for (const [key, cell] of Object.entries(report.cells)) matrix[key] = cell.bad.length ? cell.bad.join(' | ') : 'ok';
console.log(JSON.stringify({ matrix, caps: Object.fromEntries(Object.entries(report.cells).filter(([k]) => /^(2560|3840)-(overview|machine)/.test(k)).map(([k, c]) => [k, c.cap])) }, null, 1));
if (report.failures.length) process.exitCode = 1;
