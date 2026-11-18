import type { components, paths } from './schema.d.ts';

export type Schemas = components['schemas'];
export type Paths = paths;
export type JsonOf<P extends keyof paths> = paths[P]['get']['responses'][200]['content'] extends infer C
  ? C extends { 'application/json': infer T } ? T : never
  : never;
export type LiveEvent = Schemas['LiveEvent'];
export type LiveEventType = LiveEvent['type'];
