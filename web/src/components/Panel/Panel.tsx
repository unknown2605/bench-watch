import { useRef, type ReactNode } from 'react';
import { CutFrame } from '../CutFrame';
import type { Tone } from '../tone';
import tones from '../tones.module.css';
import { useElementSize } from '../useElementSize';
import type_ from '../../styles/type.module.css';
import styles from './Panel.module.css';

type Props = {
  index?: string;
  title: string;
  tone?: Tone;
  children?: ReactNode;
  className?: string | undefined;
  bodyClassName?: string | undefined;
};

export function Panel({ index, title, tone = 'default', children, className, bodyClassName }: Props) {
  const ref = useRef<HTMLElement>(null);
  const [w, h] = useElementSize(ref);
  return (
    <section ref={ref} className={[styles.panel, tones[tone], className].filter(Boolean).join(' ')} aria-label={title}>
      <CutFrame width={w} height={h} />
      <span className={styles.bar} />
      <header className={styles.header}>
        {index ? <span className={`${type_.monoS} ${styles.index}`}>{index}</span> : null}
        <h2 className={`${type_.heading} ${styles.title}`}>{title}</h2>
      </header>
      <div className={[styles.body, bodyClassName].filter(Boolean).join(' ')}>{children}</div>
    </section>
  );
}
