import { formatLike, parseNumber } from './number';
import { useTween } from './useTween';

/** A new `scope` (the item shown) makes the number appear at its value instead of counting from the last item's. */
export function useAnimatedText(text: string, duration = 600, scope = ''): string {
  const parsed = parseNumber(text);
  const unit = parsed ? `${scope}|${parsed.pre}|${parsed.post}|${parsed.decimals}` : '';
  const value = useTween(parsed ? parsed.value : 0, duration, unit);
  if (!parsed || value === parsed.value) return text;
  return `${parsed.pre}${formatLike(value, parsed)}${parsed.post}`;
}
