import type { Schemas } from '../api';

export const navTabs = [
  { id: 'overview', label: 'Overview' },
  { id: 'tokens', label: 'Tokens' },
  { id: 'context', label: 'Context' },
  { id: 'sessions', label: 'Sessions' },
  { id: 'sub-agents', label: 'Sub-agents' },
  { id: 'fills', label: 'Fills' },
  { id: 'guard', label: 'Guard' },
  { id: 'machine', label: 'Machine' },
  { id: 'docker', label: 'Docker' },
] as const;

/** Tabs whose figures exist only for Claude Code; the API refuses them under another client. */
export const CLAUDE_ONLY_TABS: readonly TabId[] = ['context', 'sub-agents', 'fills', 'guard'];

export type TabId = (typeof navTabs)[number]['id'];

export const TAB_PATHS = {
  overview: '/',
  tokens: '/tokens',
  context: '/context',
  sessions: '/sessions',
  'sub-agents': '/sub-agents',
  fills: '/fills',
  guard: '/guard',
  machine: '/machine',
  docker: '/docker',
} as const satisfies Record<TabId, string>;

export function tabForPath(pathname: string): TabId | null {
  const clean = pathname.replace(/\/+$/, '') || '/';
  const hit = navTabs.find((tab) => TAB_PATHS[tab.id] === clean);
  return hit ? hit.id : null;
}

/** Whether the Docker and Guard tabs exist on this machine; true until the status says `disabled`. */
export type Sections = { docker: boolean; guard: boolean };

export const ALL_SECTIONS: Sections = { docker: true, guard: true };

export function sectionsFrom(status: Pick<Schemas['StatusResponse'], 'docker' | 'guard'> | undefined): Sections {
  if (!status) return ALL_SECTIONS;
  return { docker: status.docker.state !== 'disabled', guard: status.guard.state !== 'disabled' };
}

export const HIDEABLE_TABS: readonly TabId[] = ['docker', 'guard'];

export function tabVisible(id: TabId, sections: Sections): boolean {
  return id === 'docker' ? sections.docker : id === 'guard' ? sections.guard : true;
}
