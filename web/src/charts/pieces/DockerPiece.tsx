import { keyed } from '../../app/keys';
import { int, rate, size } from '../../format/format';
import { CountUp, Piece, useActive, vars } from './Piece';
import { HEX_H, HEX_W, dockerModel, type DockerInput, type Hex } from './docker';
import styles from './pieces.module.css';
import own from './DockerPiece.module.css';

const HEX_POINTS = (() => {
  const w = HEX_W - 2;
  const h = HEX_H - 2;
  const pts = [
    [w / 2 + 1, 1], [w + 1, h / 4 + 1], [w + 1, (h * 3) / 4 + 1],
    [w / 2 + 1, h + 1], [1, (h * 3) / 4 + 1], [1, h / 4 + 1],
  ];
  return pts.map(([x, y]) => `${x},${y}`).join(' ');
})();

const STATE_WORD: Record<Hex['health'], string> = {
  ok: 'healthy', none: 'no healthcheck', finished: 'finished', warn: 'needs a look',
};

/** A folded hex has no word for 'none': there it only means the containers do not agree. */
const stateWord = (h: Hex) => (h.count > 1 && h.health === 'none' ? '' : STATE_WORD[h.health]);

/** Wide enough for a short stack heading such as "NO STACK · 1" above a one-hex comb. */
const GROUP_MIN_W = 100;

/** Offers a line break after each _ or - so a long container name wraps at its own word boundaries. */
const breakable = (name: string) =>
  name.split(/(?<=[_-])/).map((part, i) => (i ? [<wbr key={i} />, part] : part));

export function DockerPiece({ data, label = 'Containers by stack' }: { data: DockerInput; label?: string }) {
  const m = dockerModel(data);
  const { on } = useActive();
  const groups = m.groups;
  const offsets = groups.map((_, gi) => groups.slice(0, gi).reduce((a, g) => a + g.hexes.length, 0));
  return (
    <Piece name="docker" label={label} state={m.state} className={own.root}>
      {groups.length ? (
        <div className={own.groups}>
          {keyed(groups, (g) => g.key).map(([groupKey, g], gi) => (
            <section key={groupKey} className={own.group} style={{ width: Math.max(g.width, GROUP_MIN_W) }}>
              <header className={own.groupHead}>
                <span className={own.groupTitle} title={g.title}>{g.title}</span>
                <span className={own.groupCount}>· {g.count}</span>
              </header>
              <div className={own.comb} style={{ width: g.width, height: g.height }}>
                {keyed(g.hexes, (h) => h.key).map(([hexKey, h], hi) => {
                  const i = (offsets[gi] ?? 0) + hi;
                  const word = stateWord(h);
                  const mem = size(h.memBytes, 1024);
                  return (
                    <div
                      key={hexKey}
                      className={`${own.hex} ${own[h.kind]} ${own[h.health]}`}
                      style={vars({ left: `${h.x}px`, top: `${h.y}px`, '--i': i })}
                      tabIndex={0}
                      role="img"
                      aria-label={`${h.name}: ${mem}${h.count > 1 ? `, ${h.count} containers` : ''}${
                        word ? `, ${word}` : ''}`}
                      title={word ? `${h.name} · ${word}` : h.name}
                      {...on(i)}
                    >
                      <svg className={own.hexShape} width={HEX_W} height={HEX_H} aria-hidden="true">
                        <polygon points={HEX_POINTS} />
                      </svg>
                      <span className={own.health} />
                      <span className={own.hexName}>{breakable(h.name)}</span>
                      <span className={own.hexMem}>{size(h.memBytes, 1024)}</span>
                      <span className={own.hexBar}>
                        <span style={vars({ '--w': h.bar })} />
                      </span>
                    </div>
                  );
                })}
              </div>
              <p className={own.groupFoot}>
                {g.stack
                  ? `MEM ${size(g.stack.memBytes, 1024)} · ↓ ${rate(g.stack.rxBps)} · ↑ ${rate(g.stack.txBps)}`
                  : `MEM ${size(m.otherMem, 1024)}`}
              </p>
            </section>
          ))}
        </div>
      ) : (
        <p className={`${styles.empty} ${own.noContainers}`}>No containers running</p>
      )}
      <div className={own.summary}>
        <CountUp text={int(m.total)} className={own.total} />
        <span className={own.summaryText}>
          {m.total === 1 ? 'container' : 'containers'} · {int(m.healthy)} healthy · {int(m.noCheck)} without healthcheck
          {m.finished ? ` · ${int(m.finished)} finished` : ''}
          {m.warn ? ` · ${int(m.warn)} ${m.warn === 1 ? 'needs' : 'need'} a look` : ''} · {int(m.restarts)}{' '}
          {m.restarts === 1 ? 'restart' : 'restarts'} seen by this agent run
        </span>
      </div>
      <p className={styles.note}>
        Hex = container · bar = memory on a log scale, 5 MB to 1 GB · teal = healthy · hollow dot = no healthcheck ·
        grey square = finished · coral = needs a look
      </p>
    </Piece>
  );
}
