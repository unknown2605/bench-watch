import { useId } from 'react';
import { keyed } from '../../app/keys';
import { tokens as fmtTokens } from '../../format/format';
import { arcPath, projectShort } from './geometry';
import { CountUp, Piece, RowButton, rowSelected, selectable, useActive, vars, type Selection } from './Piece';
import { SESSION_BOX, SESSION_R, SESSION_SLOTS, sessionsModel, type SessionsInput, type SessionTone } from './sessions';
import styles from './pieces.module.css';
import own from './SessionsPiece.module.css';

export const TONE_COLOR: Record<SessionTone, string> = {
  live: 'var(--sh-status-live)',
  gold: 'var(--sh-accent-gold)',
  session: 'var(--sh-data-session)',
  warn: 'var(--sh-status-warn)',
  dim: 'var(--sh-ink-dim)',
};

const two = (n: number) => String(n).padStart(2, '0');
const BRIGHT = 'var(--sh-ink-warm)';
const markColor = (state: 'live' | 'idle' | 'ended', tone: SessionTone) =>
  state === 'live' ? BRIGHT : state === 'ended' ? 'var(--sh-ink-dim)' : TONE_COLOR[tone];

type Props = Selection & { data: SessionsInput; label?: string };

export function SessionsPiece({ data, label = 'Session map', selected, onSelect }: Props) {
  const m = sessionsModel(data);
  const { on } = useActive();
  const c = SESSION_BOX / 2;
  const used = m.slots.filter((s) => s.item);
  const glow = useId();
  const sel = { selected, onSelect };
  return (
    <Piece name="sessions" label={label} state={m.state} className={own.root}>
      <div className={own.left}>
        <p className={styles.caption}>Projects</p>
        {m.legend.length ? (
          <ul className={styles.list}>
            {keyed(m.legend, (p) => p.project).map(([key, p], i) => (
              <li
                key={key}
                className={styles.row}
                style={vars({ '--c': TONE_COLOR[p.tone], '--i': i })}
                title={p.project}
              >
                <span className={styles.dot} />
                <span className={styles.name}>{p.project === 'other' ? 'other' : projectShort(p.project)}</span>
                <span className={`${styles.num} ${styles.dim}`}>{p.sessions}</span>
              </li>
            ))}
          </ul>
        ) : (
          <p className={styles.empty}>—</p>
        )}
      </div>
      <div className={`${styles.drawing} ${own.draw}`} style={{ width: SESSION_BOX, height: SESSION_BOX }}>
        <svg width={SESSION_BOX} height={SESSION_BOX} viewBox={`0 0 ${SESSION_BOX} ${SESSION_BOX}`} aria-hidden="true">
          <defs>
            <radialGradient id={glow}>
              <stop offset="0.55" stopColor="var(--sh-accent-gold)" stopOpacity="0.16" />
              <stop offset="1" stopColor="var(--sh-accent-gold)" stopOpacity="0" />
            </radialGradient>
          </defs>
          <circle
            className={`${own.orbit} ${styles.draw}`}
            cx={c}
            cy={c}
            r={SESSION_R}
            pathLength={1}
            transform={`rotate(-90 ${c} ${c})`}
          />
          <circle cx={c} cy={c} r={84} fill={`url(#${glow})`} />
          <circle className={own.hubRing} cx={c} cy={c} r={54} />
          <path className={own.hubTrack} d={arcPath(62, 0, 360)} transform={`translate(${c} ${c})`} />
          {m.liveShare > 0 ? (
            <path
              className={`${own.liveArc} ${styles.draw}`}
              d={arcPath(62, 0, m.liveShare * 360)}
              transform={`translate(${c} ${c})`}
              pathLength={1}
              style={vars({ '--i': 3 })}
            />
          ) : null}
        </svg>
        <div className={styles.hub}>
          <CountUp text={m.total ? String(m.total) : '—'} className={styles.hubValue} />
          <span className={styles.hubLabel}>Sessions</span>
          <span className={styles.hubLive}>
            <span className={own.liveDot} /> {m.live} live
          </span>
        </div>
        {keyed(used, (s) => s.item?.key ?? '').map(([key, s]) => {
          const it = s.item;
          if (!it) return null;
          return (
            <button
              type="button"
              key={key}
              className={`${own.dot} ${own[it.state]}`}
              style={vars({
                left: `${s.x - it.size / 2}px`,
                top: `${s.y - it.size / 2}px`,
                '--s': `${it.size}px`,
                '--c': markColor(it.state, it.tone),
                '--i': s.index,
              })}
              aria-label={`Rank ${it.rank}: ${it.project}, ${fmtTokens(it.tokens)} tokens, ${it.state}`}
              {...on(s.index)}
              {...selectable(sel, it.key, true)}
            >
              {it.rank}
            </button>
          );
        })}
      </div>
      <div className={own.right}>
        <p className={styles.caption}>Top {SESSION_SLOTS} sessions</p>
        {used.length ? (
          <ul className={styles.list}>
            {keyed(used, (s) => s.item?.key ?? '').map(([key, s]) => {
              const it = s.item;
              if (!it) return null;
              return (
                <li
                  key={key}
                  className={`${styles.row} ${styles.rowRank}`}
                  style={vars({ '--c': markColor(it.state, it.tone), '--i': s.index })}
                  title={it.project}
                  {...on(s.index)}
                  {...rowSelected(sel, it.key)}
                >
                  <RowButton sel={sel} id={it.key}>
                    <span className={styles.dot} />
                    <span className={styles.rank}>{two(it.rank)}</span>
                    <span className={styles.name}>{projectShort(it.project)}</span>
                    <span className={styles.num}>{fmtTokens(it.tokens)}</span>
                  </RowButton>
                </li>
              );
            })}
            {m.more > 0 ? <li className={styles.more}>+{m.more} more</li> : null}
          </ul>
        ) : (
          <p className={styles.empty}>No sessions in this range</p>
        )}
      </div>
    </Piece>
  );
}
