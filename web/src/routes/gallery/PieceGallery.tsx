import { useEffect, useRef, useState, type ReactNode } from 'react';
import {
  ContextPiece, CrossFade, DockerPiece, FillsPiece, GuardPiece, MachinePiece, OverviewPiece, SessionsPiece,
  SubagentsPiece, TokensPiece,
} from '../../charts/pieces';
import { CONTEXT_RINGS } from '../../charts/pieces/context';
import { STACK_GROUPS, HEX_PER_STACK } from '../../charts/pieces/docker';
import { FILL_TOP } from '../../charts/pieces/fills';
import { GUARD_SEGMENTS } from '../../charts/pieces/guard';
import { MACHINE_WEDGES } from '../../charts/pieces/machine';
import { TREND_SLOTS } from '../../charts/pieces/overview';
import { SESSION_SLOTS } from '../../charts/pieces/sessions';
import { SPOKES } from '../../charts/pieces/subagents';
import { TOKEN_KINDS } from '../../charts/pieces/tokens';
import {
  contextEdge, dockerEdge, EDGES, fillsEdge, guardEdge, machineEdge, overviewEdge, sessionsEdge, subagentsEdge,
  tokensEdge,
  type EdgeKey,
} from './edges';
import {
  contextFrom, DOCKER_RANGES, dockerFrom, fillsFrom, guardFrom, hasRecorded, MACHINE_RANGES, machineFrom, overviewFrom,
  RANGES,
  sessionsFrom, subagentsFrom, tokensFrom,
} from './recorded';
import { INTRO_ATTR, useIntroScene } from '../../motion/intro';
import styles from './PieceGallery.module.css';

type Card = { key: string; label: string; node: ReactNode | null };
type Entry = { key: string; title: string; what: string; cap: string; ranges: Card[]; edges: Card[] };

function entry<T>(
  key: string,
  title: string,
  what: string,
  cap: string,
  ranges: readonly { key: string; label: string; data: T | null }[],
  edge: (k: EdgeKey) => T,
  render: (d: T) => ReactNode,
): Entry {
  return {
    key,
    title,
    what,
    cap,
    ranges: ranges.map((r) => ({ key: r.key, label: r.label, node: r.data ? render(r.data) : null })),
    edges: EDGES.map((e) => ({ key: e.key, label: e.label, node: render(edge(e.key)) })),
  };
}

function entries(): Entry[] {
  return [
    entry('overview', 'Overview', 'Arcs and the daily line', `3 fixed arcs · ${TREND_SLOTS} day slots`,
      RANGES.map((r) => ({ ...r, data: overviewFrom(r.key, r.label) })), overviewEdge, (d) => <OverviewPiece
        data={d} />),
    entry('tokens', 'Tokens', 'True-proportion ring, callout list with log bars, zoom ring',
      `${TOKEN_KINDS.length} kinds in fixed slots`,
      RANGES.map((r) => ({ ...r, data: tokensFrom(r.key, r.label) })), tokensEdge, (d) => <TokensPiece data={d} />),
    entry('context', 'Context', 'Five band rings, dashed 200K ring, side list',
      `${CONTEXT_RINGS} rings, later bands fold into the last`,
      RANGES.map((r) => ({ ...r, data: contextFrom(r.key, r.label) })), contextEdge, (d) => <ContextPiece data={d} />),
    entry('sessions', 'Sessions', '12 fixed slots from 12 o\'clock, three dot sizes',
      `${SESSION_SLOTS} sessions by tokens, then +N more`,
      RANGES.map((r) => ({ ...r, data: sessionsFrom(r.key) })), sessionsEdge, (d) => <SessionsPiece data={d} />),
    entry('subagents', 'Sub-agents', 'Eight spokes with rank numbers and an other row',
      `${SPOKES} types by runs, then other`,
      RANGES.map((r) => ({ ...r, data: subagentsFrom(r.key, r.label) })), subagentsEdge, (d) => <SubagentsPiece
        data={d} />),
    entry('fills', 'Fills', 'Top six plus a small-tools slice, and the tail list',
      `${FILL_TOP} tools, then one small-tools slice`,
      RANGES.map((r) => ({ ...r, data: fillsFrom(r.key, r.label) })), fillsEdge, (d) => <FillsPiece data={d} />),
    entry('guard', 'Guard', '24 hour segments of today, teal calls and coral blocks', `${GUARD_SEGMENTS} segments`,
      RANGES.map((r) => ({ ...r, data: guardFrom(r.key) })), guardEdge, (d) => <GuardPiece data={d} />),
    entry('machine', 'Machine', 'Core wedges', `${MACHINE_WEDGES} wedges, more cores share a wedge`,
      MACHINE_RANGES.map((r) => ({ ...r, data: machineFrom(r.file) })), machineEdge, (d) => <MachinePiece data={d} />),
    entry('docker', 'Docker', 'Hexes by stack, log memory bar, an other hex',
      `${STACK_GROUPS} stacks × ${HEX_PER_STACK} hexes, then other`,
      DOCKER_RANGES.map((r) => ({ ...r, data: dockerFrom(r.file) })), dockerEdge, (d) => <DockerPiece data={d} />),
  ];
}

