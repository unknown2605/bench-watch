const NUMBER = /-?\d[\d,]*(?:\.\d+)?/;

export type ParsedNumber = { pre: string; post: string; value: number; decimals: number; grouped: boolean };

/** Splits "$1,284.50 est." into its first number and the text round it; null when there is no number. */
export function parseNumber(text: string): ParsedNumber | null {
  const m = NUMBER.exec(text);
  if (!m) return null;
  const raw = m[0];
  const value = Number(raw.replaceAll(',', ''));
  if (!Number.isFinite(value)) return null;
  const dot = raw.indexOf('.');
  return {
    pre: text.slice(0, m.index),
    post: text.slice(m.index + raw.length),
    value,
    decimals: dot < 0 ? 0 : raw.length - dot - 1,
    grouped: raw.includes(','),
  };
}

export function formatLike(value: number, like: Pick<ParsedNumber, 'decimals' | 'grouped'>): string {
  const fixed = Math.abs(value).toFixed(like.decimals);
  const [int = '0', frac] = fixed.split('.');
  const body = like.grouped ? int.replace(/\B(?=(\d{3})+(?!\d))/g, ',') : int;
  return `${value < 0 ? '-' : ''}${body}${frac ? `.${frac}` : ''}`;
}
