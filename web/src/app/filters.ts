import { useSyncExternalStore } from 'react';
import type { Schemas } from '../api';
import type { TabId } from './tabs';

export type TranscriptRange = Schemas['TranscriptRange'];
export const RANGES: readonly TranscriptRange[] = ['today', '7d', '14d', '30d', '90d', 'all'];
export const DEFAULT_RANGE: TranscriptRange = 'today';
export const RANGE_LABEL: Record<TranscriptRange, string> = { today: 'Today', '7d': '7d', '14d': '14d', '30d': '30d', '90d': '90d', all: 'All' };

export type MetricsRange = Schemas['MetricsRange'];
export const METRIC_RANGES: readonly MetricsRange[] = ['live', '5m', '15m', '1h', '24h', '7d'];
export const METRIC_RANGE_LABEL: Record<MetricsRange, string> = { live: 'Live', '5m': '5m', '15m': '15m', '1h': '1h', '24h': '24h', '7d': '7d' };
export const DEFAULT_METRIC_RANGE: MetricsRange = 'live';

// The project follows the user across tabs; each tab keeps its own range.
type State = {
  project: string | null;
  client: string | null;
  ranges: Partial<Record<TabId, TranscriptRange>>;
  metrics: Partial<Record<TabId, MetricsRange>>;
  stack: string | null;
};

let state: State = { project: null, client: null, ranges: {}, metrics: {}, stack: null };
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

export function setProject(project: string | null) {
  state = { ...state, project };
  emit();
}

export function setClient(client: string | null) {
  state = { ...state, client };
  emit();
}

export function setRange(tab: TabId, range: TranscriptRange) {
  state = { ...state, ranges: { ...state.ranges, [tab]: range } };
  emit();
}

export function setMetricsRange(tab: TabId, range: MetricsRange) {
  state = { ...state, metrics: { ...state.metrics, [tab]: range } };
  emit();
}

export function setStack(stack: string | null) {
  state = { ...state, stack };
  emit();
}

export function resetFilters() {
  state = { project: null, client: null, ranges: {}, metrics: {}, stack: null };
  emit();
}

export function useMetricsFilters(tab: TabId): { range: MetricsRange; stack: string | null } {
  const s = useSyncExternalStore(subscribe, () => state);
  return { range: s.metrics[tab] ?? DEFAULT_METRIC_RANGE, stack: s.stack };
}

export function useFilters(tab: TabId): { project: string | null; client: string | null; range: TranscriptRange } {
  const s = useSyncExternalStore(subscribe, () => state);
  return { project: s.project, client: s.client, range: s.ranges[tab] ?? DEFAULT_RANGE };
}
