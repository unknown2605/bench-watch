import { useState, type ReactNode } from 'react';
import { useReducedMotion } from '../../motion/useReducedMotion';
import styles from './CrossFade.module.css';

type Layer = { id: string; node: ReactNode };

/** A new `id` (range or project) fades the old piece out over the new one; reduced motion swaps at once. */
export function CrossFade({ id, children }: { id: string; children: ReactNode }) {
  const reduced = useReducedMotion();
  const [cur, setCur] = useState<Layer>({ id, node: children });
  const [prev, setPrev] = useState<Layer | null>(null);
  if (cur.id !== id) {
    setPrev(reduced ? null : cur);
    setCur({ id, node: children });
  } else if (cur.node !== children) {
    setCur({ id, node: children });
  }
  return (
    <div className={styles.stack}>
      {prev ? (
        <div
          key={`out-${prev.id}`}
          className={styles.leaving}
          aria-hidden="true"
          onAnimationEnd={(e) => {
            if (e.target === e.currentTarget) setPrev(null);
          }}
        >
          {prev.node}
        </div>
      ) : null}
      <div key={`in-${id}`} className={prev ? styles.entering : styles.still}>
        {children}
      </div>
    </div>
  );
}
