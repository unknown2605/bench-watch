const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const group = new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 });

const finite = (n: number) => (Number.isFinite(n) ? n : 0);

function sig3(n: number): string {
  const digits = n >= 100 ? 0 : n >= 10 ? 1 : 2;
  return n.toFixed(digits);
}

/** Token counts as on the design: 999, 9.6k, 171k, 1.28M, 14.9B. */
export function tokens(raw: number): string {
  const n = Math.abs(finite(raw));
  const sign = raw < 0 ? '-' : '';
  if (n < 1000) return `${sign}${Math.round(n)}`;
  if (n < 10_000) return `${sign}${(n / 1000).toFixed(1).replace(/\.0$/, '')}k`;
  if (n < 999_500) return `${sign}${Math.round(n / 1000)}k`;
  if (n < 999_500_000) return `${sign}${sig3(n / 1e6)}M`;
  return `${sign}${sig3(n / 1e9)}B`;
}

export function int(n: number): string {
  return group.format(Math.round(finite(n)));
}

/** Estimated cost: cents below $1,000, whole dollars above. */
export function usd(raw: number): string {
  const n = finite(raw);
  if (Math.abs(n) >= 1000) return `$${group.format(Math.round(n))}`;
  return `$${n.toFixed(2)}`;
}

/** A share 0-1 as a whole percent; a non-zero share below 0.5% shows as <1%. */
export function pct(share: number): string {
  const s = finite(share);
  if (s > 0 && s < 0.005) return '<1%';
  return `${Math.round(s * 100)}%`;
}

export function pct1(share: number): string {
  return `${(finite(share) * 100).toFixed(1)}%`;
}

const arrow = (d: number) => (d > 0 ? '▲' : d < 0 ? '▼' : '■');

/** Relative change against the previous period, or null when there is none to compare with. */
export function deltaPct(value: number, previous: number | null, days: number): string | null {
  if (previous === null) return null;
  if (previous === 0) return value === 0 ? `■ 0% vs prev ${days} d` : `▲ new vs prev ${days} d`;
  const d = (value - previous) / Math.abs(previous);
  return `${arrow(d)} ${Math.abs(d * 100).toFixed(1)}% vs prev ${days} d`;
}

export function deltaPts(value: number, previous: number | null): string | null {
  if (previous === null) return null;
  const d = (value - previous) * 100;
  return `${arrow(Math.round(d * 10))} ${Math.abs(d).toFixed(1)} pts`;
}

/** The count with its noun, singular for exactly one: "1 run", "3 runs". */
export function counted(n: number, noun: string): string {
  return `${int(n)} ${n === 1 ? noun : `${noun}s`}`;
}

export function deltaCount(value: number, previous: number | null, days: number, unit: string): string | null {
  if (previous === null) return null;
  const d = value - previous;
  return `${arrow(d)} ${int(Math.abs(d))} ${unit} vs prev ${days} d`;
}

/** YYYY-MM-DD read as a calendar day, never shifted by a time zone. */
export function parseDay(day: string): { y: number; m: number; d: number } | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!m) return null;
  return { y: Number(m[1]), m: Number(m[2]), d: Number(m[3]) };
}

export function dayLabel(day: string): string {
  const p = parseDay(day);
  if (!p) return '';
  return `${String(p.d).padStart(2, '0')} ${MONTHS[p.m - 1] ?? ''}`;
}

export function periodLabel(from: string, to: string): string {
  return from === to ? dayLabel(from) : `${dayLabel(from)} → ${dayLabel(to)}`;
}

export function clock(iso: string): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '';
  return `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`;
}

export function localDay(t: Date): string {
  return `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, '0')}-${String(t.getDate()).padStart(2, '0')}`;
}

/** "Today 10:41" for today, "06 Nov 18:30" otherwise. */
export function whenLabel(iso: string, now: Date): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '';
  const day = localDay(t);
  return `${day === localDay(now) ? 'Today' : dayLabel(day)} ${clock(iso)}`;
}

