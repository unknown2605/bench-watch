import { useEffect } from 'react';
import { Button, ListBox, ListBoxItem, Popover, Select } from 'react-aria-components';
import { Chip } from '../../components';
import chip from '../../components/Chip/Chip.module.css';
import { RANGE_LABEL, RANGES, setClient, setProject, setRange, useFilters } from '../../app/filters';
import { useMeta } from '../../app/queries';
import type { TabId } from '../../app/tabs';
import { clientLabel, clientName } from '../../format/clients';
import { periodLabel } from '../../format/format';
import { projectLabel } from '../../format/labels';
import type_ from '../../styles/type.module.css';
import styles from './tabs.module.css';

const ALL = '__all__';
const ALL_CLIENTS = '__all_clients__';

type Props = { tab: TabId; period: { from: string; to: string } | null; note?: string | null | undefined };

export function FilterBar({ tab, period, note }: Props) {
  const { project, client, range } = useFilters(tab);
  const meta = useMeta();
  const projects = [...new Set((meta.data?.projects ?? []).map(projectLabel))].sort((a, b) => a.localeCompare(b));
  const items = [{ id: ALL, name: 'All projects' }, ...projects.map((p) => ({ id: p, name: p }))];
  const found = meta.data?.clients ?? [];
  const clientItems = [{ id: ALL_CLIENTS, name: 'All clients' },
    ...found.map((c) => ({ id: c.id, name: clientLabel(c) }))];
  const picked = found.find((c) => c.id === client);
  const loaded = meta.data !== undefined;
  // The select is hidden below two clients; a pick it can no longer undo must not keep filtering every query.
  useEffect(() => {
    if (loaded && client !== null && (found.length < 2 || picked === undefined)) setClient(null);
  }, [loaded, client, found.length, picked]);
  return (
    <div className={styles.filters}>
      <Select
        aria-label="Project"
        className={styles.projectSelect ?? ''}
        value={project ?? ALL}
        onChange={(key) => setProject(key === ALL || key === null ? null : String(key))}
      >
        <Button className={`${chip.chip} ${type_.tab} ${styles.project}`}>
          <span>Project · {project ?? 'All projects'}</span>
          <svg className={chip.caret} width="8" height="5" viewBox="0 0 8 5" aria-hidden="true">
            <path d="M0 0H8L4 5Z" />
          </svg>
        </Button>
        <Popover className={styles.popover ?? ''} placement="bottom start" offset={4}>
          <ListBox className={styles.listbox ?? ''} items={items}>
            {(item) => (
              <ListBoxItem id={item.id} className={`${type_.mono} ${styles.option}`} textValue={item.name}>
                {item.name}
              </ListBoxItem>
            )}
          </ListBox>
        </Popover>
      </Select>
      {found.length > 1 ? (
        <Select
          aria-label="Client"
          className={styles.projectSelect ?? ''}
          value={client ?? ALL_CLIENTS}
          onChange={(key) => setClient(key === ALL_CLIENTS || key === null ? null : String(key))}
        >
          <Button className={`${chip.chip} ${type_.tab} ${styles.project}`}>
            <span>Client · {client === null ? 'All clients' : picked ? clientLabel(picked) : clientName(client)}</span>
            <svg className={chip.caret} width="8" height="5" viewBox="0 0 8 5" aria-hidden="true">
              <path d="M0 0H8L4 5Z" />
            </svg>
          </Button>
          <Popover className={styles.popover ?? ''} placement="bottom start" offset={4}>
            <ListBox className={styles.listbox ?? ''} items={clientItems}>
              {(item) => (
                <ListBoxItem id={item.id} className={`${type_.mono} ${styles.option}`} textValue={item.name}>
                  {item.name}
                </ListBoxItem>
              )}
            </ListBox>
          </Popover>
        </Select>
      ) : null}
      {note ? <span className={`${type_.label} ${styles.note}`} role="status">{note}</span> : null}
      <div className={styles.ranges} role="group" aria-label="Period">
        {RANGES.map((r) => (
          <Chip key={r} isSelected={r === range} onChange={(on) => on && setRange(tab, r)}>
            {RANGE_LABEL[r]}
          </Chip>
        ))}
      </div>
      <span className={`${type_.tab} ${styles.period}`} role="group" aria-label="Period shown">
        {period ? periodLabel(period.from, period.to) : '—'}
      </span>
    </div>
  );
}
