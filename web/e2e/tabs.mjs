import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { chromium } from 'playwright';

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:4173';
const OUT = path.resolve(process.env.SHOT_DIR ?? 'test-results/tabs');
mkdirSync(OUT, { recursive: true });

const TABS = [
  { id: 'overview', path: '/' },
  { id: 'tokens', path: '/tokens' },
  { id: 'context', path: '/context' },
  { id: 'sessions', path: '/sessions' },
  { id: 'sub-agents', path: '/sub-agents' },
  { id: 'fills', path: '/fills' },
  { id: 'guard', path: '/guard' },
  { id: 'machine', path: '/machine' },
  { id: 'docker', path: '/docker' },
].filter((t) => !process.env.TABS || process.env.TABS.split(',').includes(t.id));
const problems = [];
const report = { tabs: [], problems };

const browser = await chromium.launch({ channel: 'chrome', headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
await context.exposeBinding('reportCsp', (_s, info) => problems.push(`csp violation: ${info}`));
await context.addInitScript(() => {
  document.addEventListener('securitypolicyviolation', (e) => window.reportCsp(`${e.violatedDirective} ${e.blockedURI}`.slice(0, 200)));
});
const page = await context.newPage();
page.on('console', (m) => {
  if (m.type() === 'error' || m.type() === 'warning') problems.push(`${m.type()}: ${m.text().slice(0, 200)}`);
});
page.on('pageerror', (e) => problems.push(`pageerror: ${e.message.slice(0, 200)}`));
page.on('request', (r) => {
  if (!r.url().startsWith(BASE) && !r.url().startsWith('data:') && !r.url().startsWith('blob:')) problems.push(`external: ${r.url().slice(0, 120)}`);
});

// WCAG contrast of each visible text colour against its nearest opaque background, colours resolved via a canvas.
const contrast = () =>
  page.evaluate(() => {
    const ctx = Object.assign(document.createElement('canvas'), { width: 1, height: 1 }).getContext('2d', { willReadFrequently: true });
    const rgba = (css) => {
      ctx.clearRect(0, 0, 1, 1);
      ctx.fillStyle = '#000';
      ctx.fillStyle = css;
      ctx.fillRect(0, 0, 1, 1);
      const [r, g, b, a] = ctx.getImageData(0, 0, 1, 1).data;
      return [r, g, b, a / 255];
    };
    const lum = ([r, g, b]) => {
      const c = [r, g, b].map((v) => {
        const x = v / 255;
        return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
    };
    const surface = rgba(getComputedStyle(document.documentElement).getPropertyValue('--sh-surface-base').trim() || '#000');
    const bgOf = (el) => {
      for (let e = el; e; e = e.parentElement) {
        const c = rgba(getComputedStyle(e).backgroundColor);
        if (c[3] > 0.9) return c;
      }
      return surface;
    };
    const low = [];
    const walker = document.createTreeWalker(document.querySelector('main'), NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const el = n.parentElement;
      if (!el || !n.textContent?.trim() || el.closest('table[id], caption')) continue;
      const st = getComputedStyle(el);
      if (st.visibility === 'hidden' || st.display === 'none' || Number(st.opacity) < 0.1) continue;
      const box = el.getBoundingClientRect();
      if (box.width < 2 || box.height < 2) continue;
      const svg = el.namespaceURI === 'http://www.w3.org/2000/svg';
      const fg = rgba(svg ? st.fill : st.color);
      const bg = bgOf(el);
      const mix = fg.slice(0, 3).map((v, i) => v * fg[3] + bg[i] * (1 - fg[3]));
      const a = lum(mix);
      const b = lum(bg);
      const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      const large = parseFloat(st.fontSize) >= 24 || (parseFloat(st.fontSize) >= 18.66 && Number(st.fontWeight) >= 700);
      if (ratio < (large ? 3 : 4.5)) low.push(`${n.textContent.trim().slice(0, 24)} ${ratio.toFixed(2)}`);
    }
    return low;
  });

for (const tab of TABS) {
  await page.goto(`${BASE}${tab.path}`);
  await page.waitForSelector('main[aria-busy="false"]', { timeout: 20_000 }).catch(() => problems.push(`${tab.id}: still busy`));
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(1200);
  const state = await page.locator('main [role="alert"], main [role="status"]').allTextContents();
  const file = path.join(OUT, `${tab.id}-app.png`);
  await page.screenshot({ path: file });
  const focusables = await page.locator('main button, main a[href], main [tabindex="0"]').count();
  const lowContrast = await contrast();
  const leak = await page.evaluate(() => /\/Users\/|\/home\/|\$ |sk-[A-Za-z0-9]/.test(document.querySelector('main')?.innerText ?? ''));
  if (leak) problems.push(`${tab.id}: path- or command-like text in main`);
  report.tabs.push({ tab: tab.id, file, state, focusables, lowContrast });
}

await page.goto(`${BASE}/sessions`);
await page.waitForSelector('main[aria-busy="false"]', { timeout: 20_000 }).catch(() => undefined);
const planets = page.locator('main [role="button"][tabindex="0"]');
if ((await planets.count()) > 1) {
  await planets.nth(1).focus();
  await page.keyboard.press('Enter');
  await page.waitForTimeout(800);
  report.sessionsKeyboard = { pressed: await planets.nth(1).getAttribute('aria-pressed') };
}

if (TABS.some((t) => t.id === 'fills')) {
  await page.goto(`${BASE}/fills`);
  await page.waitForSelector('main[aria-busy="false"]', { timeout: 20_000 }).catch(() => undefined);
  const arcs = page.locator('main path[role="button"]');
  if ((await arcs.count()) > 1) {
    const before = await page.locator('main section[aria-label^="Selected"]').innerText();
    await arcs.nth(1).click({ force: true });
    await page.waitForTimeout(500);
    report.fillsDrill = { before: before.slice(0, 60), after: (await page.locator('main section[aria-label^="Selected"]').innerText()).slice(0, 60), pressed: await arcs.nth(1).getAttribute('aria-pressed') };
  }
}

if (TABS.some((t) => t.id === 'machine')) {
  await page.goto(`${BASE}/machine`);
  await page.waitForSelector('main[aria-busy="false"]', { timeout: 20_000 }).catch(() => undefined);
  const updated = () => page.locator('main [aria-label="Last update"]').innerText();
  const first = await updated();
  await page.getByRole('button', { name: '24h' }).click();
  await page.waitForTimeout(2500);
  report.machine24h = { updatedBefore: first, updatedAfter: await updated(), note: (await page.locator('main').innerText()).includes('History paused') };
  await page.screenshot({ path: path.join(OUT, 'machine-24h-app.png') });
}

if (TABS.some((t) => t.id === 'sessions')) {
  await page.goto(`${BASE}/sessions`);
  await page.waitForSelector('main[aria-busy="false"]', { timeout: 20_000 }).catch(() => undefined);
  const link = await page.evaluate(async () => (await (await fetch('/api/sessions?range=30d')).json()).sessions?.[0] ?? null);
  if (link) {
    await page.goto(`${BASE}/sessions?session=${link.session}&project=${encodeURIComponent(link.project)}`);
    await page.waitForSelector('main[aria-busy="false"]', { timeout: 20_000 }).catch(() => undefined);
    await page.waitForTimeout(1000);
    await page.reload();
    await page.waitForTimeout(1500);
    report.deepLink = { url: page.url().replace(/session=[^&]+/, 'session=…'), selectedShown: await page.locator('main section[aria-label^="Selected"]').count() };
    await page.screenshot({ path: path.join(OUT, 'sessions-deeplink-app.png') });
  }
}

await browser.close();
writeFileSync(path.join(OUT, 'tabs-report.json'), JSON.stringify(report, null, 2));
console.log(JSON.stringify(report, null, 1));
if (problems.length) process.exitCode = 1;
