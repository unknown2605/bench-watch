export type NetKind = 'uplink' | 'vpn' | 'local' | 'other';

export type NetNow = { interface: string; rxBps: number; txBps: number };
export type NetSeries = {
  interface: string;
  points: readonly { at: string; rxBps: number; txBps: number }[];
};

/** The server drops idle interfaces from `now`, so recent traffic in the series is what marks one as in use. */
export const RECENT_TRAFFIC_MS = 60_000;

export const KIND_LABEL: Record<NetKind, string> = {
  uplink: 'Wi-Fi/Ethernet', vpn: 'VPN', local: 'Local', other: 'Other',
};

const KIND_ORDER: Record<NetKind, number> = { uplink: 0, vpn: 1, other: 2, local: 3 };
const SYSTEM = /^(?:awdl|llw|anpi|ap|gif|stf)\d*$/i;
const BRIDGE = /^bridge\d*$/i;
const LOOPBACK = /^lo\d*$/i;
const VPN = /^(?:utun|ipsec|ppp|tun|tap|wg)\d*$/i;
// The API sends BSD names (en0) or service names (Wi-Fi); Linux eth, wlan, wl and enp/ens/eno names are planned.
const PHYSICAL = /^(?:en\d+|eth\d+|wlan\d+|wl[a-z0-9]*|en[pso][a-z0-9]*|wi-?fi|ethernet)$/i;

export function netKind(name: string): NetKind {
  if (LOOPBACK.test(name)) return 'local';
  if (VPN.test(name)) return 'vpn';
  if (PHYSICAL.test(name)) return 'uplink';
  return 'other';
}

/** System interfaces never show; a bridge shows only while it carries traffic. */
export function isHidden(i: NetNow): boolean {
  return SYSTEM.test(i.interface) || (BRIDGE.test(i.interface) && i.rxBps + i.txBps === 0);
}

const byName = new Intl.Collator('en', { numeric: true });

/** Fixed order (kind, then name) so rows do not move as traffic shifts; loopback comes last. */
export function visibleInterfaces<T extends NetNow>(all: readonly T[]): T[] {
  return all
    .filter((i) => !isHidden(i))
    .sort((a, b) => KIND_ORDER[netKind(a.interface)] - KIND_ORDER[netKind(b.interface)]
      || byName.compare(a.interface, b.interface));
}

/** Smallest gap between consecutive points, so a 1 h rollup is not judged against a 60 s window. */
function pointSpacingMs(series: readonly NetSeries[]): number {
  let gap = Infinity;
  for (const s of series) {
    for (let i = 1; i < s.points.length; i++) {
      const d = Date.parse(s.points[i]?.at ?? '') - Date.parse(s.points[i - 1]?.at ?? '');
      if (d > 0 && d < gap) gap = d;
    }
  }
  return gap;
}

function recentNames(series: readonly NetSeries[]): string[] {
  const latest = Math.max(...series.flatMap((s) => s.points.map((p) => Date.parse(p.at))));
  if (!Number.isFinite(latest)) return [];
  const since = latest - Math.max(RECENT_TRAFFIC_MS, pointSpacingMs(series));
  return series
    .filter((s) => s.points.some((p) => Date.parse(p.at) >= since && p.rxBps + p.txBps > 0))
    .map((s) => s.interface);
}

/** First physical interface by name, never the busiest; one idle for under a minute keeps the default. */
export function defaultInterface(rowsNow: readonly NetNow[], series: readonly NetSeries[]): string | null {
  const names = [...new Set([...rowsNow.map((i) => i.interface), ...recentNames(series)])]
    .filter((n) => !SYSTEM.test(n) && !BRIDGE.test(n));
  const pick = (kind: NetKind) => names.filter((n) => netKind(n) === kind).sort(byName.compare)[0];
  return pick('uplink') ?? pick('other') ?? pick('vpn') ?? pick('local') ?? null;
}

export function selectedInterface(
  selected: string | null,
  listed: readonly string[],
  fallback: string | null,
): string | null {
  return selected !== null && listed.includes(selected) ? selected : fallback;
}
