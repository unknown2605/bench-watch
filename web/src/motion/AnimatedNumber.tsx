import { useAnimatedText } from './useAnimatedText';

type Props = { text: string; scope?: string; className?: string | undefined };

export function AnimatedNumber({ text, scope, className }: Props) {
  const shown = useAnimatedText(text, 600, scope);
  return <span className={className}>{shown}</span>;
}
