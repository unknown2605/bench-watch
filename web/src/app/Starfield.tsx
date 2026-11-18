import { useEffect, useRef } from 'react';
import styles from './Starfield.module.css';

function mulberry32(seed: number) {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function drawStarfield(canvas: HTMLCanvasElement, width: number, height: number, dpr: number) {
  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  canvas.width = Math.round(width * dpr);
  canvas.height = Math.round(height * dpr);
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  const horizon = height * 0.62;
  const sky = ctx.createLinearGradient(0, 0, 0, height);
  sky.addColorStop(0, '#03060f');
  sky.addColorStop(0.45, '#0a1030');
  sky.addColorStop(0.62, '#1b1748');
  sky.addColorStop(0.7, '#0b1032');
  sky.addColorStop(1, '#060a20');
  ctx.fillStyle = sky;
  ctx.fillRect(0, 0, width, height);

  ctx.save();
  ctx.translate(width / 2, horizon);
  ctx.scale(1, 0.16);
  const haze = ctx.createRadialGradient(0, 0, 0, 0, 0, width * 0.75);
  haze.addColorStop(0, 'rgba(150,130,235,0.34)');
  haze.addColorStop(0.45, 'rgba(120,100,215,0.16)');
  haze.addColorStop(1, 'rgba(120,100,215,0)');
  ctx.fillStyle = haze;
  ctx.fillRect(-width, -width, width * 2, width * 2);
  ctx.restore();

  const glow = ctx.createRadialGradient(width / 2, height * 0.38, 0, width / 2, height * 0.38, width * 0.4);
  glow.addColorStop(0, 'rgba(255,197,107,0.06)');
  glow.addColorStop(1, 'rgba(255,197,107,0)');
  ctx.fillStyle = glow;
  ctx.fillRect(0, 0, width, height);

  const floor = ctx.createLinearGradient(0, horizon, 0, height);
  floor.addColorStop(0, 'rgba(140,128,230,0)');
  floor.addColorStop(0.25, 'rgba(140,128,230,0.22)');
  floor.addColorStop(1, 'rgba(120,132,214,0.12)');
  ctx.strokeStyle = floor;
  ctx.lineWidth = 1;
  const reach = Math.ceil(width / 110) + 2;
  for (let i = -reach; i <= reach; i += 1) {
    ctx.beginPath();
    ctx.moveTo(width / 2 + i * 28, horizon);
    ctx.lineTo(width / 2 + i * 220, height);
    ctx.stroke();
  }
  for (let k = 1; k <= 16; k += 1) {
    const y = horizon + (height - horizon) * (k / 16) ** 2.2;
    ctx.beginPath();
    ctx.moveTo(0, y);
    ctx.lineTo(width, y);
    ctx.stroke();
  }
}

type Star = {
  x: number; y: number; r: number; color: number; alpha: number; speed: number; phase: number; rate: number;
};

const PALETTE = ['255,244,224', '169,180,214', '95,212,196', '255,197,107'];
const FRAME_MS = 33;

export function makeStars(width: number, height: number): Star[] {
  const rand = mulberry32(2605);
  const horizon = height * 0.62;
  const count = Math.round((width * height) / 5200);
  return Array.from({ length: count }, () => {
    const x = rand() * width;
    const y = rand() * horizon;
    const r = 0.3 + rand() ** 3 * 1.3;
    const color = Math.floor(rand() * rand() * PALETTE.length);
    const alpha = 0.25 + rand() * 0.6;
    return { x, y, r, color, alpha, speed: 1.5 + r * 3, phase: rand() * Math.PI * 2, rate: 0.6 + rand() * 1.6 };
  });
}

const LEVELS = 10;
const buckets: number[][] = Array.from({ length: PALETTE.length * LEVELS }, () => []);

/** Alpha is quantised to ten steps so each colour and step is one path and one fill, not one fill per star. */
export function drawStars(
  ctx: CanvasRenderingContext2D,
  stars: readonly Star[],
  width: number,
  height: number,
  t: number,
  still: boolean,
) {
  ctx.clearRect(0, 0, width, height);
  for (const b of buckets) b.length = 0;
  stars.forEach((s, i) => {
    const a = still ? s.alpha : s.alpha * (0.6 + 0.4 * Math.sin(t * s.rate + s.phase));
    buckets[s.color * LEVELS + Math.min(LEVELS - 1, Math.floor(a * LEVELS))]?.push(i);
  });
  buckets.forEach((idx, b) => {
    if (!idx.length) return;
    ctx.fillStyle = `rgba(${PALETTE[Math.floor(b / LEVELS)]},${((b % LEVELS) + 0.5) / LEVELS})`;
    ctx.beginPath();
    for (const i of idx) {
      const s = stars[i];
      if (!s) continue;
      const x = still ? s.x : (s.x + s.speed * t) % width;
      ctx.moveTo(x + s.r, s.y);
      ctx.arc(x, s.y, s.r, 0, Math.PI * 2);
    }
    ctx.fill();
  });
}

export function Starfield() {
  const sky = useRef<HTMLCanvasElement>(null);
  const layer = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const base = sky.current;
    const top = layer.current;
    if (!base || !top) return;
    let frame = 0;
    let stars: Star[] = [];
    let w = 0;
    let h = 0;
    let t0 = 0;
    let last = 0;
    let timer = 0;
    const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)');
    const ctx = () => top.getContext('2d');
    const paint = () => {
      const dpr = window.devicePixelRatio || 1;
      w = window.innerWidth;
      h = window.innerHeight;
      drawStarfield(base, w, h, dpr);
      top.width = Math.round(w * dpr);
      top.height = Math.round(h * dpr);
      ctx()?.setTransform(dpr, 0, 0, dpr, 0, 0);
      stars = makeStars(w, h);
      const c = ctx();
      if (c) drawStars(c, stars, w, h, (performance.now() - t0) / 1000, true);
    };
    // Waiting out most of the interval before asking for a frame keeps an idle tab at 30 frames a second, not 60.
    const next = () => {
      timer = window.setTimeout(() => {
        frame = requestAnimationFrame(tick);
      }, FRAME_MS - 8);
    };
    const tick = (now: number) => {
      next();
      if (now - last < FRAME_MS - 8) return;
      last = now;
      const c = ctx();
      if (c) drawStars(c, stars, w, h, (now - t0) / 1000, false);
    };
    const sync = () => {
      window.clearTimeout(timer);
      cancelAnimationFrame(frame);
      frame = 0;
      if (reduced?.matches || document.hidden) {
        const c = ctx();
        if (c) drawStars(c, stars, w, h, 0, true);
        return;
      }
      frame = requestAnimationFrame(tick);
    };
    let pending = 0;
    const onResize = () => {
      cancelAnimationFrame(pending);
      pending = requestAnimationFrame(() => {
        paint();
        sync();
      });
    };
    t0 = performance.now();
    paint();
    sync();
    window.addEventListener('resize', onResize);
    document.addEventListener('visibilitychange', sync);
    reduced?.addEventListener('change', sync);
    return () => {
      window.removeEventListener('resize', onResize);
      document.removeEventListener('visibilitychange', sync);
      reduced?.removeEventListener('change', sync);
      cancelAnimationFrame(pending);
      cancelAnimationFrame(frame);
      window.clearTimeout(timer);
    };
  }, []);
  return (
    <>
      <canvas ref={sky} className={styles.sky} aria-hidden="true" />
      <canvas ref={layer} className={styles.stars} aria-hidden="true" />
    </>
  );
}
