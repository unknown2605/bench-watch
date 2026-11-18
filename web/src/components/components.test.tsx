import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { Badge, LogBar, Marker, Meter, Planet, Status } from '.';

describe('components', () => {
  it('Meter clamps its value and exposes it', () => {
    const html = renderToStaticMarkup(<Meter value={1.7} label="fill" />);
    expect(html).toContain('aria-valuenow="100"');
    expect(renderToStaticMarkup(<Meter value={-1} label="fill" />)).toContain('aria-valuenow="0"');
  });
  it('Status shows its default label', () => {
    expect(renderToStaticMarkup(<Status kind="warn" />)).toContain('WARN');
  });
  it('Badge, Marker and Planet render with an accessible name', () => {
    expect(renderToStaticMarkup(<Badge kind="gold">NEW</Badge>)).toContain('NEW');
    expect(renderToStaticMarkup(<Marker kind="peak" label="Peak" />)).toContain('aria-label="Peak"');
    expect(renderToStaticMarkup(<Planet kind="selected" label="Selected" />)).toContain('aria-label="Selected"');
  });
  it('LogBar announces the full line once, in a spoken span, and hides the compact one', () => {
    const html = renderToStaticMarkup(
      <LogBar segments={['a 1', '+1 MORE']} spoken={['a 1', 'b 2']} nextScan={null} progress={0} />,
    );
    expect(html).toMatch(
      /<span class="[^"]*compact[^"]*" aria-hidden="true" title="a 1 {3}· {3}b 2">a 1 {3}· {3}\+1 MORE<\/span>/,
    );
    expect(html).toMatch(/<span class="[^"]*spoken[^"]*">a 1 {3}· {3}b 2<\/span>/);
    const line = /<p[^>]*>/.exec(html)?.[0] ?? '';
    expect(line).not.toContain('title=');
    expect(line).toContain('tabindex="0"');
    expect(line).toContain('role="region"');
    expect(line).toContain('aria-label="Activity log"');
  });
});
