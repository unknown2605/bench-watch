import { useEffect, useRef } from 'react';
import { Link } from '@tanstack/react-router';
import { Panel } from '../components';
import a11y from '../styles/a11y.module.css';
import styles from './Placeholder.module.css';

export function NotOnThisMachine({ label }: { label: string }) {
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), []);
  return (
    <main className={styles.page} data-page="not-on-this-machine">
      <h1 ref={heading} tabIndex={-1} className={a11y.hidden}>{label}</h1>
      <Panel index="N/A" title={`${label}: not on this machine`} tone="warn">
        <Link to="/" className={styles.link}>Back to Overview</Link>
      </Panel>
    </main>
  );
}
