import { describe, expect, it } from 'vitest';
import type { Schemas } from '../api';
import { NO_DATA, shellStatus } from './status';

const base = {
  generatedAt: '2026-11-10T10:00:00Z',
  health: 'nominal',
  scan: { state: 'ok', lastRunAt: '2026-11-10T09:59:57Z', nextRunInSeconds: 4, toolErrors: 0 },
  machine: { state: 'ok' },
  docker: { state: 'ok' },
  guard: { state: 'ok' },
  database: { historyPaused: false },
  liveSessions: 3,
} as unknown as Schemas['StatusResponse'];

describe('shellStatus', () => {
  it('reports the data it has instead of NO DATA YET', () => {
    const s = shellStatus(base, new Date('2026-11-10T10:00:00Z'));
    expect(s.updated).not.toBe(NO_DATA.updated);
    expect(s.health).toBe('ALL NOMINAL');
    expect(s.log).toContain('3 LIVE SESSIONS');
    expect(s.nextScan).toBe('NEXT SCAN 4 s');
    expect(s.progress).toBeCloseTo(0.6);
  });

  it('runs the countdown over the interval the server reports', () => {
    const slow = { ...base, scan: { ...base.scan, nextRunInSeconds: 15, intervalSeconds: 30 } } as Schemas['StatusResponse'];
    expect(shellStatus(slow, new Date('2026-11-10T10:00:00Z')).progress).toBeCloseTo(0.5);
  });

  it('keeps the 10 s default when an older server sends no interval', () => {
    expect(shellStatus(base, new Date('2026-11-10T10:00:00Z')).progress).toBeCloseTo(0.6);
  });

  it('adds HISTORY PAUSED when the database is down', () => {
    const s = shellStatus({ ...base, database: { historyPaused: true } } as Schemas['StatusResponse'], new Date());
    expect(s.log).toContain('HISTORY PAUSED');
  });

  it('lists the two newest compactions of today before the tool-error count', () => {
    const at = (when: Date, before: number, after: number | null) =>
      ({ at: when.toISOString(), project: 'Docker', tokensBefore: before, tokensAfter: after }) as never;
    const now = new Date(2026, 10, 10, 12, 0);
    const old = at(new Date(2026, 10, 8, 8, 0), 250_000, 40_000);
    const rows = [
      at(new Date(2026, 10, 10, 11, 30), 271_000, 100_000),
      at(new Date(2026, 10, 10, 11, 0), 267_000, null),
      at(new Date(2026, 10, 10, 8, 0), 250_000, 40_000),
      old,
    ];
    const log = shellStatus(base, now, rows).log;
    const lines = log.filter((l) => l.includes('COMPACTION'));
    expect(lines).toHaveLength(2);
    expect(lines[0]).toMatch(/COMPACTION Docker 271k → 100k$/);
    expect(lines[1]).toMatch(/COMPACTION Docker 267k$/);
    expect(log.at(-1)).toBe('0 TOOL ERRORS TODAY');
    expect(shellStatus(base, now, [old]).log.some((l) => l.includes('COMPACTION'))).toBe(false);
  });

  it('lists skipped lines and unreadable files per client, only for clients with a count', () => {
    const byClient = [
      { client: 'claude-code', skippedLines: 0, unreadable: 0 },
      { client: 'codex', skippedLines: 1200, unreadable: 0 },
      { client: 'gemini-cli', skippedLines: 0, unreadable: 2 },
      { client: 'opencode', skippedLines: 3, unreadable: 1 },
    ];
    const s = shellStatus({ ...base, scan: { ...base.scan, byClient } } as Schemas['StatusResponse'], new Date());
    const drift = s.log.filter((l) => /SKIP |UNREAD /.test(l));
    expect(drift).toEqual(['CODEX SKIP 1,200', 'GEMINI CLI UNREAD 2', 'OPENCODE SKIP 3 UNREAD 1']);
    expect(s.logSpoken.filter((l) => /SKIPPED|UNREADABLE/.test(l)))
      .toEqual(['CODEX 1,200 SKIPPED LINES', 'GEMINI CLI 2 UNREADABLE', 'OPENCODE 3 SKIPPED LINES, 1 UNREADABLE']);
    expect(s.log.at(-1)).toBe('0 TOOL ERRORS TODAY');
    expect(shellStatus(base, new Date()).logSpoken.some((l) => /SKIPPED|UNREADABLE/.test(l))).toBe(false);
  });

  it('collapses the compact drift text when it would not fit, and keeps every client in the spoken line', () => {
    const ids = ['codex', 'gemini-cli', 'opencode', 'qwen-code', 'copilot-cli', 'claude-code'];
    const byClient = ids.map((client, i) => ({ client, skippedLines: 1200 + i, unreadable: 1 + (i % 2) }));
    const s = shellStatus({ ...base, scan: { ...base.scan, byClient } } as Schemas['StatusResponse'], new Date());
    expect(s.log.join('   ·   ').length).toBeLessThanOrEqual(170);
    expect(s.log.some((l) => /^\+\d+ MORE$/.test(l))).toBe(true);
    expect(s.log.at(-1)).toBe('0 TOOL ERRORS TODAY');
    expect(s.logSpoken.filter((l) => /SKIPPED/.test(l))).toHaveLength(6);
  });

  it.each([[1, '1 CLIENT WITH DRIFT'], [2, '2 CLIENTS WITH DRIFT']])(
    'words the collapsed drift of %i client(s) as "%s"', (n, want) => {
      const compactions = Array.from({ length: 2 }, (_, i) => ({
        at: new Date(2026, 10, 10, 11, i).toISOString(), project: 'a-project-with-quite-a-long-name',
        tokensBefore: 90_000, tokensAfter: 10_000,
      })) as unknown as Schemas['Compaction'][];
      const byClient = ['claude-code', 'copilot-cli'].slice(0, n)
        .map((client) => ({ client, skippedLines: 123_456_789_012_345, unreadable: 123_456_789_012_345 }));
      const d = { ...base, scan: { ...base.scan, byClient } } as Schemas['StatusResponse'];
      expect(shellStatus(d, new Date(2026, 10, 10, 12, 0), compactions).log).toContain(want);
    });

  it('shows the fallback name for a project the label gate rejects', () => {
    const odd = {
      at: new Date(2026, 10, 10, 11, 0).toISOString(),
      project: '/Users/me/secret repo',
      tokensBefore: 90_000,
      tokensAfter: null,
    } as unknown as Schemas['Compaction'];
    const log = shellStatus(base, new Date(2026, 10, 10, 12, 0), [odd]).log;
    expect(log.find((l) => l.includes('COMPACTION'))).toMatch(/COMPACTION other 90k$/);
    expect(log.join(' ')).not.toContain('secret');
  });
});
