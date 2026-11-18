import { Link } from '@tanstack/react-router';
import { Panel } from '../components';
import a11y from '../styles/a11y.module.css';
import styles from './Placeholder.module.css';

export function NotFound() {
  return (
    <main className={styles.page} data-page="not-found">
      <h1 className={a11y.hidden}>Page not found</h1>
      <Panel index="404" title="Page not found" tone="warn">
        <Link to="/" className={styles.link}>Back to Overview</Link>
      </Panel>
    </main>
  );
}
