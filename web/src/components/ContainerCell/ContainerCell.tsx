import { useId } from 'react';
import { HealthMark, type Health } from '../HealthMark';
import type_ from '../../styles/type.module.css';
import styles from './ContainerCell.module.css';

type Props = { name: string; value: string; health?: Health; className?: string | undefined };

export const HEX_W = 128;
export const HEX_H = 98;
export const HEX_POINTS = `0.5,49 32.3,0.5 95.7,0.5 127.5,49 95.7,97.5 32.3,97.5`;

export function ContainerCell({ name, value, health = 'healthy', className }: Props) {
  const id = useId();
  return (
    <div className={[styles.cell, styles[health], className].filter(Boolean).join(' ')} role="group" aria-label={`${name}, ${health}`}>
      <svg className={styles.hex} width={HEX_W} height={HEX_H} aria-hidden="true">
        <defs>
          <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" className={styles.stopTop} />
            <stop offset="1" className={styles.stopBottom} />
          </linearGradient>
        </defs>
        <polygon className={styles.base} points={HEX_POINTS} />
        <polygon fill={`url(#${id})`} points={HEX_POINTS} />
        <polygon className={styles.stroke} points={HEX_POINTS} />
      </svg>
      <HealthMark className={styles.dot} health={health} />
      <span className={`${type_.body} ${styles.name}`} title={name}>{name}</span>
      <span className={`${type_.mono} ${styles.value}`}>{value}</span>
    </div>
  );
}
