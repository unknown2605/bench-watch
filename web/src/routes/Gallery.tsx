import { useState, type ReactNode } from 'react';
import { CoreRing, DiscTower, Honeycomb, LineArea, RingChart, Shield, StarMap, UPlotLine } from '../charts';
import {
  Badge, Chip, ContainerCell, KPI, ListRow, LogBar, Marker, Meter, NavTabs, Panel, Planet, Status, TopBar,
} from '../components';
import { navTabs } from '../app/tabs';
import {
  containers, coreLoad, discs, kpiTrends, liveSeries, logSegments, ringValues, stars, tokenFlow, tokenMarkers,
} from '../mock/data';
import { PieceGallery } from './gallery/PieceGallery';
import styles from './Gallery.module.css';

type FrameProps = { shot: string; title: string; gap?: number; pad?: number; wrap?: number; children: ReactNode };

function Frame({ shot, title, gap = 24, pad = 24, wrap, children }: FrameProps) {
  const style = { '--gap': `${gap}px`, '--pad': `${pad}px` } as React.CSSProperties;
  return (
    <section className={styles.block}>
      <h2 className={styles.title}>{title}</h2>
      <div className={wrap ? `${styles.frame} ${styles.grid}` : styles.frame} data-shot={shot} style={style}>
        {children}
      </div>
    </section>
  );
}

const series = liveSeries();

export default function Gallery() {
  const params = new URLSearchParams(window.location.search);
  if (params.get('view') === 'pieces') {
    return <PieceGallery only={params.get('piece')} demo={params.get('demo') === '1'} />;
  }
  return <Components />;
}

function Components() {
  const [chip, setChip] = useState(true);
  return (
    <main className={styles.gallery}>
      <h1 className={styles.heading}>Component gallery</h1>
      <a className={styles.title} href="?view=pieces">Centre pieces on recorded responses</a>
      <Frame shot="panel" title="Panel" gap={32} pad={32}>
        <Panel index="01" title="Panel title" />
        <Panel index="01" title="Panel title" tone="gold" />
        <Panel index="01" title="Panel title" tone="warn" />
        <Panel index="01" title="Panel title" tone="live" />
      </Frame>
      <Frame shot="kpi" title="KPI" gap={32} pad={32}>
        <KPI label="Tokens" value="1.28M" delta="▲ 12.4% vs prev 30 d" trend={kpiTrends.gold} tone="gold" />
        <KPI label="Tokens" value="1.28M" delta="▲ 12.4% vs prev 30 d" trend={kpiTrends.live} tone="live" />
        <KPI label="Tokens" value="1.28M" delta="▲ 12.4% vs prev 30 d" trend={kpiTrends.warn} tone="warn" />
        <KPI label="Tokens" value="1.28M" delta="▲ 12.4% vs prev 30 d" trend={kpiTrends.default} tone="default" />
      </Frame>
      <Frame shot="list-row" title="List row" gap={32} pad={32}>
        <ListRow name="project-alpha" meta="LIVE · opus · 42 m" value="128k / 200k" fill={0.6} />
        <ListRow name="project-alpha" meta="LIVE · opus · 42 m" value="128k / 200k" fill={0.6} tone="warn" />
      </Frame>
      <Frame shot="container-cell" title="Container cell" gap={32} pad={32}>
        <ContainerCell name="mariadb" value="412 MB" health="healthy" />
        <ContainerCell name="mariadb" value="412 MB" health="warn" />
        <ContainerCell name="mariadb" value="412 MB" health="down" />
      </Frame>
      <Frame shot="chip" title="Chip" gap={40}>
        <Chip isSelected={!chip} onChange={(v) => setChip(!v)}>30d</Chip>
        <Chip isSelected={chip} onChange={(v) => setChip(v)}>30d</Chip>
      </Frame>
      <Frame shot="marker" title="Marker">
        <Marker kind="compaction" label="Compaction" />
        <Marker kind="peak" label="Peak" />
        <Marker kind="event" label="Event" />
      </Frame>
      <Frame shot="planet" title="Planet" gap={48}>
        <Planet kind="idle" label="Idle" />
        <Planet kind="live" label="Live" />
        <Planet kind="selected" label="Selected" />
      </Frame>
      <Frame shot="badge" title="Badge">
        <Badge kind="live">LIVE</Badge>
        <Badge kind="warn">OVER</Badge>
        <Badge kind="gold">NEW</Badge>
      </Frame>
      <Frame shot="status" title="Status" gap={24}>
        <Status kind="live" />
        <Status kind="idle" />
        <Status kind="warn" />
      </Frame>
      <Frame shot="meter" title="Meter" wrap={2}>
        <Meter value={0.6} tone="gold" label="Gold" className={styles.meter} />
        <Meter value={0.6} tone="live" label="Live" className={styles.meter} />
        <Meter value={0.6} tone="warn" label="Warn" className={styles.meter} />
        <Meter value={0.6} tone="session" label="Session" className={styles.meter} />
      </Frame>
      <section className={styles.block}>
        <h2 className={styles.title}>Top bar</h2>
        <div className={styles.bare} data-shot="top-bar">
          <NavTabs activeKey="overview" onSelect={() => undefined}>
            <TopBar tabs={navTabs} dividerBefore="machine" updated="3 s ago" health="ALL NOMINAL" />
          </NavTabs>
        </div>
      </section>
      <section className={styles.block}>
        <h2 className={styles.title}>Log bar</h2>
        <div className={styles.bare} data-shot="log-bar">
          <LogBar segments={logSegments} nextScan="NEXT SCAN 7 s" progress={0.3} />
        </div>
      </section>

      <h1 className={styles.heading}>Chart primitives</h1>
      <Frame shot="chart-ring" title="Ring, arc and core ring" gap={48}>
        <RingChart rings={ringValues} label="Token mix rings" />
        <CoreRing values={coreLoad} efficiencyCores={4} label="Per-core load" />
      </Frame>
      <Frame shot="chart-disc-tower" title="Disc tower" gap={48}>
        <DiscTower discs={discs} label="Token mix tower" />
      </Frame>
      <Frame shot="chart-star-map" title="Star map (d3-force collision)" gap={48}>
        <StarMap nodes={stars} label="Session map" />
      </Frame>
      <Frame shot="chart-shield" title="Shield" gap={48}>
        <Shield value={0.82} display="82%" caption="BLOCKED" label="Guard shield" />
        <Shield value={0.35} display="35%" caption="BLOCKED" tone="warn" label="Guard shield, low" />
      </Frame>
      <Frame shot="chart-honeycomb" title="Hex honeycomb" gap={48}>
        <Honeycomb
          cells={containers.map((c) => ({ name: c.name, value: `${c.memoryMb} MB`, health: c.health }))}
          label="Containers"
        />
      </Frame>
      <Frame shot="chart-line" title="Line and area" gap={48}>
        <LineArea data={tokenFlow} markers={tokenMarkers} label="Token flow, daily" formatY={(v) => `${v}k`} />
      </Frame>
      <Frame shot="chart-uplot" title="uPlot live line" gap={48}>
        <div className={styles.uplot}>
          <UPlotLine data={series} label="Live load" />
        </div>
      </Frame>
    </main>
  );
}
