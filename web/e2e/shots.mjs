import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { chromium } from 'playwright';

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:4173';
const OUT = path.resolve('test-results');
const SHOTS = path.join(OUT, 'shots');
mkdirSync(SHOTS, { recursive: true });

const COMPONENTS = [
  'panel', 'kpi', 'list-row', 'container-cell', 'chip', 'meter', 'marker', 'planet', 'badge', 'status',
  'top-bar', 'log-bar',
];
const CHARTS = [
  'chart-ring', 'chart-disc-tower', 'chart-star-map', 'chart-shield', 'chart-honeycomb', 'chart-line', 'chart-uplot',
];
const TABS = ['overview', 'tokens', 'context', 'sessions', 'sub-agents', 'fills', 'guard', 'machine', 'docker'];
const labelOf = (tab) => (tab === 'sub-agents' ? 'Sub-agents' : tab[0].toUpperCase() + tab.slice(1));
const pathOf = (tab) => (tab === 'overview' ? '/' : `/${tab}`);

const SHELL_ONLY = process.argv.includes('--shell');
const problems = [];

// React Aria's pressable style is blocked by the CSP unless index.html pre-declares it; this guard fails the run
// when that breaks.
async function watch(context) {
  await context.exposeBinding('reportCsp', (_source, info) => problems.push(`csp violation: ${info}`));
  await context.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (e) => {
      window.reportCsp(`${e.violatedDirective} ${e.blockedURI}`.slice(0, 200));
    });
  });
}

