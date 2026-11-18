import { describe, expect, it } from 'vitest';
import * as f from './format';
import { bucketMean } from './scale';

describe('machine and docker formats', () => {
  it('sizes use 1000s for disks and 1024s for memory', () => {
    expect(f.size(412_000_000_000)).toBe('412 GB');
    expect(f.size(32 * 1024 ** 3, 1024)).toBe('32.0 GB');
    expect(f.size(96 * 1000 ** 2)).toBe('96 MB');
  });

  it('rates carry the per second unit', () => {
    expect(f.rate(4_200_000)).toBe('4.2 MB/s');
    expect(f.rate(8_000)).toBe('8 KB/s');
  });

  it('shows a limit next to the raw value, never a percentage', () => {
    expect(f.withLimit(966_000, 200_000)).toBe('966k (limit 200k)');
    expect(f.withLimit(966_000, 0)).toBe('966k');
  });

  it('splits tool labels into family and detail', () => {
    expect(f.toolFamily('Read: whole')).toBe('Read');
    expect(f.toolDetail('Read: whole')).toBe('whole');
    expect(f.toolFamily('mcp__x__y')).toBe('MCP');
    expect(f.toolDetail('Bash')).toBe('');
  });

  it('shortens compose container names', () => {
    expect(f.containerShort('php84_service_1')).toBe('php84-1');
    expect(f.containerShort('floci_service')).toBe('floci');
    expect(f.containerShort('redis')).toBe('redis');
  });

  it('rounds CPU points and latency', () => {
    expect(f.pctPoints(22.6)).toBe('23%');
    expect(f.ms(3.14)).toBe('3.1 ms');
    expect(f.ms(42.4)).toBe('42 ms');
  });

  it('averages a series into buckets and skips gaps', () => {
    expect(bucketMean([1, 3, null, 5], 2)).toEqual([2, 5]);
    expect(bucketMean([1, 2], 10)).toEqual([1, 2]);
    expect(bucketMean([], 4)).toEqual([]);
  });
});
