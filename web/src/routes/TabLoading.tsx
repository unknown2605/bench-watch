import type_ from '../styles/type.module.css';
import styles from './TabLoading.module.css';

export function TabLoading() {
  return (
    <div className={styles.loading} role="status">
      <p className={type_.tab}>Loading</p>
    </div>
  );
}
