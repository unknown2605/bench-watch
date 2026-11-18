import { useNavigate, useSearch } from '@tanstack/react-router';
import type { SessionSearch } from '../../app/router';
import { useMemo } from 'react';
import type { Schemas } from '../../api';
import { useSessionDetail, useTranscript } from '../../app/queries';
import { SessionsPiece } from '../../charts/pieces';
import { SESSION_SLOTS, type SessionsInput } from '../../charts/pieces/sessions';
import { projectShort } from '../../charts/pieces/geometry';
import { SessionsPieceHours } from '../../charts/pieces/SessionsPieceHours';
import { ListRow, Meter, Panel } from '../../components';
import { AnimatedNumber } from '../../motion/AnimatedNumber';
import * as f from '../../format/format';
import { agentLabel, modelLabel, num, projectLabel, rows as listOf, sessionLabel } from '../../format/labels';
import { fraction, share } from '../../format/scale';
import { Fit } from '../../components/Fit';
import type_ from '../../styles/type.module.css';
import { Divider, Heading, MixBar, StatValue, type MixPart } from './parts';
import { errorText, TabFrame, TabPiece } from './TabFrame';
import tabs from './tabs.module.css';
import styles from './views.module.css';
import own from './Sessions.module.css';
import { keyed } from '../../app/keys';
import { NOT_FOR_CLIENT } from '../../format/clients';

type Data = Schemas['SessionsResponse'];
type Detail = Schemas['SessionDetailResponse'];

const MAX_PLANETS = 24;
const PROJECT_ROWS = 5;
const LEGEND = [
  ['dotSession', 'dot = session · number = rank'],
  ['dotLive', 'bright = live · dim = ended'],
  ['dotProject', 'colour = project · size = tokens'],
] as const;

export function sessionsView(d: Data, now: Date) {
  const projects = [...d.projects].sort((a, b) => num(b.tokens) - num(a.tokens));
  const sessions = [...d.sessions]
    .sort((a, b) => Number(b.state === 'live') - Number(a.state === 'live') || num(b.tokens) - num(a.tokens))
    .slice(0, MAX_PLANETS)
    .map((s) => ({
      key: `${projectLabel(s.project)}/${sessionLabel(s.session)}`,
      project: projectLabel(s.project),
      session: sessionLabel(s.session),
      live: s.state === 'live',
      tokens: num(s.tokens),
      runs: num(s.subagentRuns),
      compactions: num(s.compactions),
      startedAt: s.startedAt,
      lastActivityAt: s.lastActivityAt,
    }));
  const shown = new Set(
    [...d.sessions]
      .sort((a, b) => num(b.tokens) - num(a.tokens))
      .slice(0, SESSION_SLOTS)
      .map((x) => `${projectLabel(x.project)}/${sessionLabel(x.session)}`),
  );
  // The panel's default must be a mark the piece draws, so it skips sessions ranked beyond the piece's slots.
  const defaultKey = (sessions.find((s) => shown.has(s.key)) ?? sessions[0])?.key ?? null;
  const end = now.getTime();
  const dayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const recent = listOf(d.compactions?.recent).filter((c) => Date.parse(c.at) >= dayStart);
  const hours = {
    spans: listOf(d.sessions).map((s) => ({
      project: projectLabel(s.project),
      start: Date.parse(s.startedAt),
      end: s.state === 'live' ? end : Date.parse(s.lastActivityAt),
    })),
    marks: recent.map((c) => Date.parse(c.at)),
    projects: projects.map((p) => projectLabel(p.project)),
    dayStart,
    now: end,
  };
  const totalTokens = projects.reduce((s, p) => s + num(p.tokens), 0);
  return {
    empty: num(d.totals.sessions) === 0,
    claudeOnly: d.claudeOnly?.available !== false,
    totals: {
      sessions: num(d.totals.sessions),
      live: num(d.totals.live),
      runs: num(d.totals.subagentRuns),
      compactions: num(d.totals.compactions),
    },
    projects: projects.slice(0, PROJECT_ROWS).map((p) => ({
      name: projectLabel(p.project),
      short: projectShort(projectLabel(p.project)),
      meta: f.counted(num(p.sessions), 'session'),
      value: f.pct(num(p.share)),
      tokens: f.tokens(num(p.tokens)),
      fill: share(num(p.tokens), totalTokens),
    })),
    piece: {
      sessions: listOf(d.sessions).map((x) => ({
        key: `${projectLabel(x.project)}/${sessionLabel(x.session)}`,
        project: projectLabel(x.project),
        tokens: num(x.tokens),
        state: x.state === 'live' ? 'live' : x.state === 'idle' ? 'idle' : 'ended',
      })),
      projects: listOf(d.projects).map((p) => ({
        project: projectLabel(p.project),
        sessions: num(p.sessions),
        tokens: num(p.tokens),
      })),
      total: num(d.totals.sessions),
      live: num(d.totals.live),
    } satisfies SessionsInput,
    sessions,
    defaultKey,
    targets: new Map(listOf(d.sessions).map((x) => {
      const t = { project: projectLabel(x.project), session: sessionLabel(x.session) };
      return [`${t.project}/${t.session}`, t];
    })),
    hours,
    active: hours.spans.some((h) => h.end >= dayStart),
    recent: recent.slice(0, 3).map((c, i) => ({
      key: `${c.at}-${i}`,
      cells: [
        f.clock(c.at),
        projectShort(projectLabel(c.project)),
        `${f.tokens(num(c.tokensBefore))} → ${c.tokensAfter === null ? '?' : f.tokens(num(c.tokensAfter))}`,
      ] as const,
    })),
    recentCount: recent.length,
  };
}

