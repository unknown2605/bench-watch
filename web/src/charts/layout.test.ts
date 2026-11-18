import { describe, expect, it } from 'vitest';
import { angleFor, callouts, hourTicks, placeHubLabel, spokes } from './layout';

describe('chart layout', () => {
  it('gives a label the same angle every time', () => {
    const a = angleFor('benchwatch');
    expect(angleFor('benchwatch')).toBe(a);
    expect(a).toBeGreaterThanOrEqual(0);
    expect(a).toBeLessThan(Math.PI * 2);
    expect(angleFor('web')).not.toBe(a);
  });

  it('moves the hub label off a star label that sits where it would go', () => {
    const base = { cx: 200, cy: 200, hub: 60, textW: 60, textH: 12, bounds: [400, 400] as [number, number], segments: [] };
    const free = placeHubLabel({ ...base, boxes: [] });
    const blocker = { x0: free.x - 40, y0: free.y - 20, x1: free.x + 40, y1: free.y + 20 };
    const moved = placeHubLabel({ ...base, boxes: [blocker] });
    expect(Math.hypot(moved.x - free.x, moved.y - free.y)).toBeGreaterThan(20);
    const box = { x0: moved.x - 30, y0: moved.y - 6, x1: moved.x + 30, y1: moved.y + 6 };
    expect(box.x0 < blocker.x1 && blocker.x0 < box.x1 && box.y0 < blocker.y1 && blocker.y0 < box.y1).toBe(false);
  });

  it('puts callouts on the side of their slice and keeps them apart', () => {
    const out = callouts([0.3, 0.35, Math.PI * 1.5], 100, 100, 50, 80, 20);
    expect(out.map((c) => c.side)).toEqual(['right', 'right', 'left']);
    expect(out[0]?.x).toBe(180);
    expect(out[2]?.x).toBe(20);
    const right = out.filter((c) => c.side === 'right').map((c) => c.y).sort((p, q) => p - q);
    expect((right[1] ?? 0) - (right[0] ?? 0)).toBeGreaterThanOrEqual(20);
    expect(out[0]?.ax).toBeCloseTo(100 + Math.sin(0.3) * 50);
  });

  it('lists local hour ticks inside a window', () => {
    const start = new Date(2026, 10, 9, 1, 30).getTime();
    const end = new Date(2026, 10, 9, 13, 0).getTime();
    const ticks = hourTicks(start, end, 6);
    expect(ticks.map((t) => t.label)).toEqual(['06:00', '12:00']);
    expect(ticks[0]?.at).toBeCloseTo((4.5 * 3600_000) / (end - start));
    expect(hourTicks(end, start, 6)).toEqual([]);
    expect(hourTicks(start, end, 0)).toEqual([]);
  });

  it('spreads spokes round an ellipse from the top', () => {
    const pts = spokes(4, 0, 0, 10, 5);
    expect(pts[0]?.x).toBeCloseTo(0);
    expect(pts[0]?.y).toBeCloseTo(-5);
    expect(pts[1]?.x).toBeCloseTo(10);
    expect(pts[2]?.y).toBeCloseTo(5);
    expect(spokes(0, 0, 0, 1, 1)).toEqual([]);
  });
});
