import { useMemo } from 'react';
import type { Schemas } from '../../api';
import { useTranscript, useTrend } from '../../app/queries';
import { Bars, type BarDatum } from '../../charts/Bars';
import { TokensPiece } from '../../charts/pieces';
import type { TokensInput } from '../../charts/pieces/tokens';
import { TOKEN_TONE } from '../../charts/pieces/TokensPiece';
import type { TokenTone } from '../../charts/pieces/tokens';
import { KPI, ListRow, Meter, Panel } from '../../components';
import { clientName } from '../../format/clients';
import * as f from '../../format/format';
import { modelLabel, num, numOrNull, rows } from '../../format/labels';
import { fillDays, fraction, maxOf, share } from '../../format/scale';
import { Fit } from '../../components/Fit';
import type_ from '../../styles/type.module.css';
import { ClientSplit, UnpricedNote } from './ClientSplit';
import { pieceRange } from './parts';
import { TabFrame, TabPiece } from './TabFrame';
import tabs from './tabs.module.css';
import styles from './views.module.css';
import own from './Tokens.module.css';

type Data = Schemas['TokensResponse'];
type Kind = Schemas['TokenKind'];

export const KINDS: Record<Kind, { name: string; tone: TokenTone }> = {
  cacheRead: { name: 'cache read', tone: 'gold' },
  cacheWrite: { name: 'cache write', tone: 'session' },
  input: { name: 'input', tone: 'live' },
  output: { name: 'output', tone: 'warm' },
};
const MODEL_ROWS = 3;
const DAY_ROWS = 4;
const pairPct = (s: number) => (s > 0 && s < 0.001 ? '<0.1%' : s < 0.1 ? f.pct1(s) : f.pct(s));

