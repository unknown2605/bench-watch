const KEY = 'cm-chunk-reloaded';

export class ChunkLoadError extends Error {
  constructor(cause: unknown) {
    super('A page chunk could not be loaded', { cause });
  }
}

const flag = {
  has: () => { try { return sessionStorage.getItem(KEY) !== null; } catch { return true; } },
  set: () => { try { sessionStorage.setItem(KEY, '1'); return true; } catch { return false; } },
  clear: () => { try { sessionStorage.removeItem(KEY); } catch { /* storage unavailable */ } },
};

/** React.lazy caches a rejected import for good, so one reload fetches fresh chunk names; the flag stops a loop. */
export async function loadChunk<T>(
  load: () => Promise<T>,
  reload: () => void = () => window.location.reload(),
): Promise<T> {
  try {
    const loaded = await load();
    flag.clear();
    return loaded;
  } catch (error) {
    if (flag.has()) throw new ChunkLoadError(error);
    if (!flag.set()) throw new ChunkLoadError(error);
    reload();
    return new Promise<T>(() => undefined);
  }
}
