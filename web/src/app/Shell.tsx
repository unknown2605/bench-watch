import { Outlet, useNavigate, useRouterState } from '@tanstack/react-router';
import { Suspense, useLayoutEffect } from 'react';
import { LogBar, NavPanel, NavTabs, TopBar } from '../components';
import { NotOnThisMachine } from '../routes/NotOnThisMachine';
import { TabLoading } from '../routes/TabLoading';
import { useLiveStream } from './live';
import { useSections, useSectionsSettled, useShellStatus } from './queries';
import { Starfield } from './Starfield';
import { HIDEABLE_TABS, navTabs, TAB_PATHS, tabForPath, tabVisible, type TabId } from './tabs';
import { useUiScale } from './uiScale';
import styles from './Shell.module.css';

export function Shell() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const navigate = useNavigate();
  const status = useShellStatus();
  const sections = useSections();
  const settled = useSectionsSettled();
  useLiveStream();
  const active = tabForPath(pathname);
  const zoom = useUiScale();
  // Set on the root so popovers, which render outside the shell, scale with it.
  useLayoutEffect(() => {
    document.documentElement.style.setProperty('--ui-zoom', String(zoom));
  }, [zoom]);
  const hidden = active !== null && !tabVisible(active, sections);
  const tabs = navTabs.filter((t) => tabVisible(t.id, sections));
  if (!active) {
    return (
      <>
        <Starfield />
        <div className={styles.shell}>
          <div className={styles.main}>
            <Outlet />
          </div>
        </div>
      </>
    );
  }
  // A hideable tab mounts only once the status says it exists, so its requests never go out first.
  const waiting = !settled && HIDEABLE_TABS.includes(active);
  return (
    <>
      <Starfield />
      <div className={styles.shell}>
        <NavTabs activeKey={active} onSelect={(key) => void navigate({ to: TAB_PATHS[key as TabId] })}>
          <TopBar tabs={tabs} dividerBefore="machine" updated={status.updated} health={status.health} live={status.live} />
          <NavPanel key={active} id={active} className={styles.main}
            unlisted={hidden ? (navTabs.find((t) => t.id === active)?.label ?? active) : undefined}>
            {waiting ? (
              <TabLoading />
            ) : hidden ? (
              <NotOnThisMachine label={navTabs.find((t) => t.id === active)?.label ?? ''} />
            ) : (
              <Suspense fallback={<TabLoading />}>
                <Outlet />
              </Suspense>
            )}
          </NavPanel>
        </NavTabs>
        <LogBar segments={status.log} spoken={status.logSpoken} nextScan={status.nextScan} progress={status.progress} />
      </div>
    </>
  );
}
