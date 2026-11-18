import { describe, expect, it } from 'vitest';
import type { Schemas } from '../api';
import { ALL_SECTIONS, navTabs, sectionsFrom, TAB_PATHS, tabForPath, tabVisible } from './tabs';

describe('tabForPath', () => {
  it('maps each of the 9 routes to its tab', () => {
    expect(navTabs).toHaveLength(9);
    for (const tab of navTabs) expect(tabForPath(TAB_PATHS[tab.id])).toBe(tab.id);
  });
  it('ignores a trailing slash and returns null for the gallery', () => {
    expect(tabForPath('/guard/')).toBe('guard');
    expect(tabForPath('/_gallery')).toBeNull();
  });
});

type Status = Pick<Schemas['StatusResponse'], 'docker' | 'guard'>;
const section = (state: Status['docker']['state']): Status['docker'] =>
  ({ state, lastAt: null, intervalSeconds: 30, errorCode: null });
const withStates = (docker: Status['docker']['state'], guard: Status['guard']['state']): Status =>
  ({ docker: section(docker), guard: section(guard) });

describe('sectionsFrom', () => {
  it('shows every tab until the status has answered', () => {
    expect(sectionsFrom(undefined)).toEqual(ALL_SECTIONS);
  });
  it('hides a section only when the server says it is disabled, never for a stopped or failing one', () => {
    expect(sectionsFrom(withStates('disabled', 'disabled'))).toEqual({ docker: false, guard: false });
    expect(sectionsFrom(withStates('unavailable', 'failed'))).toEqual({ docker: true, guard: true });
    expect(sectionsFrom(withStates('ok', 'starting'))).toEqual({ docker: true, guard: true });
  });
  it('hides only the Docker and Guard tabs', () => {
    const none = { docker: false, guard: false };
    expect(navTabs.filter((t) => !tabVisible(t.id, none)).map((t) => t.id)).toEqual(['guard', 'docker']);
  });
});
