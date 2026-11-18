import type { Schemas } from '../api';
import { clientName } from '../format/clients';
import { clock, int, localDay, since, tokens } from '../format/format';
import { clamp01 } from '../format/scale';
import { num, numOrNull, projectLabel, rows } from '../format/labels';

export type ShellStatus = {
  updated: string;
  health: string;
  live: boolean;
  log: readonly string[];
  logSpoken: readonly string[];
  nextScan: string | null;
  progress: number;
};

export const NO_DATA: ShellStatus = {
  updated: 'NO DATA YET',
  health: 'WAITING FOR DATA',
  live: false,
  log: ['NO DATA YET'],
  logSpoken: ['NO DATA YET'],
  nextScan: null,
  progress: 0,
};

const DEFAULT_SCAN_SECONDS = 10;
const HEALTH: Record<Schemas['Health'], string> = { nominal: 'ALL NOMINAL', degraded: 'DEGRADED', failed: 'FAILED' };
const LOG_COMPACTIONS = 2;
const LOG_SEPARATOR = '   ·   ';
// About 182 characters fit the 1183 px box at 1440 in the mono face (6.5 px each); stay under it.
const LOG_FIT_CHARS = 170;
const SCAN_LOG: Record<Schemas['ScanState'], string> = {
  ok: 'SCAN OK',
  running: 'SCAN RUNNING',
  locked: 'SCAN WAITING',
  failed: 'SCAN FAILED',
  never_ran: 'NO SCAN YET',
};

function compactionLog(c: Schemas['Compaction']): string {
  const after = numOrNull(c.tokensAfter);
  const freed = after === null ? '' : ` → ${tokens(after)}`;
  return `${clock(c.at)} COMPACTION ${projectLabel(c.project)} ${tokens(num(c.tokensBefore))}${freed}`;
}

/** One entry per client whose reader skipped lines or could not read files; compact fits 1440 px, full is for screen readers. */
function driftLog(counts: Schemas['ScanStatus']['byClient'] | undefined, full: boolean): string[] {
  return rows(counts).flatMap((c) => {
    const skipped = num(c.skippedLines);
    const unread = num(c.unreadable);
    const parts = full
      ? [...(skipped > 0 ? [`${int(skipped)} SKIPPED LINES`] : []), ...(unread > 0 ? [`${int(unread)} UNREADABLE`] : [])]
      : [...(skipped > 0 ? [`SKIP ${int(skipped)}`] : []), ...(unread > 0 ? [`UNREAD ${int(unread)}`] : [])];
    return parts.length ? [`${clientName(c.client).toUpperCase()} ${parts.join(full ? ', ' : ' ')}`] : [];
  });
}

/** The header and the status line, from /api/status; `now` moves the "3 s ago" text between answers. */
export function shellStatus(
  d: Schemas['StatusResponse'],
  now: Date,
  compactions: readonly Schemas['Compaction'][] = [],
): ShellStatus {
  const scan = d.scan;
  const last = scan.lastRunAt ?? d.generatedAt;
  const next = numOrNull(scan.nextRunInSeconds);
  const span = numOrNull(scan.intervalSeconds) || DEFAULT_SCAN_SECONDS;
  const entries = (drift: readonly string[]) => [
    `${clock(last)} ${SCAN_LOG[scan.state]}`,
    `${int(num(d.liveSessions))} LIVE ${num(d.liveSessions) === 1 ? 'SESSION' : 'SESSIONS'}`,
    ...(d.database.historyPaused ? ['HISTORY PAUSED'] : []),
    ...(d.machine.state === 'failed' || d.docker.state === 'failed' ? ['COLLECTOR FAILED'] : []),
    ...compactions
      .filter((c) => localDay(new Date(c.at)) === localDay(now))
      .slice(0, LOG_COMPACTIONS)
      .map(compactionLog),
    ...drift,
    `${int(num(scan.toolErrors))} TOOL ${num(scan.toolErrors) === 1 ? 'ERROR' : 'ERRORS'} TODAY`,
  ];
  const drifting = driftLog(scan.byClient, false);
  let log = entries(drifting);
  for (let shown = drifting.length - 1; log.join(LOG_SEPARATOR).length > LOG_FIT_CHARS && shown >= 0; shown -= 1) {
    const rest = drifting.length - shown;
    log = entries([...drifting.slice(0, shown), shown > 0 ? `+${rest} MORE` : `${rest} CLIENTS WITH DRIFT`]);
  }
  return {
    updated: `${since(last, now) || '0 s'} ago`,
    health: HEALTH[d.health],
    live: scan.state !== 'never_ran' && scan.state !== 'failed',
    log,
    logSpoken: entries(driftLog(scan.byClient, true)),
    nextScan: next === null ? null : `NEXT SCAN ${Math.round(next)} s`,
    progress: next === null ? 0 : clamp01(1 - next / span),
  };
}
