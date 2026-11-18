import { useId } from 'react';
import styles from './Planet.module.css';

export type PlanetKind = 'idle' | 'live' | 'selected';
type Props = { kind: PlanetKind; size?: number; label: string; className?: string | undefined };

type GlyphProps = { kind: PlanetKind; cx: number; cy: number; size: number; ids: PlanetIds };
export type PlanetIds = { idle: string; live: string; haloIdle: string; haloLive: string };

export function usePlanetIds(): PlanetIds {
  const id = useId();
  return { idle: `${id}-bi`, live: `${id}-bl`, haloIdle: `${id}-hi`, haloLive: `${id}-hl` };
}

/** Gradients the glyphs refer to; render once inside any svg that draws planets. */
export function PlanetDefs({ ids }: { ids: PlanetIds }) {
  return (
    <defs>
      <radialGradient id={ids.haloIdle}>
        <stop offset="0" className={styles.haloIdleA} />
        <stop offset="1" className={styles.haloIdleB} />
      </radialGradient>
      <radialGradient id={ids.haloLive}>
        <stop offset="0" className={styles.haloLiveA} />
        <stop offset="1" className={styles.haloLiveB} />
      </radialGradient>
      <radialGradient id={ids.idle}>
        <stop offset="0" className={styles.core} />
        <stop offset="0.4" className={styles.idleMid} />
        <stop offset="1" className={styles.idleEdge} />
      </radialGradient>
      <radialGradient id={ids.live}>
        <stop offset="0" className={styles.core} />
        <stop offset="0.4" className={styles.liveMid} />
        <stop offset="1" className={styles.liveEdge} />
      </radialGradient>
    </defs>
  );
}

/** Drawn on a 48 unit grid scaled by size / 48, centred on cx, cy. */
export function PlanetGlyph({ kind, cx, cy, size, ids }: GlyphProps) {
  const live = kind !== 'idle';
  const k = size / 48;
  const bracket = (x: number, y: number, d: string) => (
    <path key={`${x}${y}`} className={styles.bracket} d={d} transform={`translate(${x} ${y})`} />
  );
  return (
    <g transform={`translate(${cx - 24 * k} ${cy - 24 * k}) scale(${k})`}>
      <circle cx="24" cy="24" r="22" fill={`url(#${live ? ids.haloLive : ids.haloIdle})`} />
      <circle className={live ? styles.bodyLive : styles.bodyIdle} cx="24" cy="24" r="8" fill={`url(#${live ? ids.live : ids.idle})`} />
      {live ? <circle className={styles.ring} cx="24" cy="24" r="13.5" /> : null}
      {kind === 'selected' ? (
        <>
          {bracket(0.75, 0.75, 'M0 7.25 V0 H7.25')}
          {bracket(40.25, 0.75, 'M0 0 H7.25 V7.25')}
          {bracket(40.25, 40.25, 'M7.25 0 V7.25 H0')}
          {bracket(0.75, 40.25, 'M7.25 7.25 H0 V0')}
        </>
      ) : null}
    </g>
  );
}

export function Planet({ kind, size = 48, label, className }: Props) {
  const ids = usePlanetIds();
  return (
    <svg className={[styles.planet, className].filter(Boolean).join(' ')} width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img" aria-label={label}>
      <PlanetDefs ids={ids} />
      <PlanetGlyph kind={kind} cx={size / 2} cy={size / 2} size={size} ids={ids} />
    </svg>
  );
}
