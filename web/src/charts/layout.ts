const TAU = Math.PI * 2;

/** A stable angle for a label, so a planet keeps its place when the data refreshes. */
export function angleFor(label: string): number {
  let h = 2166136261;
  for (let i = 0; i < label.length; i += 1) {
    h ^= label.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return ((h >>> 0) / 4294967296) * TAU;
}

export type Box = { x0: number; y0: number; x1: number; y1: number };
export type Segment = [number, number, number, number];

const hits = (a: Box, b: Box) => a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1;

function crosses(box: Box, [ax, ay, bx, by]: Segment): boolean {
  for (let i = 0; i <= 16; i += 1) {
    const x = ax + ((bx - ax) * i) / 16;
    const y = ay + ((by - ay) * i) / 16;
    if (x > box.x0 - 3 && x < box.x1 + 3 && y > box.y0 - 3 && y < box.y1 + 3) return true;
  }
  return false;
}

export type HubLabelSpec = {
  cx: number; cy: number; hub: number; textW: number; textH: number; bounds: [number, number];
  boxes: readonly Box[]; segments: readonly Segment[];
};

/** Centre of a label beside a hub: the first of 16 directions clear of every star and label, else the least crossed. */
export function placeHubLabel(s: HubLabelSpec): { x: number; y: number } {
  const w = s.textW / 2;
  const h = s.textH / 2;
  let best = { x: s.cx, y: s.cy - s.hub - 20, score: Infinity };
  for (let k = 0; k !== 16; k += 1) {
    const a = ((-35 + k * 22.5) * Math.PI) / 180;
    const dx = Math.sin(a);
    const dy = -Math.cos(a);
    const reach = s.hub + 16 + Math.abs(dx) * w + Math.abs(dy) * h;
    const x = Math.min(Math.max(s.cx + dx * reach, w + 2), s.bounds[0] - w - 2);
    const y = Math.min(Math.max(s.cy + dy * reach, h + 2), s.bounds[1] - h - 2);
    const box = { x0: x - w, y0: y - h, x1: x + w, y1: y + h };
    const score = s.boxes.filter((b) => hits(box, b)).length * 10 + s.segments.filter((g) => crosses(box, g)).length;
    if (score < best.score) best = { x, y, score };
    if (score === 0) break;
  }
  return best;
}

export type Callout = { side: 'left' | 'right'; x: number; y: number; ax: number; ay: number };

/** Angles run clockwise from the top; labels on one side are pushed at least `gap` px apart. */
export function callouts(mids: readonly number[], cx: number, cy: number, r: number, column: number,
  gap: number): Callout[] {
  const out: Callout[] = mids.map((a) => {
    const ax = cx + Math.sin(a) * r;
    const ay = cy - Math.cos(a) * r;
    const side = Math.sin(a) >= 0 ? 'right' : 'left';
    return { side, ax, ay, x: side === 'right' ? cx + column : cx - column, y: ay };
  });
  for (const side of ['left', 'right'] as const) {
    const group = out.filter((c) => c.side === side).sort((p, q) => p.y - q.y);
    for (let i = 1; i < group.length; i += 1) {
      const prev = group[i - 1];
      const cur = group[i];
      if (prev && cur && cur.y - prev.y < gap) cur.y = prev.y + gap;
    }
  }
  return out;
}

/** Hour ticks every `stepHours` local hours inside [start, end] (ms), as 0-1 positions with HH:00 labels. */
export function hourTicks(start: number, end: number, stepHours: number): { at: number; label: string }[] {
  if (!(end > start) || stepHours <= 0) return [];
  const t = new Date(start);
  t.setMinutes(0, 0, 0);
  if (t.getTime() < start) t.setHours(t.getHours() + 1);
  while (t.getHours() % stepHours !== 0) t.setHours(t.getHours() + 1);
  const out: { at: number; label: string }[] = [];
  while (t.getTime() <= end && out.length < 48) {
    out.push({ at: (t.getTime() - start) / (end - start), label: `${String(t.getHours()).padStart(2, '0')}:00` });
    t.setHours(t.getHours() + stepHours);
  }
  return out;
}

export type Spoke = { x: number; y: number; angle: number };

/** n points spread round an ellipse, starting at the top and going clockwise. */
export function spokes(n: number, cx: number, cy: number, rx: number, ry: number, offset = 0): Spoke[] {
  return Array.from({ length: Math.max(0, n) }, (_, i) => {
    const angle = offset + (i / Math.max(n, 1)) * TAU;
    return { angle, x: cx + Math.sin(angle) * rx, y: cy - Math.cos(angle) * ry };
  });
}