export function detailView(d: Detail, now: Date) {
  const s = d.summary;
  const ctx = num(d.contextTokens);
  const limit = num(s.contextLimitTokens);
  const mix: MixPart[] = [
    { name: 'Read', value: num(d.tokenMix.cacheRead), tone: 'gold' },
    { name: 'Write', value: num(d.tokenMix.cacheWrite), tone: 'session' },
    { name: 'In', value: num(d.tokenMix.input), tone: 'dim' },
    { name: 'Out', value: num(d.tokenMix.output), tone: 'warn' },
  ];
  const last = [...listOf(d.compactions)].sort((a, b) => b.at.localeCompare(a.at))[0];
  const runMax = Math.max(0, ...d.subagents.map((a) => num(a.tokens)));
  return {
    project: projectLabel(s.project),
    live: s.state === 'live',
    meta: `${f.modelShort(modelLabel(s.model))} · started ${f.since(s.startedAt, now)} ago`,
    tokens: f.tokens(num(s.tokens)),
    context: f.tokens(ctx),
    runs: f.int(num(s.subagentRuns)),
    claudeOnly: d.claudeOnly?.available !== false,
    fill: limit > 0 ? fraction(ctx, limit) : null,
    contextLine: f.withLimit(ctx, limit),
    contextLabel: `Context ${f.withLimit(ctx, limit)}`,
    agents: [...d.subagents]
      .sort((a, b) => num(b.tokens) - num(a.tokens))
      .slice(0, 2)
      .map((a) => ({
        name: agentLabel(a.agentType),
        meta: f.counted(num(a.runs), 'run'),
        value: f.tokens(num(a.tokens)),
        fill: fraction(num(a.tokens), runMax),
      })),
    compaction: last
      ? `${f.whenLabel(last.at, now)} · ${f.tokens(num(last.tokensBefore))} → ` +
        (last.tokensAfter === null ? '?' : f.tokens(num(last.tokensAfter)))
      : null,
    mix,
  };
}

function SelectedSession({ target }: { target: { session: string; project: string } | null }) {
  const q = useSessionDetail(target);
  const v = useMemo(() => (q.data ? detailView(q.data, new Date()) : null), [q.data]);
  if (!target) return <p className={`${type_.mono} ${tabs.none}`}>Select a session on the map or in its list</p>;
  if (!v) {
    return (
      <p className={`${type_.mono} ${tabs.none}`} role={q.error ? 'alert' : 'status'}>
        {q.error ? errorText(q.error) : 'Loading'}
      </p>
    );
  }
  return (
    <>
      <p className={`${type_.tab} ${styles.name}`}>
        <span className={styles.nameText} title={v.project}>{v.project}</span>
        {v.live ? <span className={`${type_.monoS} ${own.liveBlock}`}>LIVE</span> : null}
      </p>
      <p className={`${type_.monoS} ${tabs.muted}`}>{v.meta}</p>
      <dl className={styles.stats}>
        <div><dt className={type_.monoS}>tokens</dt><dd><StatValue text={String(v.tokens)} /></dd></div>
        <div><dt className={type_.monoS}>context</dt><dd><StatValue text={String(v.context)} /></dd></div>
        <div>
          <dt className={type_.monoS}>sub-agents</dt>
          <dd><StatValue text={v.claudeOnly ? String(v.runs) : '—'} /></dd>
        </div>
      </dl>
      <div className={own.meterBlock}>
        {v.fill === null ? null : (
          <Meter value={v.fill} tone={v.fill >= 0.8 ? 'warn' : 'gold'} label={v.contextLabel} />
        )}
        <span className={`${type_.monoS} ${(v.fill ?? 0) >= 0.8 ? styles.meterWarn : own.meterLabel}`}>
          {v.contextLabel}
        </span>
      </div>
      {v.claudeOnly && v.agents.length ? (
        <>
          <Heading>Sub-agents</Heading>
          <div className={styles.rowsTight}>
            {keyed(v.agents, (a) => a.name).map(([key, a]) => (
              <ListRow key={key} name={a.name} meta={a.meta} value={a.value} fill={a.fill} />
            ))}
          </div>
        </>
      ) : null}
      <Divider />
      <p className={`${type_.monoS} ${v.compaction ? tabs.footWarn : tabs.foot}`}>
        {!v.claudeOnly ? `Sub-agents and compactions: ${NOT_FOR_CLIENT.toLowerCase()}`
          : v.compaction ? `Last compaction ${v.compaction}` : 'No compaction'}
      </p>
      <Divider />
      <MixBar parts={v.mix} label="Session token mix" height={8} />
    </>
  );
}