export function tokensView(d: Data, now: Date, trend: Data = d) {
  const days = num(d.period.days);
  const kinds = (Object.keys(KINDS) as Kind[]).flatMap((kind) => {
    const row = d.byKind.find((k) => k.kind === kind);
    return row ? [{ ...KINDS[kind], tokens: num(row.tokens), cost: num(row.costUsd) }] : [];
  });
  const totalTokens = kinds.reduce((s, k) => s + k.tokens, 0);
  const totalCost = kinds.reduce((s, k) => s + k.cost, 0);
  const pairs = kinds.map((k) => ({ ...k, tokenShare: share(k.tokens, totalTokens),
    costShare: share(k.cost, totalCost) }));
  const costly = pairs.reduce<(typeof pairs)[number] | null>(
    (best, k) => (k.tokenShare > 0 && (!best || k.costShare / k.tokenShare > best.costShare / best.tokenShare) ? k
      : best),
    null,
  );
  const emptyDay = (day: string) => ({ day, tokens: 0, costUsd: 0, topModel: null });
  const daily = fillDays(trend.period.from, trend.period.to, trend.daily, emptyDay);
  const costs = daily.map((r) => num(r.costUsd));
  const last = costs.length - 1;
  const bars: BarDatum[] = daily.map((r, i) => ({ key: r.day, label: f.dayLabel(r.day), value: costs[i] ?? 0,
    display: f.usd(costs[i] ?? 0) }));
  const avg = num(d.avgCostPerDayUsd);
  const barAvg = num(trend.avgCostPerDayUsd);
  const models = [...d.byModel].sort((a, b) => num(b.costUsd) - num(a.costUsd)).slice(0, 3);
  const modelMax = maxOf(models, (m) => num(m.costUsd));
  const peakDay = d.peakCostDay;
  return {
    empty: num(d.tokens.value) === 0 && num(d.costUsd.value) === 0,
    days,
    byClient: rows(d.byClient),
    unpriced: rows(d.unpriced),
    tokens: {
      value: f.tokens(num(d.tokens.value)),
      delta: f.deltaPct(num(d.tokens.value), numOrNull(d.tokens.previous), days),
      trend: daily.map((r) => num(r.tokens)),
    },
    cost: { value: f.usd(num(d.costUsd.value)), delta: f.deltaPct(num(d.costUsd.value), numOrNull(d.costUsd.previous),
      days), trend: costs },
    avg: {
      value: f.usd(avg),
      delta: peakDay ? `peak ${f.usd(num(peakDay.costUsd))} on ${f.dayLabel(peakDay.day)}` : '',
    },
    piece: {
      kinds: rows(d.byKind).map((k) => ({ kind: k.kind, tokens: num(k.tokens), costUsd: num(k.costUsd) })),
      tokens: num(d.tokens.value),
      costUsd: num(d.costUsd.value),
      previousCost: numOrNull(d.costUsd.previous),
      days,
      rangeLabel: pieceRange(days),
    } satisfies TokensInput,
    pairs,
    costly: costly && costly.costShare > costly.tokenShare
      ? {
        text: `${costly.name}: ${f.pct(costly.tokenShare)} tok, ${f.pct(costly.costShare)} cost`,
        title: `${costly.name}: ${f.pct(costly.tokenShare)} of tokens, ${f.pct(costly.costShare)} of cost`,
      }
      : null,
    recorded: recordedNote(d.recorded),
    topDays: d.topDays.slice(0, 4).map((t) => ({
      day: t.day,
      name: f.dayLabel(t.day),
      meta: `${f.tokens(num(t.tokens))} tokens · ${t.topModel ? f.modelShort(modelLabel(t.topModel)) : '—'}`,
      value: f.usd(num(t.costUsd)),
      fill: fraction(num(t.costUsd), maxOf(d.topDays, (x) => num(x.costUsd))),
    })),
    models: models.map((m) => ({
      name: modelLabel(m.model),
      meta: `${f.tokens(num(m.tokens))} tok · ${f.pct(num(m.share))}`,
      value: f.usd(num(m.costUsd)),
      fill: fraction(num(m.costUsd), modelMax),
    })),
    bars,
    axis: {
      start: `${daily.length} d ago`,
      end: trend.period.to === f.localDay(now) ? 'Today' : f.dayLabel(trend.period.to),
      now: trend.period.to === f.localDay(now),
    },
    average: barAvg > 0 ? { value: barAvg, label: `AVG ${f.usd(barAvg)}` } : null,
    peak: last >= 0 ? { index: last, label: `${lastLabel(trend.period.to, now)} · ${f.usd(costs[last] ?? 0)}` } : null,
  };
}

/** Clients that store their own cost are out of the split by kind; text fits one 264 px line, title is complete. */
function recordedNote(r: Data['recorded'] | undefined): { text: string; title: string } | null {
  const names = rows(r?.clients).map(clientName);
  if (!names.length) return null;
  const usd = f.usd(num(r?.costUsd));
  return {
    text: `${names.length === 1 ? names[0] : `${names.length} clients`}: ${usd} unsplit`,
    title: `${names.join(', ')}: ${usd} recorded, no split`,
  };
}

function lastLabel(day: string, now: Date) {
  return day === f.localDay(now) ? 'Today' : f.dayLabel(day);
}

type PairProps = { name: string; value: string; tone: TokenTone; tok: number; cost: number };

function TokPair({ name, value, tone, tok, cost }: PairProps) {
  return (
    <div className={own.pair}>
      <span className={own.square} style={{ background: TOKEN_TONE[tone] }} aria-hidden="true" />
      <span className={`${type_.body} ${own.pairName}`}>{name}</span>
      <span className={`${type_.numberS} ${own.pairValue}`}>{value}</span>
      {([['TOK', tok, own.tok], ['COST', cost, own.cost]] as const).map(([label, v, tint]) => (
        <div key={label} className={`${own.pairLine} ${tint}`}>
          <span className={`${type_.monoS} ${own.pairLabel}`}>{label}</span>
          <Meter
            className={`${own.pairMeter} ${tint}`}
            value={v > 0 ? Math.max(v, 0.01) : 0}
            label={`${name} ${label} share`}
          />
          <span className={`${type_.monoS} ${own.pairPct}`}>{pairPct(v)}</span>
        </div>
      ))}
    </div>
  );
}

