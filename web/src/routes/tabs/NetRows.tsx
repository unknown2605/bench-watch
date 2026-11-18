import { ListBox, ListBoxItem } from 'react-aria-components';
import type_ from '../../styles/type.module.css';
import own from './NetRows.module.css';

export type NetRow = {
  id: string;
  name: string;
  kind: string;
  down: string;
  up: string;
  sparkDown: readonly number[];
  sparkUp: readonly number[];
};

type Props = { rows: readonly NetRow[]; selected: string | null; onSelect: (id: string) => void };

const SPARK_W = 56;
const SPARK_H = 11;
const SPARK_POINTS = 40;

function path(values: readonly number[], top: number): string {
  const v = values.slice(-SPARK_POINTS);
  const step = v.length > 1 ? SPARK_W / (v.length - 1) : 0;
  return v.map((n, i) => `${(i * step).toFixed(1)},${(SPARK_H - 1 - (n / top) * (SPARK_H - 2)).toFixed(1)}`).join(' ');
}

function Spark({ down, up }: { down: readonly number[]; up: readonly number[] }) {
  const top = Math.max(1, ...down.slice(-SPARK_POINTS), ...up.slice(-SPARK_POINTS));
  return (
    <svg className={own.spark} width={SPARK_W} height={SPARK_H} aria-hidden="true">
      <polyline className={own.sparkDown} fill="none" points={path(down, top)} />
      <polyline className={own.sparkUp} fill="none" points={path(up, top)} />
    </svg>
  );
}

export function NetRows({ rows, selected, onSelect }: Props) {
  if (rows.length === 0) return <p className={`${type_.mono} ${own.none}`}>No active interfaces</p>;
  return (
    <ListBox
      aria-label="Network interfaces"
      className={own.list ?? ''}
      selectionMode="single"
      disallowEmptySelection
      selectedKeys={selected === null ? [] : [selected]}
      onSelectionChange={(keys) => {
        const [first] = keys === 'all' ? [] : [...keys];
        if (typeof first === 'string') onSelect(first);
      }}
    >
      {rows.map((r) => (
        <ListBoxItem key={r.id} id={r.id} className={own.row ?? ''} textValue={`${r.name}, ${r.kind}`}>
          <span className={`${type_.mono} ${own.name}`}>{r.name}</span>
          <span className={`${type_.monoS} ${own.kind}`}>{r.kind}</span>
          <span className={`${type_.monoS} ${own.down}`}>↓ {r.down}</span>
          <span className={`${type_.monoS} ${own.up}`}>↑ {r.up}</span>
          <Spark down={r.sparkDown} up={r.sparkUp} />
        </ListBoxItem>
      ))}
    </ListBox>
  );
}
