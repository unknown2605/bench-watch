import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { Schemas } from '../../api';
import { Bars } from '../../charts/Bars';
import { fitName } from '../../charts/Honeycomb';
import { overviewModel } from '../../charts/pieces/overview';
import { contextView } from './Context';
import { dockerView } from './Docker';
import { ClippedName, LineRow } from './parts';
import { fillsView } from './Fills';
import { guardView } from './Guard';
import { machineView } from './Machine';
import { overviewView } from './Overview';
import { detailView, sessionsView } from './Sessions';
import { subagentsView } from './SubAgents';
import { fixtures } from './testData';

const NOW = new Date(2026, 10, 7, 12, 0, 0);
const data = fixtures(NOW.getTime());

describe('14-day trends', () => {
  const span = (from: string, days: number) => {
    return { ...data.overview, period: { ...data.overview.period, from, to: '2026-11-07', days } };
  };
  const fourteen = span('2026-10-25', 14);
  const todayOnly = span('2026-11-07', 1);

  it('keeps 14 points on the KPI sparklines and the piece trend when the Today range is selected', () => {
    const v = overviewView(todayOnly, NOW, fourteen);
    expect(v.days).toBe(1);
    expect(v.cost.trend).toHaveLength(14);
    expect(v.calls.trend).toHaveLength(14);
    expect(v.piece.trend).toHaveLength(14);
    expect(overviewModel(v.piece).trend).toHaveLength(14);
    expect(v.piece.rangeLabel).toBe('today');
  });

  it('marks the last bar as today and keeps the other bars plain', () => {
    const html = renderToStaticMarkup(
      <Bars
        data={[1, 2, 3].map((v, i) => ({ key: `d${i}`, label: `D${i}`, value: v, display: String(v) }))}
        width={90}
        height={40}
        today
        label="x"
      />,
    );
    expect(html).not.toContain('--sh-ink-warm');
    expect(html.match(/fill="currentColor"/g)).toHaveLength(1);
    expect(html).toContain('color="var(--sh-accent-gold)"');
    expect(html).toContain('D2 (today)');
  });
});

describe('history figures', () => {
  it('draws the re-read share of every day of the period on the Overview and Context KPIs', () => {
    const overview = overviewView(data.overview, NOW);
    expect(overview.reread.trend).toEqual([0, 0.9, 0, 0, 0.6, 0, 0]);
    expect(contextView(data.context, NOW).reread.trend).toEqual([0, 0, 0, 0.95, 0, 0, 0]);
  });

  it('feeds the Overview piece the response figures and the main plus sub tokens of every trend day', () => {
    const v = overviewView(data.overview, NOW);
    const calls = data.overview.bands.map((b) => ({ over: b.band.minTokens >= 200_000, n: b.mainCalls + b.subCalls }));
    const over = calls.filter((c) => c.over).reduce((a, c) => a + c.n, 0) / calls.reduce((a, c) => a + c.n, 0);
    expect(v.piece).toMatchObject({ tokens: 1_284_000, previous: 1_000_000, days: 7, mainShare: 0.7, sub: 0.3, reread: 0.96 });
    expect(v.piece.over).toBeCloseTo(over);
    expect(v.piece.today).toBe(data.overview.period.to);
    const day = (d: string) => v.piece.trend.find((r) => r.day === d)?.tokens;
    expect([day('2026-11-02'), day('2026-11-05'), day('2026-11-07')]).toEqual([700_000, 584_000, 0]);
  });

  it('draws the average context per call of every day on the Context KPI', () => {
    expect(contextView(data.context, NOW).avg.trend).toEqual([0, 0, 0, 321_000, 0, 0, 0]);
  });

  it('keeps the Overview, Context and Sub-agents views alive when an older server lacks a newer field', () => {
    const overview = { ...data.overview, compactions: undefined };
    const subagents = { ...data.subagents, scopeDaily: undefined };
    const context = { ...data.context, compactions: undefined };
    expect(() => overviewView(overview as unknown as Schemas['OverviewResponse'], NOW)).not.toThrow();
    expect(() => subagentsView(subagents as unknown as Schemas['SubagentsResponse'])).not.toThrow();
    expect(() => contextView(context as unknown as Schemas['ContextResponse'], NOW)).not.toThrow();
  });

  it('gives the sub-agent share and cost KPIs one point per day, zero where the API has no row', () => {
    const v = subagentsView(data.subagents);
    expect(v.scope.trend).toEqual([0, 0, 0, 0, 0, 0.3, 0]);
    expect(v.cost.trend).toEqual([0, 0, 0, 0, 0, 1.5, 0]);
  });
});

