import { ToggleButton } from 'react-aria-components';
import type { ReactNode } from 'react';
import type_ from '../../styles/type.module.css';
import styles from './Chip.module.css';

type Props = {
  children: ReactNode;
  isSelected?: boolean | undefined;
  onChange?: ((selected: boolean) => void) | undefined;
  caret?: boolean;
  className?: string | undefined;
};

export function Chip({ children, isSelected, onChange, caret = false, className }: Props) {
  return (
    <ToggleButton
      className={[styles.chip, type_.tab, className].filter(Boolean).join(' ')}
      {...(isSelected !== undefined ? { isSelected } : {})}
      {...(onChange ? { onChange } : {})}
    >
      {children}
      {caret ? (
        <svg className={styles.caret} width="8" height="5" viewBox="0 0 8 5" aria-hidden="true">
          <path d="M0 0H8L4 5Z" />
        </svg>
      ) : null}
    </ToggleButton>
  );
}
