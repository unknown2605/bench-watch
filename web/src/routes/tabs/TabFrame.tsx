import { useRef, useState, type ReactNode } from 'react';
import { ApiError } from '../../api/client';
import { Panel } from '../../components';
import { useFilters, useMetricsFilters } from '../../app/filters';
import { CrossFade } from '../../charts/pieces';
import { CLAUDE_ONLY_TABS, type TabId } from '../../app/tabs';
import { isOtherClient, NOT_FOR_CLIENT } from '../../format/clients';
import { INTRO_ATTR, useIntroScene } from '../../motion/intro';
import a11y from '../../styles/a11y.module.css';
import type_ from '../../styles/type.module.css';
import { FilterBar } from './FilterBar';
import styles from './tabs.module.css';

export type Period = { from: string; to: string; partial: boolean };

type Slots = { left: ReactNode; centre: ReactNode; bottom: ReactNode; right: ReactNode };

type Props = {
  tab: TabId;
  title: string;
  period: Period | null;
  /** Null while the first answer is pending or failed. */
  slots: Slots | null;
  empty?: boolean;
  error: unknown;
  fetching?: boolean;
  /** True while the slots still show the previous range's or project's answer. */
  placeholder?: boolean;
  emptyText?: string;
  /** Replaces the project and period bar, for the tabs that filter by host or stack and a time window. */
  filter?: ReactNode;
  /** Overview spaces its columns differently from the other tabs. */
  variant?: 'overview';
};

export function errorText(error: unknown): string {
  if (!(error instanceof ApiError)) return 'Could not load';
  if (error.code === 'history_paused' || error.status === 503) return 'History paused · database unreachable';
  if (error.code === 'network') return 'Server unreachable';
  if (error.code === 'bad_range' || error.code === 'bad_date') return 'Period not accepted';
  return `Could not load · ${error.code}`;
}

function useScene(tab: TabId): string {
  const transcript = useFilters(tab);
  const metrics = useMetricsFilters(tab);
  if (tab === 'machine' || tab === 'docker') return `${tab}|${metrics.range}|${metrics.stack ?? ''}`;
  return `${tab}|${transcript.range}|${transcript.project ?? ''}|${transcript.client ?? ''}`;
}

/** Cross-fades on a range or project switch once the new answer arrives; a live update re-renders in place. */
export function TabPiece({ tab, placeholder = false,
  children }: { tab: TabId; placeholder?: boolean; children: ReactNode }) {
  const scene = useScene(tab);
  const [shown, setShown] = useState(scene);
  if (!placeholder && shown !== scene) setShown(scene);
  return (
    <div className={styles.stage}>
      <CrossFade id={shown}>{children}</CrossFade>
    </div>
  );
}

export function TabFrame(props: Props) {
  const { tab, title, period, slots, empty = false, error, fetching = false, placeholder = false } = props;
  const { emptyText = 'No calls in this period', variant, filter } = props;
  const ref = useRef<HTMLElement>(null);
  const { client } = useFilters(tab);
  const refused = (CLAUDE_ONLY_TABS.includes(tab) && isOtherClient(client))
    || (error instanceof ApiError && error.code === 'not_for_client');
  const intro = useIntroScene(useScene(tab), slots !== null && !placeholder, ref);
  const note = refused ? null : slots && error ? `Update failed · ${errorText(error)}` : period?.partial
    ? 'History starts inside this period' : null;
  let body: ReactNode;
  if (refused) {
    body = (
      <div className={styles.state}>
        <Panel index="--" title={title} className={styles.statePanel}>
          <p className={`${type_.tab} ${styles.stateText}`} role="status">{NOT_FOR_CLIENT}</p>
        </Panel>
      </div>
    );
  } else if (!slots) {
    body = (
      <div className={error ? styles.state : `${styles.state} ${styles.wait}`}>
        <Panel index="--" title={title} tone={error ? 'warn' : 'default'} className={styles.statePanel}>
          <p className={`${type_.tab} ${styles.stateText}`} role={error ? 'alert' : 'status'}>
            {error ? errorText(error) : 'Loading'}
          </p>
        </Panel>
      </div>
    );
  } else if (empty) {
    body = (
      <div className={styles.state}>
        <Panel index="--" title={title} className={styles.statePanel}>
          <p className={`${type_.tab} ${styles.stateText}`} role="status">{emptyText}</p>
        </Panel>
      </div>
    );
  } else {
    body = (
      <div className={variant ? `${styles.grid} ${styles.overview}` : styles.grid}>
        <div className={styles.left}>{slots.left}</div>
        <div className={styles.centre}>
          <div className={styles.piece}>
            {slots.centre}
          </div>
          <div className={styles.bottom}>{slots.bottom}</div>
        </div>
        <div className={styles.right}>{slots.right}</div>
      </div>
    );
  }
  return (
    <main
      ref={ref}
      className={styles.page}
      data-page={title}
      {...(intro.active ? { [INTRO_ATTR]: '' } : {})}
      aria-busy={fetching && !slots}
    >
      <h1 className={a11y.hidden}>{title}</h1>
      {filter ?? <FilterBar tab={tab} period={period} note={note} />}
      {body}
    </main>
  );
}
