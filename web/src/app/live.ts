import { useQueryClient, type Query, type QueryClient } from '@tanstack/react-query';
import { useEffect, useSyncExternalStore } from 'react';
import type { Schemas } from '../api';
import { dockerShape, withDocker, withMachine, type DockerData, type MachineData } from './liveCache';

const TRANSCRIPT_EVENTS = ['status', 'sessions', 'activity'] as const;
const KEYS = [
  '/api/status', '/api/overview', '/api/tokens', '/api/context',
  '/api/sessions', '/api/subagents', '/api/fills', '/api/guard',
];
const TRANSCRIPT_GAP_MS = 1_000;
const LIVE_GAP_MS = 1_000;
const METRIC_GAP_MS = 2_000;
const MID_METRIC_GAP_MS = 5_000;
const SLOW_METRIC_GAP_MS = 10_000;
const MID_RANGES = ['15m', '1h'];
const SLOW_RANGES = ['24h', '7d'];
const STABLE_OPEN_MS = 10_000;
const STALL_MS = 30_000;
const BACKOFF_MS = [1_000, 2_000, 4_000, 8_000, 15_000, 30_000];

let up = false;
const listeners = new Set<() => void>();
function setUp(next: boolean) {
  if (up === next) return;
  up = next;
  listeners.forEach((l) => l());
}
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

/** True while the /api/live stream is open; queries poll only while it is not. */
export function useStreamUp(): boolean {
  return useSyncExternalStore(subscribe, () => up, () => false);
}

const head = (q: Query) => {
  const [key] = q.queryKey;
  return typeof key === 'string' ? key : '';
};

function param(q: Query, name: string): string | undefined {
  const [, params] = q.queryKey;
  if (typeof params !== 'object' || params === null || !(name in params)) return undefined;
  const value: unknown = Reflect.get(params, name);
  return typeof value === 'string' ? value : undefined;
}

const refetch = (client: QueryClient, predicate: (q: Query) => boolean) =>
  client.invalidateQueries({ predicate }, { cancelRefetch: false });

/** Runs `fire` at most once per `gap` ms, the first call at once; calls in between share one later run. */
function throttle(fire: () => void, gap: number) {
  let last = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  return {
    call() {
      if (timer !== undefined) return;
      timer = setTimeout(
        () => {
          timer = undefined;
          last = Date.now();
          fire();
        },
        Math.max(0, gap - (Date.now() - last)),
      );
    },
    cancel() {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
    },
  };
}

function eventData(e: unknown, type: string): Record<string, unknown> | null {
  if (typeof e !== 'object' || e === null || !('data' in e) || typeof e.data !== 'string') return null;
  try {
    const body: unknown = JSON.parse(e.data);
    if (typeof body !== 'object' || body === null || !('type' in body) || body.type !== type) return null;
    if (!('data' in body)) return null;
    const { data } = body;
    if (typeof data !== 'object' || data === null) return null;
    return 'at' in data && typeof data.at === 'string' ? { ...data } : null;
  } catch {
    return null;
  }
}

function isMachine(d: Record<string, unknown>): d is Schemas['MachineSnapshot'] {
  return Array.isArray(d.cores) && Array.isArray(d.interfaces);
}

function isDocker(d: Record<string, unknown>): d is Schemas['DockerSnapshot'] {
  return typeof d.vm === 'object' && d.vm !== null && Array.isArray(d.containers);
}

