import { Button, ListBox, ListBoxItem, Popover, Select } from 'react-aria-components';
import { Chip } from '../../components';
import chip from '../../components/Chip/Chip.module.css';
import { METRIC_RANGE_LABEL, METRIC_RANGES, setMetricsRange, setStack, useMetricsFilters } from '../../app/filters';
import { useMeta } from '../../app/queries';
import type { TabId } from '../../app/tabs';
import type_ from '../../styles/type.module.css';
import styles from './tabs.module.css';

const ALL = '__all__';

type Props = {
  tab: TabId;
  scope: 'host' | 'stack';
  /** Clock of the latest snapshot; it moves with every refresh whatever range is drawn. */
  updated: string | null;
  note?: string | null | undefined;
};

export function MetricsFilter({ tab, scope, updated, note }: Props) {
  const { range, stack } = useMetricsFilters(tab);
  const meta = useMeta();
  const stacks = [...new Set(meta.data?.stacks ?? [])].sort((a, b) => a.localeCompare(b));
  const items = [{ id: ALL, name: 'All stacks' }, ...stacks.map((s) => ({ id: s, name: s }))];
  return (
    <div className={styles.filters}>
      {scope === 'host' ? (
        <span className={`${chip.chip} ${type_.tab} ${styles.project} ${styles.fixedChip}`}>Host · This Mac</span>
      ) : (
        <Select aria-label="Stack" className={styles.projectSelect ?? ''} value={stack ?? ALL} onChange={(key) => setStack(key === ALL || key === null ? null : String(key))}>
          <Button className={`${chip.chip} ${type_.tab} ${styles.project}`}>
            <span>Stack · {stack ?? 'All stacks'}</span>
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
      )}
      {note ? <span className={`${type_.label} ${styles.note}`} role="status">{note}</span> : null}
      <div className={styles.ranges} role="group" aria-label="Range">
        {METRIC_RANGES.map((r) => (
          <Chip key={r} isSelected={r === range} onChange={(on) => on && setMetricsRange(tab, r)}>
            {METRIC_RANGE_LABEL[r]}
          </Chip>
        ))}
      </div>
      <span className={`${type_.tab} ${styles.period}`} role="group" aria-label="Last update">
        {updated ? `Updated ${updated}` : 'Updated —'}
      </span>
    </div>
  );
}
