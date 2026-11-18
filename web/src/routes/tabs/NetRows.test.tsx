import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Schemas } from '../../api';
import { machineView } from './Machine';
import { NetRows } from './NetRows';
import { fixtures } from './testData';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const point = (rxBps: number, txBps: number) => ({ at: new Date().toISOString(), rxBps, txBps });
const iface = (name: string, rxBps: number, txBps: number) => ({ interface: name, rxBps, txBps, mergedNames: 0 });

function machine(...list: ReturnType<typeof iface>[]) {
  const m = structuredClone(fixtures(Date.now()).machine) as Schemas['MachineResponse'];
  if (m.now) m.now.interfaces = list;
  m.interfaceSeries = list.map((i) => ({ interface: i.interface, peakRxBps: i.rxBps, peakRxAt: null,
    points: [point(1, 1), point(i.rxBps, i.txBps)] }));
  return m;
}

describe('machineView network', () => {
  const m = machine(iface('utun0', 9_000_000, 1), iface('lo0', 5, 5), iface('awdl0', 7, 7), iface('en0', 10, 20));

  it('charts the uplink, lists visible rows in order, and never sums', () => {
    const v = machineView(m);
    expect(v.network.name).toBe('en0');
    expect(v.network.rows.map((r) => [r.name, r.kind]))
      .toEqual([['en0', 'Wi-Fi/Ethernet'], ['utun0', 'VPN'], ['lo0', 'Local']]);
    expect(v.network.rows[0]?.sparkDown).toEqual([1, 10]);
    expect(v.network.down).toBe(v.network.rows[0]?.down);
  });

  it('keeps the uplink as another interface gets busier', () => {
    const busy = machine(iface('utun0', 9e9, 9e9), iface('en0', 1, 1));
    expect(machineView(busy).network.name).toBe('en0');
  });

  it('follows the selection and falls back when it disappears', () => {
    expect(machineView(m, 'utun0').network.name).toBe('utun0');
    expect(machineView(m, 'utun0').network.down).toBe(machineView(m).network.rows[1]?.down);
    expect(machineView(machine(iface('en0', 1, 1)), 'utun0').network.name).toBe('en0');
  });
});

describe('NetRows', () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;
  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = null;
    host = null;
  });
  const rows = ['en0', 'utun0', 'lo0'].map((id) => ({
    id, name: id, kind: 'k', down: '1 B/s', up: '2 B/s', sparkDown: [1, 2], sparkUp: [2, 1],
  }));
  const mount = (onSelect: (id: string) => void) => {
    host = document.createElement('div');
    document.body.append(host);
    root = createRoot(host);
    act(() => root?.render(<NetRows rows={rows} selected="en0" onSelect={onSelect} />));
    return [...host.querySelectorAll<HTMLElement>('[role="option"]')];
  };

  it('marks the selected row and selects another by click', () => {
    const onSelect = vi.fn();
    const options = mount(onSelect);
    expect(options.map((o) => o.getAttribute('aria-selected'))).toEqual(['true', 'false', 'false']);
    act(() => {
      options[1]?.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
      options[1]?.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
      options[1]?.click();
    });
    expect(onSelect).toHaveBeenCalledWith('utun0');
  });

  it('selects with Enter and Space on a focused row', () => {
    const onSelect = vi.fn();
    const options = mount(onSelect);
    for (const key of ['Enter', ' ']) {
      act(() => {
        options[1]?.focus();
        options[1]?.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
        options[1]?.dispatchEvent(new KeyboardEvent('keyup', { key, bubbles: true }));
      });
    }
    expect(onSelect).toHaveBeenCalledWith('utun0');
  });

  it('moves focus with the arrow keys without selecting, then Enter selects', () => {
    const onSelect = vi.fn();
    const options = mount(onSelect);
    const press = (target: HTMLElement | undefined, key: string) => act(() => {
      target?.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
      target?.dispatchEvent(new KeyboardEvent('keyup', { key, bubbles: true }));
    });
    act(() => options[0]?.focus());
    press(options[0], 'ArrowDown');
    expect(document.activeElement).toBe(options[1]);
    press(options[1], 'ArrowDown');
    expect(document.activeElement).toBe(options[2]);
    press(options[2], 'ArrowUp');
    expect(document.activeElement).toBe(options[1]);
    expect(onSelect).not.toHaveBeenCalled();
    press(options[1], 'Enter');
    expect(onSelect).toHaveBeenCalledWith('utun0');
  });
});
