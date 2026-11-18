import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Panel } from '../components';
import a11y from '../styles/a11y.module.css';
import styles from '../routes/Placeholder.module.css';

export function ErrorView({ reset }: { reset?: () => void }) {
  return (
    <main className={styles.page} data-page="error">
      <h1 className={a11y.hidden}>Something went wrong</h1>
      <Panel index="ERR" title="Something went wrong" tone="warn">
        <p role="alert">The page could not be shown.</p>
        <button type="button" className={styles.action} onClick={reset ?? (() => window.location.reload())}>
          Try again
        </button>
      </Panel>
    </main>
  );
}

type State = { failed: boolean };

/** Last resort around the whole app; routes have their own boundary through the router's default error component. */
export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  override state: State = { failed: false };

  static getDerivedStateFromError(): State {
    return { failed: true };
  }

  override componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(error, info.componentStack);
  }

  override render() {
    return this.state.failed ? <ErrorView /> : this.props.children;
  }
}
