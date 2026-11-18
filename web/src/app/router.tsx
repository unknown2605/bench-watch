import { createRootRoute, createRoute, createRouter, type RouterHistory } from '@tanstack/react-router';
import { lazy, type ComponentType, type LazyExoticComponent } from 'react';
import { isProjectLabel, isSessionLabel } from '../format/labels';
import { NotFound } from '../routes/NotFound';
import { ChunkLoadError, loadChunk } from './chunk';
import { ErrorView } from './ErrorBoundary';
import { Shell } from './Shell';
import { TAB_PATHS, type TabId } from './tabs';

const rootRoute = createRootRoute({ component: Shell, notFoundComponent: NotFound });

const tab = <K extends string>(load: () => Promise<Record<K, ComponentType>>, name: K) =>
  lazy(async () => ({ default: (await loadChunk(load))[name] }));

const TAB_PAGES: Record<TabId, LazyExoticComponent<ComponentType>> = {
  overview: tab(() => import('../routes/tabs/Overview'), 'Overview'),
  tokens: tab(() => import('../routes/tabs/Tokens'), 'Tokens'),
  context: tab(() => import('../routes/tabs/Context'), 'Context'),
  sessions: tab(() => import('../routes/tabs/Sessions'), 'Sessions'),
  'sub-agents': tab(() => import('../routes/tabs/SubAgents'), 'SubAgents'),
  fills: tab(() => import('../routes/tabs/Fills'), 'Fills'),
  guard: tab(() => import('../routes/tabs/Guard'), 'Guard'),
  machine: tab(() => import('../routes/tabs/Machine'), 'Machine'),
  docker: tab(() => import('../routes/tabs/Docker'), 'Docker'),
};

export type SessionSearch = { session?: string; project?: string };

/** A deep link names one session; both parts must pass the label gates, anything else is dropped. */
export function validateSessionSearch(search: Record<string, unknown>): SessionSearch {
  const { session, project } = search;
  return isSessionLabel(session) && isProjectLabel(project) ? { session, project } : {};
}

const tabRoute = (id: TabId) =>
  createRoute({ getParentRoute: () => rootRoute, path: TAB_PATHS[id], component: TAB_PAGES[id] });

const sessionsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: TAB_PATHS.sessions,
  component: TAB_PAGES.sessions,
  validateSearch: validateSessionSearch,
});

// Only the gallery build has the page; the dynamic import is removed from the production bundle.
const Gallery = import.meta.env.VITE_GALLERY === '1' ? lazy(() => import('../routes/Gallery')) : NotFound;
const galleryRoute = createRoute({ getParentRoute: () => rootRoute, path: '/_gallery', component: Gallery });

const routeTree = rootRoute.addChildren([
  tabRoute('overview'),
  tabRoute('tokens'),
  tabRoute('context'),
  sessionsRoute,
  tabRoute('sub-agents'),
  tabRoute('fills'),
  tabRoute('guard'),
  tabRoute('machine'),
  tabRoute('docker'),
  galleryRoute,
]);

export function createAppRouter(history?: RouterHistory) {
  return createRouter({
    routeTree,
    ...(history ? { history } : {}),
    defaultPreload: false,
    defaultErrorComponent: ({ error, reset }) => (error instanceof ChunkLoadError ? <ErrorView /> : <ErrorView reset={reset} />),
  });
}

export const router = createAppRouter();

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