function CardView({ card, tint, shot }: { card: Card; tint: number; shot: string }) {
  const ref = useRef<HTMLElement>(null);
  const intro = useIntroScene(shot, true, ref);
  return (
    <article ref={ref} className={styles.card} data-tint={tint} data-shot={shot}
      {...(intro.active ? { [INTRO_ATTR]: '' } : {})}>
      <header className={styles.cardHead}>
        <span className={styles.cardLabel}>{card.label}</span>
        <span className={styles.tintName}>color/panel/tint-{tint}</span>
      </header>
      <div className={styles.stage}>{card.node ?? <p
        className={styles.missing}>No recorded response for this range</p>}</div>
    </article>
  );
}

function Demo({ e }: { e: Entry }) {
  const cards = e.ranges.filter((c) => c.node);
  const [at, setAt] = useState(0);
  const [round, setRound] = useState(0);
  useEffect(() => {
    if (cards.length < 2) return undefined;
    const id = window.setInterval(() => {
      if (!document.hidden) setAt((a) => (a + 1) % cards.length);
    }, 2600);
    return () => window.clearInterval(id);
  }, [cards.length]);
  const card = cards[at % Math.max(1, cards.length)];
  const ref = useRef<HTMLElement>(null);
  const intro = useIntroScene(`${round}-${card?.key ?? ''}`, Boolean(card), ref);
  if (!card) return null;
  return (
    <article ref={ref} className={styles.card} data-tint={1} data-demo={e.key}
      {...(intro.active ? { [INTRO_ATTR]: '' } : {})}>
      <header className={styles.cardHead}>
        <span className={styles.cardLabel}>Motion · cycles the ranges with a cross-fade · now {card.label}</span>
        <button type="button" className={styles.replay} onClick={() => setRound((r) => r + 1)}>
          Replay draw-in
        </button>
      </header>
      <div className={styles.stage}>
        <CrossFade id={`${round}-${card.key}`}>{card.node}</CrossFade>
      </div>
    </article>
  );
}

export function PieceGallery({ only, demo }: { only: string | null; demo: boolean }) {
  const list = entries().filter((e) => !only || e.key === only);
  return (
    <main className={styles.page}>
      <h1 className={styles.heading}>Centre pieces · recorded responses</h1>
      {!hasRecorded() ? <p className={styles.missing}>No gallery-data folder: run node e2e/gallery-data.mjs first.</p>
        : null}
      <nav className={styles.nav}>
        {entries().map((e) => (
          <a key={e.key} href={`?view=pieces&piece=${e.key}`}>{e.title}</a>
        ))}
      </nav>
      {list.map((e) => (
        <section key={e.key} className={styles.section} data-section={e.key}>
          <h2 className={styles.title}>
            {e.title} <span className={styles.what}>· {e.what} · cap: {e.cap}</span>
          </h2>
          {demo ? (
            <Demo e={e} />
          ) : (
            <>
              <div className={styles.cards}>
                {e.ranges.map((c, i) => (
                  <CardView key={c.key} card={c} tint={(i % 4) + 1} shot={`${e.key}-${c.key}`} />
                ))}
              </div>
              <h3 className={styles.sub}>Edge states</h3>
              <div className={styles.cards}>
                {e.edges.map((c, i) => (
                  <CardView key={c.key} card={c} tint={(i % 4) + 1} shot={`${e.key}-edge-${c.key}`} />
                ))}
              </div>
            </>
          )}
        </section>
      ))}
    </main>
  );
}
