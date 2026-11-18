import { dayLabel, int, localDay, whenLabel } from '../../format/format';
import { finite, pieceState } from './geometry';

export const GUARD_SEGMENTS = 24;
export const GUARD_BOX = 340;

export type GuardHour = { hour: number; calls: number; blocked: number };
export type GuardInput = {
  blocked: number | null;
  /** One entry per hour of today; null until the guard log is read. */
  hours: readonly GuardHour[] | null;
  bash: number | null;
  file: number | null;
  /** Share of guard calls made by sub-agents, 0 to 1. */
  subShare: number | null;
  lastBlock: string | null;
  active: boolean | null;
  version: string | null;
  mode: string | null;
  /** Status of the guard log; shown whatever the figures are. */
  log?: GuardLogStatus | null;
};

export type GuardLogStatus = { text: string; skipped: string | null; warn: boolean };

export const GUARD_WAITING: GuardInput = {
  blocked: null,
  hours: null,
  bash: null,
  file: null,
  subShare: null,
  lastBlock: null,
  active: null,
  version: null,
  mode: null,
};

type Count = number | bigint | string;
type GuardLike = {
  install?: { source: 'system' | 'user' | 'none'; bash: string; file: string; build?: string; installedAt?: string };
  trend?: readonly { blocked: Count }[];
  decisions?: { deny: Count } | null;
  byTool?: readonly { tool: string; calls: Count }[];
  subAgent?: { calls: Count; sub: Count } | null;
  hours?: readonly { hour: number; calls: Count; deny: Count }[] | null;
  lastBlock?: { at: string; tool: string; rule?: string } | null;
  log?: {
    state: 'ok' | 'starting' | 'missing' | 'absent' | 'unreadable' | 'disabled';
    code?: string;
    lastReadAt?: string;
    skipped?: Partial<Record<(typeof SKIPPED)[number], Count>>;
  };
};

const SKIPPED = ['malformed', 'oversized', 'badVersion', 'badRule', 'notRegular'] as const;
const CODE = /^[A-Z][A-Z0-9_]{1,31}$/;

/** The log line under the shield: state, then the read time or error code; skipped counts follow as a second line. */
export function guardLogStatus(log: NonNullable<GuardLike['log']>, now: Date = new Date()): GuardLogStatus {
  const read = log.lastReadAt ? whenLabel(log.lastReadAt, now) : '';
  const text: Record<typeof log.state, string> = {
    ok: read ? `Log ok · read ${read}` : 'Log ok',
    starting: 'Reading the log…',
    missing: 'Log missing · stored figures',
    absent: 'Log absent · no stored figures',
    unreadable: `${log.code && CODE.test(log.code) ? `Log unreadable · ${log.code}` : 'Log unreadable'}${
      read ? ` · read ${read}` : ''}`,
    disabled: 'Collector off',
  };
  const skipped = SKIPPED.map((k) => [k, Number(log.skipped?.[k] ?? 0)] as const).filter(([, n]) => n > 0);
  return {
    text: Object.hasOwn(text, log.state) ? text[log.state] : 'Log state unknown',
    skipped: skipped.length ? `Skipped ${skipped.map(([k, n]) => `${int(n)} ${k}`).join(', ')}` : null,
    warn: log.state !== 'ok',
  };
}

const modeLine = (i: NonNullable<GuardLike['install']>) => {
  const at = i.installedAt ? new Date(i.installedAt) : null;
  const installed = at && !Number.isNaN(at.getTime()) ? ` · installed ${dayLabel(localDay(at))}` : '';
  return `bash=${i.bash} · file=${i.file} · ${i.source} install${installed}`;
};

/** Piece fields from a guard response; null where the response lacks the figure, never a zero for a missing log. */
export function guardPieceFields(d: GuardLike, now: Date): GuardInput {
  const { install, trend } = d;
  const fromTrend = trend?.length ? trend.reduce((s, r) => s + Number(r.blocked), 0) : null;
  const blocked = d.decisions ? Number(d.decisions.deny) : fromTrend;
  const tool = (name: string) => {
    const row = d.byTool?.find((t) => t.tool === name);
    return row ? Number(row.calls) : null;
  };
  const sub = d.subAgent && Number(d.subAgent.calls) > 0 ? Number(d.subAgent.sub) / Number(d.subAgent.calls) : null;
  const last = d.lastBlock;
  const lastBlock = last ? [whenLabel(last.at, now), last.tool, last.rule].filter(Boolean).join(' · ') : null;
  const active = !install ? null : install.source === 'none' ? false
    : install.bash === 'unknown' && install.file === 'unknown' ? null : true;
  return {
    blocked,
    hours: d.hours ? d.hours.map((h) => ({ hour: h.hour, calls: Number(h.calls), blocked: Number(h.deny) })) : null,
    bash: tool('bash'),
    file: tool('file'),
    subShare: sub,
    lastBlock,
    active,
    version: install?.build ? `build ${install.build}` : null,
    mode: install && install.source !== 'none' ? modeLine(install) : null,
    log: d.log ? guardLogStatus(d.log, now) : null,
  };
}

export type GuardSegment = {
  index: number;
  hour: number;
  calls: number;
  blocked: number;
  status: 'calls' | 'block' | 'none';
};

export function guardModel(input: GuardInput) {
  const byHour = new Map((input.hours ?? []).map((h) => [h.hour, h]));
  const segments: GuardSegment[] = Array.from({ length: GUARD_SEGMENTS }, (_, i) => {
    const h = byHour.get(i);
    const calls = finite(h?.calls ?? 0);
    const blocked = finite(h?.blocked ?? 0);
    return { index: i, hour: i, calls, blocked, status: blocked > 0 ? 'block' : calls > 0 ? 'calls' : 'none' };
  });
  return { segments, waiting: input.hours === null, state: pieceState(segments.map((s) => s.calls), GUARD_SEGMENTS) };
}
