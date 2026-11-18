/** Pairs each item with a key from its name, suffixed when a name repeats, so a reordered row keeps its element. */
export function keyed<T>(items: readonly T[], name: (item: T) => string): [string, T][] {
  const seen = new Map<string, number>();
  return items.map((item) => {
    const base = name(item);
    const n = (seen.get(base) ?? 0) + 1;
    seen.set(base, n);
    return [n === 1 ? base : `${base}#${n}`, item];
  });
}