/** One /api/live stream. It reopens with backoff and catches up after a gap; queries poll only while it is down. */
export function useLiveStream(): void {
  const client = useQueryClient();
  useEffect(() => {
    if (typeof EventSource === 'undefined') return undefined;
    let source: EventSource | null = null;
    let retry = 0;
    let reopen: ReturnType<typeof setTimeout> | undefined;
    let stable: ReturnType<typeof setTimeout> | undefined;
    let watchdog: ReturnType<typeof setTimeout> | undefined;
    let behind = false;
    let dockerChanged = false;

    const transcript = throttle(
      () => void refetch(client, (q) => KEYS.some((k) => head(q).startsWith(k))),
      TRANSCRIPT_GAP_MS,
    );
    const isMetric = (q: Query) => head(q) === '/api/machine' || head(q) === '/api/docker';
    const liveStale = (q: Query) => head(q) === '/api/docker' && (param(q, 'stack') !== undefined || dockerChanged);
    const needsRefetch = (q: Query) => isMetric(q) && (param(q, 'range') !== 'live' || liveStale(q));
    const slowRange = (q: Query) => SLOW_RANGES.includes(param(q, 'range') ?? '');
    const midRange = (q: Query) => MID_RANGES.includes(param(q, 'range') ?? '');
    const isLive = (q: Query) => param(q, 'range') === 'live';
    const live = throttle(() => {
      void refetch(client, (q) => needsRefetch(q) && isLive(q));
      dockerChanged = false;
    }, LIVE_GAP_MS);
    const fast = throttle(
      () => void refetch(client, (q) => needsRefetch(q) && !isLive(q) && !slowRange(q) && !midRange(q)),
      METRIC_GAP_MS,
    );
    const mid = throttle(() => void refetch(client, (q) => needsRefetch(q) && midRange(q)), MID_METRIC_GAP_MS);
    const slow = throttle(() => void refetch(client, (q) => needsRefetch(q) && slowRange(q)), SLOW_METRIC_GAP_MS);

    const patch = <T>(path: string, apply: (old: T, q: Query) => T) => {
      for (const q of client.getQueryCache().findAll({ queryKey: [path] })) {
        if (param(q, 'range') !== 'live') continue;
        client.setQueryData<T>(q.queryKey, (old) => (old === undefined ? old : apply(old, q)));
      }
    };
    const onMachine = (e: unknown) => {
      const snap = eventData(e, 'machine');
      if (!snap || !isMachine(snap)) return;
      patch<MachineData>('/api/machine', (old) => withMachine(old, snap));
      live.call();
      fast.call();
      mid.call();
      slow.call();
    };
    const onDocker = (e: unknown) => {
      const snap = eventData(e, 'docker');
      if (!snap || !isDocker(snap)) return;
      for (const q of client.getQueryCache().findAll({ queryKey: ['/api/docker'] })) {
        if (param(q, 'stack') !== undefined) continue;
        const was = client.getQueryData<DockerData>(q.queryKey)?.now;
        if (was && dockerShape(was) !== dockerShape(snap)) dockerChanged = true;
      }
      patch<DockerData>('/api/docker', (old, q) => (param(q, 'stack') === undefined ? withDocker(old, snap) : old));
      live.call();
      fast.call();
      mid.call();
      slow.call();
    };

    const close = () => {
      source?.close();
      source = null;
      clearTimeout(reopen);
      clearTimeout(stable);
      clearTimeout(watchdog);
      reopen = undefined;
      stable = undefined;
      watchdog = undefined;
    };
    const down = () => {
      setUp(false);
      behind = true;
      close();
      reopen = setTimeout(open, BACKOFF_MS[Math.min(retry, BACKOFF_MS.length - 1)]);
      retry += 1;
    };
    const alive = () => {
      clearTimeout(watchdog);
      watchdog = setTimeout(down, STALL_MS);
    };
    function open() {
      close();
      const next = new EventSource('/api/live');
      source = next;
      next.onopen = () => {
        alive();
        stable = setTimeout(() => {
          retry = 0;
        }, STABLE_OPEN_MS);
        setUp(true);
        if (!behind) return;
        behind = false;
        void refetch(client, (q) => isMetric(q) || KEYS.some((k) => head(q).startsWith(k)));
      };
      next.onerror = down;
      for (const name of TRANSCRIPT_EVENTS) {
        next.addEventListener(name, () => {
          alive();
          transcript.call();
        });
      }
      next.addEventListener('machine', (e) => {
        alive();
        onMachine(e);
      });
      next.addEventListener('docker', (e) => {
        alive();
        onDocker(e);
      });
    }
    const onVisibility = () => {
      if (document.hidden) {
        close();
        setUp(false);
        behind = true;
      } else if (source === null) {
        retry = 0;
        open();
      }
    };

    if (document.hidden) behind = true;
    else open();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      document.removeEventListener('visibilitychange', onVisibility);
      close();
      transcript.cancel();
      live.cancel();
      fast.cancel();
      mid.cancel();
      slow.cancel();
      setUp(false);
    };
  }, [client]);
}
