// A loop, not Math.min(...values): spreading passes each value as an argument, which throws past about 125k.
export function extent(values: Iterable<number>): [min: number, max: number] {
  let min = Infinity;
  let max = -Infinity;
  for (const v of values) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  return [min, max];
}