function Placeholder() {
  return (
    <div className={own.placeholder} aria-hidden="true" />
  );
}

export function Tokens() {
  const q = useTranscript('/api/tokens', 'tokens');
  const t = useTrend('/api/tokens', 'tokens');
  const v = useMemo(() => (q.data ? tokensView(q.data, new Date(), t.data) : null), [q.data, t.data]);
  const period = q.data ? { from: q.data.period.from, to: q.data.period.to, partial: q.data.period.partial } : null;
  const slots = v && {
    left: (
      <>
        <KPI
          label={`Tokens · ${pieceRange(v.days)}`}
          value={v.tokens.value}
          delta={v.tokens.delta ?? ''}
          trend={v.tokens.trend}
          trendToday
          tone="gold"
        />
        <KPI
          label="Est. cost"
          value={v.cost.value}
          delta={v.cost.delta ?? ''}
          trend={v.cost.trend}
          trendToday
          tone="live"
        />
        <KPI label="Avg cost / day" value={v.avg.value} delta={v.avg.delta} tone="default" />
        <Panel index="04" title="Cost by model">
          <div className={styles.rows}>
            {v.models.map((m) => (
              <ListRow
                key={m.name}
                name={m.name}
                meta={m.meta}
                value={m.value}
                fill={m.fill}
                className={own.trackRow}
              />
            ))}
            {Array.from({ length: Math.max(0, MODEL_ROWS - v.models.length) }, (_, i) => <Placeholder key={`p${i}`} />)}
          </div>
          <p className={`${type_.monoS} ${tabs.foot}`}>Estimate at API list prices per million tokens</p>
          <UnpricedNote unpriced={v.unpriced} />
        </Panel>
      </>
    ),
    centre: (
      <TabPiece tab="tokens" placeholder={q.isPlaceholderData}>
        <TokensPiece data={v.piece} />
      </TabPiece>
    ),
    bottom: (
      <>
        <Panel index="03" title="Cost · last 14 d" className={styles.bottomPanel}>
          <Fit fallback={[644, 70]} height={70}>
            {(w) => (
              <Bars
                data={v.bars}
                width={w}
                height={70}
                tone="gold"
                today
                axis={v.axis}
                average={v.average}
                peak={v.peak}
                label="Estimated cost per day"
              />
            )}
          </Fit>
        </Panel>
        <ClientSplit tab="tokens" index="06" split={v.byClient} className={styles.bottomPanel} />
      </>
    ),
    right: (
      <>
        <Panel index="02" title="Tokens vs cost" className={styles.rightTop}>
          <div className={styles.pairs}>
            {v.pairs.map((k) => (
              <TokPair
                key={k.name}
                name={k.name}
                value={f.usd(k.cost)}
                tone={k.tone}
                tok={k.tokenShare}
                cost={k.costShare}
              />
            ))}
          </div>
          {([['costly', v.costly], ['recorded', v.recorded]] as const).map(([key, note]) => (note ? (
            <p key={key} className={`${type_.label} ${tabs.footGold}`} title={note.title}>{note.text}</p>
          ) : null))}
        </Panel>
        <Panel index="05" title="Top days · cost" className={styles.rightLow}>
          <div className={styles.rowsTight}>
            {v.topDays.map((t) => (
              <ListRow key={t.day} name={t.name} meta={t.meta} value={t.value} fill={t.fill} className={own.trackRow} />
            ))}
            {v.topDays.length
              ? Array.from({ length: DAY_ROWS - v.topDays.length }, (_, i) => <Placeholder key={`p${i}`} />)
              : null}
          </div>
          {v.topDays.length ? null : <p className={`${type_.mono} ${tabs.none}`}>No days yet</p>}
        </Panel>
      </>
    ),
  };
  return <TabFrame tab="tokens" title="Tokens" period={period} slots={slots} empty={v?.empty ?? false} error={q.error}
    fetching={q.isFetching} placeholder={q.isPlaceholderData} />;
}