describe('Sessions views', () => {
  it('tolerate an older server without the compactions fields', () => {
    const list = { ...data.sessions, compactions: undefined };
    const detail = { ...data.detail, compactions: undefined };
    expect(() => sessionsView(list as unknown as Schemas['SessionsResponse'], NOW)).not.toThrow();
    expect(() => detailView(detail as unknown as Schemas['SessionDetailResponse'], NOW)).not.toThrow();
  });
});

describe('sub-agent type keys', () => {
  const base = data.subagents.types[0]!;
  const mk = (agentType: string, tokens: number) => ({ ...base, agentType, tokens });

  it('keep a type under the same key when the ranking changes', () => {
    const a = subagentsView({ ...data.subagents, types: [mk('alpha', 9), mk('beta', 5)] });
    const b = subagentsView({ ...data.subagents, types: [mk('alpha', 4), mk('beta', 8)] });
    const keyOf = (v: typeof a, name: string) => v.types.find((t) => t.name === name)?.key;
    expect(keyOf(a, 'alpha')).toBe(keyOf(b, 'alpha'));
    expect(keyOf(a, 'beta')).toBe(keyOf(b, 'beta'));
  });

  it('stay unique when two raw types share a label', () => {
    const v = subagentsView({ ...data.subagents, types: [mk('a b', 9), mk('c d', 5), mk('e', 1)] });
    expect(new Set(v.types.map((t) => t.key)).size).toBe(3);
  });
});

describe('Docker name cell', () => {
  it('carries the whole container name in the title and clips the text with CSS', () => {
    const long = 'a'.repeat(58);
    const html = renderToStaticMarkup(<ClippedName name={long} full={long} />);
    expect(html).toContain(`title="${long}"`);
    expect(html).toContain('class="');
  });

  it('gives a compaction line the whole project name as its title', () => {
    const long = 'www-self-projects-'.concat('y'.repeat(30));
    const html = renderToStaticMarkup(<LineRow cells={['10:00', long, '1k → 2k']} />);
    expect(html).toContain(`title="${long}"`);
    expect(html).toContain(`>${long}<`);
  });

  it('gives every table row its full name', () => {
    expect(dockerView(data.docker, NOW.getTime()).table.every((r) => r.full.length > 0)).toBe(true);
  });
});

describe('guard bars', () => {
  it('span every day of the period and label both ends', () => {
    const v = guardView(data.guard, new Date(2026, 10, 9, 12));
    expect(v.bars).toHaveLength(7);
    expect(v.barAxis).toEqual({ start: '01 Nov', end: '07 Nov', now: false });
  });

  it('says TODAY when the period ends today', () => {
    const today = { ...data.guard, period: { ...data.guard.period, to: '2026-11-07' } };
    const v = guardView(today, new Date(2026, 10, 7, 9));
    expect(v.barAxis).toMatchObject({ end: 'Today', now: true });
  });
});

describe('Bars axis', () => {
  const bars = [{ key: 'a', label: 'a', value: 0, display: '0' }, { key: 'b', label: 'b', value: 5, display: '5' }];

  it('prints the ends and the note, and keeps a stub for an empty day', () => {
    const html = renderToStaticMarkup(<Bars data={bars} width={100} height={40} axis={{ start: '09 Oct', end: 'Today', note: 'x = y', now: true }} label="L" />);
    expect(html).toContain('09 OCT');
    expect(html).toContain('X = Y');
    expect(html).toContain('TODAY');
    expect(html.match(/<rect/g)).toHaveLength(2);
  });

  it('adds nothing without an axis', () => {
    expect(renderToStaticMarkup(<Bars data={bars} width={100} height={40} label="L" />)).not.toContain('09 OCT');
  });
});

