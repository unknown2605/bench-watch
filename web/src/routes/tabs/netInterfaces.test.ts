import { describe, expect, it } from 'vitest';
import {
  defaultInterface, isHidden, netKind, RECENT_TRAFFIC_MS, selectedInterface, visibleInterfaces,
} from './netInterfaces';

const n = (name: string, rxBps = 100, txBps = 0) => ({ interface: name, rxBps, txBps });
const T0 = Date.parse('2026-11-18T10:00:00Z');
const at = (secondsAgo: number) => new Date(T0 - secondsAgo * 1000).toISOString();
const sample = (secondsAgo: number, bps: number) => ({ at: at(secondsAgo), rxBps: bps, txBps: 0 });
const named = (name: string, ...points: ReturnType<typeof sample>[]) => ({ interface: name, points });
const series = (...names: string[]) => names.map((name) => named(name, sample(5, 100)));

describe('netKind', () => {
  it.each([
    ['en0', 'uplink'], ['en1', 'uplink'], ['Wi-Fi', 'uplink'], ['Ethernet', 'uplink'], ['eth0', 'uplink'],
    ['wlan0', 'uplink'], ['wlp3s0', 'uplink'], ['enp0s31f6', 'uplink'],
    ['utun0', 'vpn'], ['ipsec0', 'vpn'], ['ppp0', 'vpn'], ['tun0', 'vpn'], ['wg0', 'vpn'],
    ['lo0', 'local'], ['lo', 'local'], ['docker0', 'other'], ['bridge100', 'other'], ['x', 'other'],
  ])('%s is %s', (name, kind) => expect(netKind(name)).toBe(kind));
});

describe('hiding', () => {
  it.each(['awdl0', 'llw0', 'anpi0', 'ap1', 'gif0', 'stf0'])('%s never shows, even busy', (name) => {
    expect(isHidden(n(name, 5000, 5000))).toBe(true);
  });
  it('shows a bridge only while it carries traffic', () => {
    expect(isHidden(n('bridge100', 0, 0))).toBe(true);
    expect(isHidden(n('bridge100', 1, 0))).toBe(false);
  });
  it('drops system rows from the list', () => {
    const names = visibleInterfaces([n('awdl0'), n('en0'), n('llw0'), n('lo0')]).map((i) => i.interface);
    expect(names).toEqual(['en0', 'lo0']);
  });
});

describe('ordering', () => {
  it('goes by kind then name, loopback last, whatever the traffic', () => {
    const rows = [n('lo0', 9e9), n('utun1', 8e9), n('en10', 1), n('en2', 2), n('docker0', 7e9), n('utun0')];
    expect(visibleInterfaces(rows).map((i) => i.interface))
      .toEqual(['en2', 'en10', 'utun0', 'utun1', 'docker0', 'lo0']);
  });
  it('does not reorder when traffic shifts', () => {
    const a = visibleInterfaces([n('en0', 1), n('utun0', 900)]).map((i) => i.interface);
    const b = visibleInterfaces([n('en0', 900), n('utun0', 1)]).map((i) => i.interface);
    expect(a).toEqual(b);
  });
});

describe('default choice', () => {
  it('is the physical uplink even when a VPN is busier', () => {
    expect(defaultInterface([n('utun0', 9e9), n('en0', 1), n('lo0')], series('utun0', 'en0'))).toBe('en0');
  });
  it('keeps the uplink while it is idle and missing from the live list', () => {
    expect(defaultInterface([n('utun0', 5)], series('en0', 'utun0'))).toBe('en0');
  });
  it('drops an unplugged uplink once its traffic is older than the recent window', () => {
    const old = RECENT_TRAFFIC_MS / 1000 + 30;
    const gone = [named('en0', sample(old + 20, 50), sample(old, 0)), named('en1', sample(10, 80), sample(0, 90))];
    expect(defaultInterface([n('en1')], gone)).toBe('en1');
    expect(defaultInterface([], gone)).toBe('en1');
  });
  it('ignores a series that only holds zero samples', () => {
    const idle = [named('en0', sample(10, 0), sample(0, 0)), named('utun0', sample(0, 5))];
    expect(defaultInterface([], idle)).toBe('utun0');
  });
  it('keeps an uplink whose last traffic is inside the recent window', () => {
    const brief = [named('en0', sample(40, 50), sample(30, 0)), named('en1', sample(0, 90))];
    expect(defaultInterface([n('en1')], brief)).toBe('en0');
  });
  it('widens the recent window to the point spacing of an hourly rollup', () => {
    const hourly = [named('en0', sample(7200, 50), sample(3600, 50)), named('utun0', sample(3600, 5), sample(0, 5))];
    expect(defaultInterface([n('utun0')], hourly)).toBe('en0');
    const stale = [named('en0', sample(10800, 50), sample(7200, 50)), named('utun0', sample(3600, 5), sample(0, 5))];
    expect(defaultInterface([n('utun0')], stale)).toBe('utun0');
  });
  it('falls back to other, then VPN, then loopback, then nothing', () => {
    expect(defaultInterface([n('docker0'), n('utun0'), n('lo0')], [])).toBe('docker0');
    expect(defaultInterface([n('utun0'), n('lo0')], [])).toBe('utun0');
    expect(defaultInterface([n('lo0')], [])).toBe('lo0');
    expect(defaultInterface([n('awdl0')], series('awdl0'))).toBeNull();
  });
});

describe('selection fallback', () => {
  it('keeps a listed choice', () => expect(selectedInterface('utun0', ['en0', 'utun0'], 'en0')).toBe('utun0'));
  it('falls back to the default when the choice disappears or none was made', () => {
    expect(selectedInterface('utun0', ['en0'], 'en0')).toBe('en0');
    expect(selectedInterface(null, ['en0', 'utun0'], 'en0')).toBe('en0');
  });
});