/** Elapsed time: 42 m, 1 h 05, 3 d. */
export function duration(seconds: number): string {
  const s = Math.max(0, Math.floor(finite(seconds)));
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} h ${String(m % 60).padStart(2, '0')}`;
  return `${Math.floor(h / 24)} d`;
}

export function since(iso: string, now: Date): string {
  const t = new Date(iso).getTime();
  return Number.isNaN(t) ? '' : duration((now.getTime() - t) / 1000);
}

/** claude-opus-5-5 → opus; the [1m] tag stays visible because it changes the context limit. */
export function modelShort(model: string): string {
  if (model === 'unknown') return 'unknown';
  const wide = /\[1m\]$/.test(model) ? ' 1m' : '';
  const parts = model.replace(/\[[0-9a-z]+\]$/, '').split('-');
  if (parts[1] === '3' && parts.length >= 4) return `${parts[3]}${wide}`;
  return `${parts[1] ?? model}${wide}`;
}

export type Band = { minTokens: number; maxTokens: number | null };

/** Context band: <100k, 100–200k, 700k+. */
export function bandLabel(band: Band): string {
  if (band.maxTokens === null) return `${tokens(band.minTokens)}+`;
  if (band.minTokens <= 0) return `<${tokens(band.maxTokens)}`;
  return `${tokens(band.minTokens).replace(/[kMB]$/, (u) => (tokens(band.maxTokens ?? 0).endsWith(u) ? '' : u))}–${tokens(band.maxTokens)}`;
}

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const;

function scaled(raw: number, base: number): { n: number; unit: (typeof UNITS)[number] } {
  let n = Math.max(0, finite(raw));
  let i = 0;
  while (n >= base * 0.9995 && i < UNITS.length - 1) {
    n /= base;
    i += 1;
  }
  return { n, unit: UNITS[i] ?? 'B' };
}

/** Sizes: 412 GB, 0.4 GB, 96 MB. Memory counts in 1024s, disks and networks in 1000s. */
export function size(raw: number, base: 1000 | 1024 = 1000): string {
  const { n, unit } = scaled(raw, base);
  const digits = unit === 'B' || unit === 'KB' || n >= 100 ? 0 : n >= 10 && unit !== 'GB' && unit !== 'TB' ? 0 : 1;
  return `${n.toFixed(digits)} ${unit}`;
}

/** Throughput in bytes per second: 4.2 MB/s, 8 KB/s. */
export function rate(bps: number): string {
  const { n, unit } = scaled(bps, 1000);
  return `${n.toFixed(unit === 'B' || unit === 'KB' || n >= 100 ? 0 : 1)} ${unit}/s`;
}

/** Latency: 3.1 ms below 10, whole milliseconds above. */
export function ms(raw: number): string {
  const n = Math.max(0, finite(raw));
  return `${n < 10 ? n.toFixed(1) : Math.round(n)} ms`;
}

/** A 0-100 percentage as the design shows CPU: 23%. */
export function pctPoints(raw: number): string {
  return `${Math.round(Math.max(0, finite(raw)))}%`;
}

/** Hours, minutes and seconds of an instant, as the Updated chip shows it. */
export function clockSeconds(iso: string): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '';
  return `${clock(iso)}:${String(t.getSeconds()).padStart(2, '0')}`;
}

/** Shows a limit as the raw value next to it: 966k (limit 200k). Never a percentage over 100. */
export function withLimit(value: number, limit: number): string {
  return limit > 0 ? `${tokens(value)} (limit ${tokens(limit)})` : tokens(value);
}

/** The tool a label belongs to: `Read: whole` and `Read: ranged` are Read, every MCP tool is MCP. */
export function toolFamily(tool: string): string {
  if (tool.startsWith('mcp__')) return 'MCP';
  const at = tool.indexOf(': ');
  return at > 0 ? tool.slice(0, at) : tool;
}

/** What follows the colon in a tool label (whole, ranged, git), or an empty string. */
export function toolDetail(tool: string): string {
  const at = tool.indexOf(': ');
  return at > 0 ? tool.slice(at + 2) : '';
}

/** A container name without the compose suffix: php84_service_1 becomes php84-1, floci_service becomes floci. */
export function containerShort(name: string): string {
  return name.replace(/_service(?:_(\d+))?$/, (_m, n: string | undefined) => (n ? `-${n}` : ''));
}
