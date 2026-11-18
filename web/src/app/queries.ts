import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import type { JsonOf } from '../api';
import { ApiError, getJson, type Query } from '../api/client';
import { DEFAULT_RANGE, useFilters, useMetricsFilters, type TranscriptRange } from './filters';
import { useStreamUp } from './live';
import { NO_DATA, shellStatus, type ShellStatus } from './status';
import { sectionsFrom, type Sections, type TabId } from './tabs';

export const TREND_RANGE: TranscriptRange = '14d';

/** Fallback poll, used only while the /api/live stream (`useLiveStream`) is down. */
export const REFRESH_MS = 30_000;

const retry = (count: number, error: unknown) => !(error instanceof ApiError && error.status >= 400
  && error.status < 500) && count < 2;

type TranscriptPath = '/api/overview' | '/api/tokens' | '/api/context' | '/api/sessions' | '/api/subagents'
  | '/api/fills' | '/api/guard';

function useTranscriptAt<P extends TranscriptPath>(path: P, tab: TabId, extra: Query, fixed: TranscriptRange | null) {
  const filters = useFilters(tab);
  const range = fixed ?? filters.range;
  const { project, client } = filters;
  const query = { range, ...(project ? { project } : {}), ...(client ? { client } : {}), ...extra };
  const up = useStreamUp();
  return useQuery({
    queryKey: [path, query],
    queryFn: ({ signal }) => getJson<JsonOf<P>>(path, query, signal),
    refetchInterval: up ? false : REFRESH_MS,
    placeholderData: keepPreviousData,
    retry,
  });
}

export function useTranscript<P extends TranscriptPath>(path: P, tab: TabId, extra: Query = {}) {
  return useTranscriptAt(path, tab, extra, null);
}

/** Day-by-day trends always cover the last 14 days, whatever range is picked; one cached query per tab and project. */
export function useTrend<P extends TranscriptPath>(path: P, tab: TabId, extra: Query = {}) {
  return useTranscriptAt(path, tab, extra, TREND_RANGE);
}

export function useMeta() {
  return useQuery({
    queryKey: ['/api/meta'],
    queryFn: ({ signal }) => getJson<JsonOf<'/api/meta'>>('/api/meta', {}, signal),
    staleTime: 60_000,
    refetchInterval: 60_000,
    retry,
  });
}

export function useSessionDetail(key: { session: string; project: string } | null) {
  const up = useStreamUp();
  return useQuery({
    queryKey: ['/api/sessions/{session}', key],
    enabled: key !== null,
    queryFn: ({ signal }) =>
      getJson<JsonOf<'/api/sessions/{session}'>>(`/api/sessions/${encodeURIComponent(key?.session ?? '')}`,
        { project: key?.project }, signal),
    refetchInterval: up ? false : REFRESH_MS,
    retry,
  });
}

/** Poll spacing while the stream is down; Live follows the fastest sample. */
const METRIC_REFRESH_MS = { live: 1_000, '5m': 5_000, '15m': 10_000, '1h': 10_000, '24h': 10_000,
  '7d': 10_000 } as const;

type MetricsPath = '/api/machine' | '/api/docker';

export function useMetrics<P extends MetricsPath>(path: P, tab: TabId) {
  const { range, stack } = useMetricsFilters(tab);
  const query = { range, ...(path === '/api/docker' && stack ? { stack } : {}) };
  const up = useStreamUp();
  return useQuery({
    queryKey: [path, query],
    queryFn: ({ signal }) => getJson<JsonOf<P>>(path, query, signal),
    refetchInterval: up ? false : METRIC_REFRESH_MS[range],
    placeholderData: keepPreviousData,
    retry,
  });
}

const STATUS_MS = 30_000;

function useNow(everyMs: number): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), everyMs);
    return () => clearInterval(id);
  }, [everyMs]);
  return now;
}

const statusQuery = {
  queryKey: ['/api/status'],
  queryFn: ({ signal }: { signal: AbortSignal }) => getJson<JsonOf<'/api/status'>>('/api/status', {}, signal),
  retry,
} as const;

/** Own key: the live stream does not invalidate it, so it polls only every 30 s. */
const SHELL_CONTEXT_KEY = ['shell', '/api/context', { range: DEFAULT_RANGE }] as const;

export function useShellStatus(): ShellStatus {
  const now = useNow(1_000);
  const up = useStreamUp();
  const { data } = useQuery({ ...statusQuery, refetchInterval: up ? false : STATUS_MS });
  const context = useQuery({
    queryKey: SHELL_CONTEXT_KEY,
    queryFn: ({ signal }) =>
      getJson<JsonOf<'/api/context'>>('/api/context', { range: DEFAULT_RANGE }, signal),
    refetchInterval: STATUS_MS,
    retry,
  });
  return data ? shellStatus(data, now, context.data?.compactions?.recent ?? []) : NO_DATA;
}

/** Which optional tabs this machine has, from the shared status query; all of them until it answers. */
export function useSections(): Sections {
  const { data } = useQuery({ ...statusQuery, refetchInterval: false });
  return sectionsFrom(data);
}

/** False only while the first status answer is awaited; a final failure or a paused (offline) fetch counts as settled. */
export function useSectionsSettled(): boolean {
  const { data, status, fetchStatus } = useQuery({ ...statusQuery, refetchInterval: false });
  return data !== undefined || status === 'error' || fetchStatus === 'paused';
}

/** Read-only observer of the shared status query; the shell hook owns its polling. */
export function useToolErrorsToday(): number | null {
  const { data } = useQuery({ ...statusQuery, refetchInterval: false });
  const n = data?.scan?.toolErrors;
  return typeof n === 'number' ? n : null;
}
