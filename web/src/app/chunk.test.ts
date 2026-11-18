import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ChunkLoadError, loadChunk } from './chunk';

describe('loadChunk', () => {
  beforeEach(() => sessionStorage.clear());

  it('reloads the page once when a chunk fails, and never resolves', async () => {
    const reload = vi.fn();
    const settled = vi.fn();
    void loadChunk(() => Promise.reject(new Error('offline')), reload).then(settled, settled);
    await new Promise((r) => setTimeout(r, 10));
    expect(reload).toHaveBeenCalledTimes(1);
    expect(settled).not.toHaveBeenCalled();
  });

  it('throws instead of reloading again right after a reload', async () => {
    const reload = vi.fn();
    const fail = () => Promise.reject(new Error('offline'));
    void loadChunk(fail, reload);
    await new Promise((r) => setTimeout(r, 10));
    await expect(loadChunk(fail, reload)).rejects.toBeInstanceOf(ChunkLoadError);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('forgets the reload once a chunk loads, so a later failure can reload again', async () => {
    const reload = vi.fn();
    const fail = () => Promise.reject(new Error('offline'));
    void loadChunk(fail, reload);
    await new Promise((r) => setTimeout(r, 10));
    await expect(loadChunk(() => Promise.resolve(1), reload)).resolves.toBe(1);
    void loadChunk(fail, reload);
    await new Promise((r) => setTimeout(r, 10));
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it('does not reload when the flag cannot be stored, so a failing storage cannot loop', async () => {
    const reload = vi.fn();
    const full = { getItem: () => null, removeItem: () => undefined, setItem: () => { throw new Error('full'); } };
    vi.stubGlobal('sessionStorage', full);
    try {
      const failed = loadChunk(() => Promise.reject(new Error('offline')), reload);
      await expect(failed).rejects.toBeInstanceOf(ChunkLoadError);
      expect(reload).not.toHaveBeenCalled();
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