describe('machine boot volume', () => {
  it('leads the disks and feeds the Disk free card, whatever its size', () => {
    const m = structuredClone(data.machine);
    const volume = (name: string, kind: Schemas['VolumeKind'] | null, total: number) =>
      ({ volume: name, kind, filesystem: null, totalBytes: total, freeBytes: total / 2, mergedNames: 0 });
    if (m.now) m.now.volumes = [volume('My Passport', null, 2_000_000_000_000), volume('Macintosh HD', 'internal', 500_000_000_000)];
    const v = machineView(m);
    expect(v.disk.delta).toContain('Macintosh HD');
    expect(v.volumes.map((d) => d.name)).toEqual(['Macintosh HD', 'My Passport']);
  });

  it('falls back to the volume named root when no volume says it is internal', () => {
    const m = structuredClone(data.machine);
    if (m.now) {
      m.now.volumes = [
        { volume: 'My Passport', kind: null, filesystem: null, totalBytes: 2_000_000_000_000, freeBytes: 1, mergedNames: 0 },
        { volume: 'root', kind: null, filesystem: null, totalBytes: 500_000_000_000, freeBytes: 1, mergedNames: 0 },
      ];
    }
    expect(machineView(m).volumes[0]?.name).toBe('root');
  });
});

describe('docker hexagons', () => {
  it('keep the whole container name', () => {
    const d = structuredClone(data.docker);
    if (d.now) d.now.containers = d.now.containers.map((c) => ({ ...c, container: 'redis_sentinel_service' }));
    const names = dockerView(d, NOW.getTime()).piece.containers.map((c) => c.name);
    expect(names.length).toBeGreaterThan(0);
    for (const name of names) expect(name).toBe('redis_sentinel_service');
  });

  it('fit a long name on one line, on two lines at a separator, or in a smaller font', () => {
    expect(fitName('postgresql', 64)).toEqual({ lines: ['postgresql'], size: 11 });
    expect(fitName('redis_sentinel', 63)).toEqual({ lines: ['redis_', 'sentinel'], size: 11 });
    const squeezed = fitName('averyveryverylongnamewithoutseparators', 63);
    expect(squeezed.lines).toHaveLength(3);
    expect(squeezed.size).toBe(8.4);
  });

  it('never lets a long container name run wider than its hexagon', () => {
    for (const name of ['infrastructure_services_postgres_primary_1', 'x'.repeat(80), 'a-b-c-d-e-f-g-h-i-j-k-l-m-n-o-p-q-r-s-t']) {
      const fit = fitName(name, 66);
      expect(fit.lines.length).toBeLessThanOrEqual(3);
      for (const line of fit.lines) expect(line.length * fit.size * 0.58).toBeLessThanOrEqual(66.01);
    }
    expect(fitName('x'.repeat(80), 66).lines[2]).toMatch(/…$/);
  });
});

describe('fills by project', () => {
  const many = {
    ...data.fills,
    byProject: [
      {
        project: 'a',
        weightedTokens: 1000,
        tools: [
          { tool: 'Bash: git', weightedTokens: 200 }, { tool: 'Bash: ls', weightedTokens: 160 }, { tool: 'Bash: go', weightedTokens: 100 },
          { tool: 'Read: whole', weightedTokens: 90 }, { tool: 'Grep', weightedTokens: 450 },
        ],
      },
      { project: 'b', weightedTokens: 50, tools: [{ tool: 'Read: ranged', weightedTokens: 50 }] },
    ],
  } as Schemas['FillsResponse'];

  it('merges the Bash commands into one family on the default view', () => {
    const a = fillsView(many, null).projects.find((p) => p.name === 'a');
    expect(a?.meta).toBe('Bash 46% · Grep 45% · Read 9%');
  });

  it('sums the exact weighted tokens of the picked tool for every project', () => {
    const v = fillsView(many, 'Read');
    expect(v.projects.map((p) => [p.name, p.weighted])).toEqual([['a', 90], ['b', 50]]);
  });

  it('names the picked tool in the selected panel', () => {
    expect(fillsView(data.fills, 'Grep').selected?.name).toBe('Grep');
  });

  it('shows a project without the new field instead of crashing', () => {
    const old = { ...many, byProject: [{ project: 'a', weightedTokens: 10 }] } as unknown as Schemas['FillsResponse'];
    expect(fillsView(old, null).projects).toHaveLength(1);
  });

  it('keeps the projects, with a dash for the meta, when a picked tool meets a server without tools[]', () => {
    const old = { ...many, byProject: [{ project: 'a', weightedTokens: 10 }] } as unknown as Schemas['FillsResponse'];
    const v = fillsView(old, 'Read');
    expect(v.projects.map((p) => [p.name, p.weighted, p.meta])).toEqual([['a', 10, '—']]);
  });
});