const browser = await chromium.launch({ channel: 'chrome', headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
await watch(context);
const external = [];
function listen(p) {
  p.on('console', (m) => {
    if (m.type() === 'error' || m.type() === 'warning') problems.push(`${m.type()}: ${m.text().slice(0, 200)}`);
  });
  p.on('pageerror', (e) => problems.push(`pageerror: ${e.message.slice(0, 200)}`));
  p.on('requestfailed', (r) => problems.push(`request failed: ${r.url().slice(0, 200)}`));
  p.on('response', (r) => {
    if (r.status() >= 400) problems.push(`http ${r.status()}: ${r.url().slice(0, 200)}`);
  });
  p.on('request', (r) => {
    const url = r.url();
    if (!url.startsWith(BASE) && !url.startsWith('data:') && !url.startsWith('blob:')) external.push(url);
  });
}
const page = await context.newPage();
listen(page);

const report = { shots: [], keyboard: [], reduced: {}, problems, external };

if (!SHELL_ONLY) {
  await page.goto(`${BASE}/_gallery`);
  await page.waitForSelector('[data-shot="panel"]');
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(500);

  for (const name of [...COMPONENTS, ...CHARTS]) {
    const el = page.locator(`[data-shot="${name}"]`);
    const file = path.join(SHOTS, `${name}.png`);
    if (name === 'top-bar' || name === 'log-bar') {
      await el.scrollIntoViewIfNeeded();
      const box = await el.boundingBox();
      const scrollY = await page.evaluate(() => window.scrollY);
      if (!box) throw new Error(`no box for ${name}`);
      const pad = name === 'top-bar' ? 8 : 0;
      const clip = {
        x: Math.max(0, box.x - pad),
        y: Math.max(0, box.y + scrollY - pad),
        width: box.width + (pad ? 16 : 0),
        height: box.height + (pad ? 8 : 0),
      };
      await page.screenshot({ path: file, fullPage: true, clip });
    } else {
      await el.screenshot({ path: file });
    }
    report.shots.push(file);
  }
}

const ariaControlsResolves = () =>
  page.evaluate(() => {
    const tab = document.querySelector('[role="tab"][aria-selected="true"]');
    const id = tab?.getAttribute('aria-controls');
    return Boolean(id && document.getElementById(id));
  });

// Never throws: after 3 s the caller records what it sees, so a slow tab switch is a reported problem.
const settle = (label, pathname, { focus = false } = {}) =>
  page
    .waitForFunction(
      ([want, where, focused]) => {
        const selected = document.querySelector('[role="tab"][aria-selected="true"]')?.textContent?.trim();
        const active = document.activeElement?.textContent?.trim();
        return selected === want && (!focused || active === want) && window.location.pathname === where;
      },
      [label, pathname, focus],
      { timeout: 3000 },
    )
    .catch(() => {});

for (const tab of TABS) {
  await page.goto(`${BASE}/`);
  await page.waitForSelector('[role="tab"]');
  await page.evaluate(() => document.fonts.ready);
  const label = labelOf(tab);
  await page.locator('[role="tab"]').filter({ hasText: new RegExp(`^${label}$`) }).click();
  await settle(label, pathOf(tab));
  const selected = await page.locator('[role="tab"][aria-selected="true"]').textContent();
  const url = new URL(page.url()).pathname;
  if (selected?.trim() !== label) problems.push(`clicking ${tab} selected ${JSON.stringify(selected)}`);
  if (url !== pathOf(tab)) problems.push(`clicking ${tab} went to ${url}, expected ${pathOf(tab)}`);
  await page.screenshot({ path: path.join(SHOTS, `shell-${tab}.png`) });
  const resolves = await ariaControlsResolves();
  if (!resolves) problems.push(`aria-controls does not resolve after opening ${tab}`);
  report.keyboard.push({ clickTab: tab, selected, url, ariaControlsResolves: resolves });
}

await page.goto(`${BASE}/`);
await page.waitForSelector('[role="tab"]');
await page.keyboard.press('Tab');
const seen = [];
for (let i = 0; i < 9; i += 1) {
  const tab = TABS[i];
  await settle(labelOf(tab), pathOf(tab), { focus: true });
  const focused = await page.evaluate(() => document.activeElement?.textContent ?? '');
  const selected = await page.locator('[role="tab"][aria-selected="true"]').textContent();
  seen.push({ focused, url: new URL(page.url()).pathname, selected });
  const step = seen[i];
  if (step.focused.trim() !== labelOf(tab) || step.selected?.trim() !== labelOf(tab) || step.url !== pathOf(tab)) {
    problems.push(`keyboard walk step ${i}: expected ${labelOf(tab)} at ${pathOf(tab)}, got ${JSON.stringify(step)}`);
  }
  if (i < 8) await page.keyboard.press('ArrowRight');
}
report.keyboardWalk = seen;
await page.waitForTimeout(300);
report.ariaControlsResolves = await ariaControlsResolves();
if (!report.ariaControlsResolves) problems.push('aria-controls does not resolve after the keyboard walk');
const mains = await page.locator('main').count();
if (mains !== 1) problems.push(`expected one main landmark, found ${mains}`);

const reduced = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' });
await watch(reduced);
const rpage = await reduced.newPage();
listen(rpage);
await rpage.goto(`${BASE}${SHELL_ONLY ? '/' : '/_gallery'}`);
await rpage.waitForSelector(SHELL_ONLY ? '[role="tab"]' : '[data-shot="panel"]');
report.reduced = await rpage.evaluate(() => {
  const chip = document.querySelector('[data-shot="chip"] button, [role="tab"]');
  return {
    matches: matchMedia('(prefers-reduced-motion: reduce)').matches,
    runningAnimations: document.getAnimations().length,
    chipTransition: chip ? getComputedStyle(chip).transitionDuration : null,
  };
});
const rmains = await rpage.locator('main').count();
if (rmains !== 1) problems.push(`reduced-motion page: expected one main landmark, found ${rmains}`);
if (report.reduced.matches !== true) problems.push('reduced motion is not matched');
if (report.reduced.runningAnimations !== 0) {
  problems.push(`${report.reduced.runningAnimations} animations run under reduced motion`);
}
if (report.reduced.chipTransition === null || !/^(0s|0ms)(, (0s|0ms))*$/.test(report.reduced.chipTransition)) {
  problems.push(`transition under reduced motion: ${report.reduced.chipTransition}`);
}
await reduced.close();

if (!SHELL_ONLY) {
  const rows = [...COMPONENTS, 'overview-shell']
    .map((n) => {
      const fig = n === 'overview-shell' ? 'overview.png' : `${n}.png`;
      const mine = n === 'overview-shell' ? 'shell-overview.png' : `${n}.png`;
      const figma = `<div><p>Figma</p><img src="../figma/${fig}"></div>`;
      return `<h3>${n}</h3><div class="r">${figma}<div><p>Built</p><img src="${mine}"></div></div>`;
    })
    .join('\n');
  writeFileSync(
    path.join(SHOTS, 'compare.html'),
    '<!doctype html><meta charset="utf-8"><style>body{background:#1b1f27;color:#aab;font:12px monospace;margin:12px}' +
      '.r{display:flex;gap:16px;align-items:flex-start;margin-bottom:16px}img{display:block;max-width:none}</style>' +
      rows,
  );
  const cmp = await context.newPage();
  for (const n of [...COMPONENTS, 'overview-shell']) {
    await cmp.setViewportSize({ width: 3000, height: 900 });
    await cmp.goto(`file://${path.join(SHOTS, 'compare.html')}`);
    await cmp.waitForLoadState('load');
    const block = cmp.locator('h3', { hasText: new RegExp(`^${n}$`) }).locator('xpath=following-sibling::div[1]');
    await block.screenshot({ path: path.join(SHOTS, `compare-${n}.png`) });
  }
}

await browser.close();
writeFileSync(path.join(OUT, 'report.json'), JSON.stringify(report, null, 2));
const summary = {
  problems,
  external,
  keyboardWalk: report.keyboardWalk,
  clicks: report.keyboard,
  reduced: report.reduced,
  ariaControlsResolves: report.ariaControlsResolves,
};
console.log(JSON.stringify(summary, null, 1));
if (problems.length || external.length) {
  console.error(`FAIL: ${problems.length} problems, ${external.length} external requests`);
  process.exitCode = 1;
}
