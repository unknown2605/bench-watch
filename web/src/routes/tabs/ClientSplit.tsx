import type { Schemas } from '../../api';
import { useFilters } from '../../app/filters';
import { useMeta } from '../../app/queries';
import type { TabId } from '../../app/tabs';
import { Meter, Panel } from '../../components';
import { clientLabel, clientName } from '../../format/clients';
import * as f from '../../format/format';
import { modelLabel, num } from '../../format/labels';
import { fraction, maxOf } from '../../format/scale';
import { AnimatedNumber } from '../../motion/AnimatedNumber';
import type_ from '../../styles/type.module.css';
import tabs from './tabs.module.css';
import own from './ClientSplit.module.css';

const NOTE_MODELS = 3;

type Props = { tab: TabId; index: string; split: readonly Schemas['ClientCost'][]; className?: string | undefined };

/** Tokens and cost per client; shown only while no client filter is set and more than one client has usage. */
export function ClientSplit({ tab, index, split, className }: Props) {
  const { client } = useFilters(tab);
  const meta = useMeta();
  if (client !== null || split.length < 2) return null;
  // Only a pending request defers the label; a failed one leaves every client unverified.
  const verified = new Map((meta.data?.clients ?? []).map((c) => [c.id, c.verified]));
  const settled = !meta.isPending;
  const top = maxOf(split, (c) => num(c.tokens));
  return (
    <Panel index={index} title="By client" className={className}>
      <div className={own.rows}>
        {split.map((c) => {
          const name = clientLabel({ id: c.client, verified: !settled || verified.get(c.client) === true });
          return (
            <div key={c.client} className={own.row}>
              <span className={`${type_.body} ${own.name}`} title={name}>{name}</span>
              <span className={`${type_.mono} ${own.meta}`}>{`${f.tokens(num(c.tokens))} tok · ${f.pct(num(c.share))}`}</span>
              <span className={`${type_.mono} ${own.value}`}>
                <AnimatedNumber text={f.usd(num(c.costUsd))} scope={name} />
              </span>
              <Meter className={own.meter} value={fraction(num(c.tokens), top)} label={`${name} fill`} />
            </div>
          );
        })}
      </div>
    </Panel>
  );
}

/** The cost figures leave out calls with no price; this names them, and renders nothing when all are priced. */
export function UnpricedNote({ unpriced, className }: {
  unpriced: readonly Schemas['UnpricedModel'][];
  className?: string | undefined;
}) {
  if (unpriced.length === 0) return null;
  const named = unpriced.slice(0, NOTE_MODELS).map((u) => `${clientName(u.client)} ${modelLabel(u.model)}`);
  const more = unpriced.length - named.length;
  const text = `Cost excludes unpriced models: ${named.join(', ')}${more > 0 ? ` +${more} more` : ''}`;
  return <p className={`${type_.monoS} ${tabs.footWarn} ${own.note} ${className ?? ''}`} title={text}>{text}</p>;
}
