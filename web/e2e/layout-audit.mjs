// Test tool: the centre piece of every tab inside the real layout, at six widths.
// Needs live data (e2e/live-server.mjs).
// Fails on a piece narrower than its column, a covered or out-of-column text leaf, horizontal scroll, or page height.
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { chromium } from 'playwright';

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:4173';
const OUT = path.resolve(process.env.SHOT_DIR ?? 'test-results/layout');
const SIZES = [[768, 1024], [1024, 768], [1440, 900], [1920, 1080], [2560, 1440], [3840, 2160]].filter(
  ([w]) => !process.env.WIDTHS || process.env.WIDTHS.split(',').includes(String(w)),
);
const TABS = [
  { id: 'overview', path: '/' }, { id: 'tokens', path: '/tokens' }, { id: 'context', path: '/context' },
  { id: 'sessions', path: '/sessions' }, { id: 'sub-agents', path: '/sub-agents' }, { id: 'fills', path: '/fills' },
  { id: 'guard', path: '/guard' }, { id: 'machine', path: '/machine' }, { id: 'docker', path: '/docker' },
].filter((t) => !process.env.TABS || process.env.TABS.split(',').includes(t.id));
const SHOT_ALL = process.env.SHOT_ALL === '1';
mkdirSync(OUT, { recursive: true });

const audit = () => {
  const out = { pieceW: 0, columnW: 0, hscroll: false, height: 0, covered: [], outside: [], leaves: 0 };
  const piece = document.querySelector('main figure[data-piece]');
  const column = piece?.closest('[class*="_centre_"]');
  if (!piece || !column) return { ...out, missing: true };
  out.pieceW = Math.round(piece.getBoundingClientRect().width);
  out.columnW = Math.round(column.getBoundingClientRect().width);
  out.hscroll = document.documentElement.scrollWidth > document.documentElement.clientWidth;
  out.height = document.documentElement.scrollHeight;
  const col = column.getBoundingClientRect();
  const leaves = [...piece.querySelectorAll('*')].filter((el) => {
    const own = [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
    const st = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    return own && st.visibility !== 'hidden' && st.display !== 'none' && r.width > 1 && r.height > 1 &&
      st.opacity !== '0';
  });
  out.leaves = leaves.length;
  const label = (el) => {
    const cls = String(el.getAttribute('class') ?? '').split(/[\s_]/)[1] ?? '';
    return `${el.tagName.toLowerCase()}.${cls}"${el.textContent.trim().slice(0, 16)}"`;
  };
  for (const el of leaves) {
    el.scrollIntoView({ block: 'center', inline: 'nearest' });
    const r = el.getBoundingClientRect();
    if (r.left < col.left - 2 || r.right > col.right + 2) {
      out.outside.push(`${label(el)} ${Math.round(r.left)}..${Math.round(r.right)}`);
    }
    const was = el.style.pointerEvents;
    el.style.pointerEvents = 'auto';
    const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    el.style.pointerEvents = was;
    if (top && top !== el && !el.contains(top) && !top.contains(el)) {
      const t = top.closest('[class]');
      out.covered.push(`${label(el)} by ${t ? label(t) : top.tagName}`);
    }
  }
  window.scrollTo(0, 0);
  return out;
};

const browser = await chromium.launch({ channel: 'chrome', headless: true });
const report = { base: BASE, cells: {}, failures: [], skipped: [] };
for (const [width, height] of SIZES) {
  const context = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: 1 });
  const page = await context.newPage();
  for (const tab of TABS) {
    await page.goto(`${BASE}${tab.path}`);
    await page.waitForSelector('main[aria-busy="false"]', { timeout: 20_000 })
      .catch(() => report.failures.push(`${tab.id}@${width}: still busy`));
    await page.evaluate(() => document.fonts.ready);
    await page.waitForTimeout(3200);
    const cell = await page.evaluate(audit);
    if (SHOT_ALL || width === 1440 || (width === 768 || width === 3840)) {
      await page.screenshot({ path: path.join(OUT, `${tab.id}-${width}.png`) });
    }
    const bad = [];
    if (cell.missing) {
      report.skipped.push(`${tab.id}@${width}`);
      report.cells[`${width}-${tab.id}`] = { ...cell, bad };
      continue;
    }
    if (Math.abs(cell.pieceW - cell.columnW) > 2) bad.push(`piece ${cell.pieceW} vs column ${cell.columnW}`);
    if (cell.hscroll) bad.push('hscroll');
    if (cell.covered.length) bad.push(`covered ${cell.covered.length}: ${cell.covered.slice(0, 3).join('; ')}`);
    if (cell.outside.length) bad.push(`outside ${cell.outside.length}: ${cell.outside.slice(0, 3).join('; ')}`);
    if (width === 1440 && cell.height > height) bad.push(`height ${cell.height}>${height}`);
    report.cells[`${width}-${tab.id}`] = { ...cell, bad };
    for (const b of bad) report.failures.push(`${tab.id}@${width}: ${b}`);
  }
  await context.close();
}
await browser.close();
writeFileSync(path.join(OUT, 'layout-report.json'), JSON.stringify(report, null, 1));
for (const [key, c] of Object.entries(report.cells).filter(([, x]) => !x.missing)) {
  const verdict = c.bad.length ? `BAD ${c.bad.join(' | ')}` : 'ok';
  console.log(`${key} piece ${c.pieceW}/${c.columnW} h ${c.height} leaves ${c.leaves} ${verdict}`);
}
console.log(`skipped (no piece, e.g. no data) ${report.skipped.join(',')}`);
console.log(`failures ${report.failures.length}`);
if (report.failures.length) process.exitCode = 1;
