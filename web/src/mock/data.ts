import type { LineMarker, Point } from '../charts';
import type { Health } from '../components/ContainerCell';
import type { PlanetKind } from '../components/Planet';

export type SessionRow = { name: string; model: string; minutes: number; state: 'live' | 'idle'; used: number; limit: number };
export type ContainerRow = { name: string; memoryMb: number; health: Health };

export const sessions: readonly SessionRow[] = [
  { name: 'project-alpha', model: 'opus', minutes: 42, state: 'live', used: 128_000, limit: 200_000 },
  { name: 'project-beta', model: 'sonnet', minutes: 18, state: 'live', used: 64_000, limit: 200_000 },
  { name: 'shop-api', model: 'opus', minutes: 85, state: 'idle', used: 171_000, limit: 200_000 },
  { name: 'docs-site', model: 'haiku', minutes: 9, state: 'idle', used: 22_000, limit: 200_000 },
  { name: 'infra-scripts', model: 'sonnet', minutes: 31, state: 'idle', used: 96_000, limit: 200_000 },
];

export const containers: readonly ContainerRow[] = [
  { name: 'mariadb', memoryMb: 412, health: 'healthy' },
  { name: 'postgres', memoryMb: 388, health: 'healthy' },
  { name: 'redis', memoryMb: 61, health: 'healthy' },
  { name: 'nginx', memoryMb: 120, health: 'warn' },
  { name: 'php84', memoryMb: 540, health: 'healthy' },
  { name: 'floci', memoryMb: 210, health: 'down' },
  { name: 'apps', memoryMb: 330, health: 'healthy' },
  { name: 'mailpit', memoryMb: 24, health: 'healthy' },
  { name: 'php83', memoryMb: 512, health: 'warn' },
  { name: 'solr', memoryMb: 0, health: 'down' },
];

const daily = [38, 44, 31, 52, 47, 63, 58, 49, 72, 66, 54, 81, 76, 69, 90, 84, 73, 95, 88, 79, 102, 97, 86, 110, 104, 92, 120, 99, 108, 128];
export const tokenFlow: readonly Point[] = daily.map((y, x) => ({ x, y }));
export const tokenMarkers: readonly LineMarker[] = [
  { x: 6, kind: 'event', name: 'scan' },
  { x: 14, kind: 'compaction', name: 'compaction' },
  { x: 29, kind: 'peak', name: 'peak' },
];

export const kpiTrends = {
  gold: [2, 4, 3, 6, 5, 8, 7, 10, 9, 12],
  live: [10, 12, 9, 10, 6, 7, 3, 5, 2, 3],
  warn: [2, 3, 4, 5, 6, 8, 9, 11, 12, 13],
  default: [3, 4, 4, 5, 5, 7, 6, 8, 8, 10],
} as const;
export const coreLoad = [0.92, 0.7, 0.35, 0.18, 0.55, 0.81, 0.44, 0.27, 0.12, 0.66] as const;
export const ringValues = [
  { name: 'cache read', value: 0.92, tone: 'gold' },
  { name: 'cache write', value: 0.7, tone: 'live' },
  { name: 'output', value: 0.35, tone: 'warn' },
] as const;
export const discs = [
  { name: 'cache read', value: 1_000, tone: 'gold' },
  { name: 'cache write', value: 130, tone: 'session' },
  { name: 'input', value: 100, tone: 'dim' },
  { name: 'output', value: 50, tone: 'warn' },
] as const;
export const stars: readonly { name: string; value: number; kind: PlanetKind }[] = [
  { name: 'project-alpha', value: 128, kind: 'selected' },
  { name: 'project-beta', value: 64, kind: 'live' },
  { name: 'shop-api', value: 171, kind: 'idle' },
  { name: 'docs-site', value: 22, kind: 'idle' },
  { name: 'infra-scripts', value: 96, kind: 'idle' },
  { name: 'agent-a', value: 40, kind: 'live' },
  { name: 'agent-b', value: 12, kind: 'idle' },
];

export function liveSeries(points = 240): [number[], number[]] {
  const t0 = 1_800_000_000;
  const xs: number[] = [];
  const ys: number[] = [];
  for (let i = 0; i < points; i += 1) {
    xs.push(t0 + i * 2);
    ys.push(40 + 25 * Math.sin(i / 11) + 12 * Math.sin(i / 3.7));
  }
  return [xs, ys];
}

export const logSegments = [
  '10:42:07  SCAN OK',
  '10:41:57  COMPACTION project-alpha 162k → 38k',
  '0 ERRORS',
] as const;
