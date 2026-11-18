import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { useEffect, useEffectEvent, useId, useRef } from 'react';
import { useElementSize } from '../components/useElementSize';
import { ChartTable } from './ChartTable';
import type { DataTone } from './colors';
import motion from './motion.module.css';
import styles from './charts.module.css';

const TONE_PROP: Record<DataTone, string> = {
  gold: '--sh-accent-gold',
  live: '--sh-status-live',
  warn: '--sh-status-warn',
  session: '--sh-data-session',
  dim: '--sh-ink-dim',
};

type Props = { data: uPlot.AlignedData; tone?: DataTone; height?: number; label: string; yMax?: number };

/** Colours are read from the tokens when the plot is created, so a token change needs a remount. */
export function UPlotLine({ data, tone = 'live', height = 160, label, yMax }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const tableId = useId();
  const [width] = useElementSize(host);
  const sized = width > 0;
  const current = useEffectEvent(() => ({ data, width }));

  useEffect(() => {
    const el = host.current;
    if (!el || !sized) return;
    const first = current();
    const css = getComputedStyle(el);
    const color = css.getPropertyValue(TONE_PROP[tone]).trim();
    const dim = css.getPropertyValue('--sh-ink-dim').trim();
    const axis = { stroke: dim, grid: { stroke: `${dim}22`, width: 1 }, ticks: { show: false }, font: '10px "Share Tech Mono"' };
    plot.current = new uPlot(
      {
        width: first.width,
        height,
        legend: { show: false },
        cursor: { show: false },
        select: { show: false, left: 0, top: 0, width: 0, height: 0 },
        scales: { x: { time: true }, y: { range: [0, yMax ?? null] as uPlot.Range.MinMax } },
        axes: [axis, { ...axis, size: 40 }],
        series: [{}, { stroke: color, width: 1.5, fill: `${color}22` }],
      },
      first.data,
      el,
    );
    return () => {
      plot.current?.destroy();
      plot.current = null;
    };
  }, [tone, height, yMax, sized]);

  useEffect(() => {
    plot.current?.setData(data);
  }, [data]);

  useEffect(() => {
    if (sized) plot.current?.setSize({ width, height });
  }, [sized, width, height]);

  const ys = (data[1] ?? []).filter((v): v is number => typeof v === 'number');
  let min = Infinity;
  let max = -Infinity;
  for (const v of ys) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const summary: (readonly [string, number])[] = ys.length
    ? [['Latest', ys[ys.length - 1] ?? 0], ['Minimum', min], ['Maximum', max], ['Points', ys.length]]
    : [];
  return (
    <>
        <div
          ref={host}
          className={`${styles.uplot} ${motion.fade}`}
          role="img"
          aria-label={label}
          aria-describedby={tableId}
        />
        <ChartTable id={tableId} caption={label} head={['Measure', 'Value']} rows={summary.map(([k, v]) => [k, Number(v.toFixed(2))])} />
    </>
  );
}
