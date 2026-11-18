import { useLayoutEffect, useState, type RefObject } from 'react';
import { cssZoomOf } from '../app/uiScale';

export function useElementSize(ref: RefObject<HTMLElement | null>): [number, number] {
  const [size, setSize] = useState<[number, number]>([0, 0]);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    // Under page zoom the on-screen box is divided back to layout pixels, so drawings are not scaled twice.
    const measure = () => {
      const zoom = cssZoomOf(el);
      const box = zoom === 1 ? null : el.getBoundingClientRect();
      const w = box ? Math.round(box.width / zoom) : el.offsetWidth;
      const h = box ? Math.round(box.height / zoom) : el.offsetHeight;
      setSize((prev) => (prev[0] === w && prev[1] === h ? prev : [w, h]));
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [ref]);
  return size;
}
