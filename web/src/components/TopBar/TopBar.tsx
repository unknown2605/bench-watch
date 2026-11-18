import { Tab, TabList, TabPanel, Tabs } from 'react-aria-components';
import { useEffect, useState, type ReactNode } from 'react';
import { cssZoomOf } from '../../app/uiScale';
import { Status } from '../Status';
import type_ from '../../styles/type.module.css';
import styles from './TopBar.module.css';

/** Room kept between the selected tab and the edge of the screen. */
const EDGE = 8;
const SETTLE_MS = 60;

export type NavTab = { id: string; label: string };

type TabsProps = { activeKey: string | null; onSelect: (key: string) => void; children: ReactNode };

/** Owns the tab state; the bar and the page panel sit inside it so aria-controls resolves. */
export function NavTabs({ activeKey, onSelect, children }: TabsProps) {
  return (
    <Tabs
      {...(styles.tabs ? { className: styles.tabs } : {})}
      {...(activeKey !== null ? { selectedKey: activeKey } : {})}
      onSelectionChange={(key) => onSelect(String(key))}>
      {children}
    </Tabs>
  );
}

type NavPanelProps = { id: string; children: ReactNode; className?: string | undefined; unlisted?: string | undefined };

/** `unlisted` names the page of a route whose tab is not rendered; a tab panel would point aria-labelledby at that tab. */
export function NavPanel({ id, children, className, unlisted }: NavPanelProps) {
  if (unlisted !== undefined) {
    return (
      <div id={id} role="region" aria-label={unlisted} {...(className ? { className } : {})}>
        {children}
      </div>
    );
  }
  return (
    <TabPanel id={id} {...(className ? { className } : {})} shouldForceMount={false}>
      {children}
    </TabPanel>
  );
}

type Props = {
  tabs: readonly NavTab[];
  dividerBefore?: string;
  updated: string;
  health: string;
  live?: boolean;
};

export function TopBar({ tabs, dividerBefore, updated, health, live = true }: Props) {
  const [list, setList] = useState<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!list) return undefined;
    const reveal = () => {
      const tab = list.querySelector<HTMLElement>('[data-selected]');
      if (!tab) return;
      const box = list.getBoundingClientRect();
      const at = tab.getBoundingClientRect();
      const style = getComputedStyle(list);
      const left = Math.max(box.left + (parseFloat(style.paddingLeft) || 0), 0) + EDGE;
      const right = Math.min(box.right - (parseFloat(style.paddingRight) || 0), window.innerWidth) - EDGE;
      const zoom = cssZoomOf(list);
      if (at.left < left) list.scrollBy?.({ left: (at.left - left) / zoom });
      else if (at.right > right) list.scrollBy?.({ left: (at.right - right) / zoom });
    };
    reveal();
    let later: ReturnType<typeof setTimeout> | undefined;
    // react-aria scrolls the focused tab by its own offsets in a passive effect; reveal runs after it.
    const watch = new MutationObserver(() => {
      clearTimeout(later);
      later = setTimeout(reveal, SETTLE_MS);
    });
    watch.observe(list, { subtree: true, childList: true, attributes: true, attributeFilter: ['data-selected'] });
    return () => {
      watch.disconnect();
      clearTimeout(later);
    };
  }, [list]);
  return (
    <header className={styles.bar}>
      <span className={styles.edge} aria-hidden="true" />
      <div className={styles.brand}>
        <svg className={styles.logo} width="24" height="24" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M0.75 12 L12 0.75 L23.25 12 L12 23.25 Z" />
          <rect x="8" y="8" width="8" height="8" />
        </svg>
        <span className={styles.product}>Benchwatch</span>
      </div>
      <TabList ref={setList} {...(styles.list ? { className: styles.list } : {})} aria-label="Sections">
        {tabs.map((tab) => (
          <Tab
            key={tab.id}
            id={tab.id}
            className={[type_.tab, styles.tab, tab.id === dividerBefore ? styles.divided : ''].filter(Boolean).join(' ')}
          >
            {tab.label}
          </Tab>
        ))}
      </TabList>
      <div className={styles.right}>
        <Status kind={live ? 'live' : 'idle'} />
        <span className={`${type_.mono} ${styles.updated}`}>{updated}</span>
        <span className={`${type_.label} ${styles.health}`}>{health}</span>
      </div>
    </header>
  );
}