export function Sessions() {
  const q = useTranscript('/api/sessions', 'sessions');
  const v = useMemo(() => (q.data ? sessionsView(q.data, new Date()) : null), [q.data]);
  const search: SessionSearch = useSearch({ from: '/sessions' });
  const linked = search.session && search.project ? { session: search.session, project: search.project } : null;
  const picked = linked ? `${linked.project}/${linked.session}` : null;
  const navigate = useNavigate();
  const choose = (key: string) => {
    const hit = v?.targets.get(key);
    if (hit) void navigate({ to: '/sessions', search: hit, replace: true });
  };
  const period = q.data ? { from: q.data.period.from, to: q.data.period.to, partial: q.data.period.partial } : null;
  const fallback = linked ? null : (v?.sessions.find((s) => s.key === v.defaultKey) ?? null);
  const current = v?.sessions.find((s) => s.key === picked) ?? fallback;
  const target = linked ?? (current ? { session: current.session, project: current.project } : null);
  const slots = v && {
    left: (
      <>
        <Panel index="01" title="Projects">
          <ol className={own.projects}>
            {keyed(v.projects, (p) => p.name).map(([key, p], i) => (
              <li key={key} className={own.project}>
                <span className={`${type_.monoS} ${own.rank}`}>{String(i + 1).padStart(2, '0')}</span>
                <span className={`${type_.body} ${own.projectName}`} title={p.name}>{p.short}</span>
                <span className={`${type_.monoS} ${own.projectMeta}`} title={`${p.tokens} tokens`}>{p.meta}</span>
                <span className={`${type_.tab} ${own.projectShare}`}>
                  <AnimatedNumber text={p.value} scope={p.name} />
                </span>
                <Meter className={own.projectBar} value={p.fill} tone="gold" label={`${p.name} share of tokens`} />
              </li>
            ))}
          </ol>
        </Panel>
        <Panel index="02" title="Map · totals" className={styles.short}>
          <dl className={own.totals}>
            {([
              ['sessions', v.totals.sessions, '', false],
              ['live now', v.totals.live, own.liveLabel, false],
              ['sub-agents', v.totals.runs, '', true],
              ['compactions', v.totals.compactions, own.warnLabel, true],
            ] as const).map(([name, value, tone, onlyClaude]) => (
              <div key={name}>
                <dt className={`${type_.monoS} ${tone}`}>{name}</dt>
                <dd><StatValue text={onlyClaude && !v.claudeOnly ? '—' : f.int(value)} /></dd>
              </div>
            ))}
          </dl>
          {v.claudeOnly ? null : (
            <p className={`${type_.monoS} ${tabs.foot}`}>Sub-agents and compactions: {NOT_FOR_CLIENT.toLowerCase()}</p>
          )}
        </Panel>
        <Panel index="03" title="Legend">
          <ul className={own.legend}>
            {LEGEND.map(([dot, text]) => (
              <li key={text} className={type_.monoS}><span className={own[dot]} aria-hidden="true" />{text}</li>
            ))}
          </ul>
        </Panel>
      </>
    ),
    centre: (
      <TabPiece tab="sessions" placeholder={q.isPlaceholderData}>
        <SessionsPiece data={v.piece} selected={picked ?? current?.key ?? null} onSelect={choose} />
      </TabPiece>
    ),
    bottom: (
      <Panel index="04" title="Active sessions per hour · today" className={styles.bottomPanel}>
        {v.active ? (
          <Fit fallback={[644, 104]} height={104}>
            {(w) => <SessionsPieceHours {...v.hours} width={w} height={88} label="Active sessions per hour today" />}
          </Fit>
        ) : (
          <p className={`${type_.mono} ${tabs.none}`}>No activity today</p>
        )}
      </Panel>
    ),
    right: (
      <>
        <Panel index="05" title="Selected · session" className={styles.sessionTop}>
          <SelectedSession target={target} />
        </Panel>
        <Panel
          index="06"
          title={v.claudeOnly ? `Compactions · today · ${f.int(v.recentCount)}` : 'Compactions · today'}
          tone="warn"
          className={styles.rightLow}
        >
          {!v.claudeOnly ? <p className={`${type_.mono} ${tabs.none}`}>{NOT_FOR_CLIENT}</p>
            : v.recent.length ? v.recent.map((c) => (
            <div key={c.key} className={own.compaction}>
              <span className={`${type_.monoS} ${own.when}`}>{c.cells[0]}</span>
              <span className={`${type_.body} ${own.compactionName}`} title={c.cells[1]}>{c.cells[1]}</span>
              <span className={`${type_.mono} ${own.drop}`}>{c.cells[2]}</span>
            </div>
          )) : <p className={`${type_.mono} ${tabs.none}`}>No compactions</p>}
        </Panel>
      </>
    ),
  };
  return (
    <TabFrame
      tab="sessions"
      title="Sessions"
      period={period}
      slots={slots}
      empty={v?.empty ?? false}
      error={q.error}
      fetching={q.isFetching} placeholder={q.isPlaceholderData}
      emptyText="No sessions in this period"
    />
  );
}
