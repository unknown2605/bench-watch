import a11y from '../styles/a11y.module.css';

type Cell = string | number;
type Props = { id: string; caption: string; head: readonly string[]; rows: readonly (readonly Cell[])[] };

/** Screen-reader copy of a chart's numbers; the svg points here with aria-describedby. */
export function ChartTable({ id, caption, head, rows }: Props) {
  return (
    <div className={a11y.hidden}>
      <table id={id}>
        <caption>{caption}</caption>
        <thead>
          <tr>
            {head.map((h) => (
              <th key={h} scope="col">{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i}>
              {row.map((cell, j) => (
                <td key={`${i}-${j}`}>{cell}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export const percent = (v: number) => `${Math.round(Math.max(0, Math.min(1, v)) * 100)}%`;
