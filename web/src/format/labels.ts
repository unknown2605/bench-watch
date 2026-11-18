// Same patterns as the label schemas in api/openapi.yaml; a value that fails one is shown as its gate's fallback.
const PROJECT = /^(?:[A-Za-z0-9][A-Za-z0-9._-]{0,119}|scratch \(tmp\))$/;
const MODEL = /^[a-z0-9]{1,24}(?:[._:-][a-z0-9]{1,24}){0,11}(?:\[[0-9]{1,2}[a-z]\])?$/;
const AGENT = /^[A-Za-z][A-Za-z0-9._:-]{0,59}$/;
const TOOL = /^(?:[A-Za-z][A-Za-z0-9_-]{0,59}|Read: (?:whole|ranged|image)|Bash: [a-z0-9][a-z0-9_ -]{0,30}|mcp__[A-Za-z0-9_-]{1,60}__[A-Za-z0-9_-]{1,60})$/;
const CONTAINER = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/;
const STACK = /^[a-z0-9][a-z0-9_-]{0,62}$/;
const APP = /^[A-Za-z0-9][A-Za-z0-9 ._()+&-]{0,63}$/;
const INTERFACE = /^[A-Za-z0-9][A-Za-z0-9 ._()+-]{0,31}$/;
const FILESYSTEM = /^[a-z0-9][a-z0-9_-]{0,15}$/;
const RULE = /^[a-z0-9][a-z0-9._-]{0,47}$/;
const SESSION = /^(?:[0-9a-f]{8}|unknown)$/;

export const isProjectLabel = (v: unknown): v is string => typeof v === 'string' && PROJECT.test(v);
export const isSessionLabel = (v: unknown): v is string => typeof v === 'string' && /^[0-9a-f]{8}$/.test(v);

const gate = (re: RegExp, fallback: string) => (v: unknown) => (typeof v === 'string' && re.test(v) ? v : fallback);

export const projectLabel = gate(PROJECT, 'other');
export const modelLabel = (v: unknown) => (typeof v === 'string' && v.length <= 60 && MODEL.test(v) ? v : 'unknown');
export const agentLabel = gate(AGENT, 'unknown');
export const ruleLabel = gate(RULE, 'other');
export const sessionLabel = gate(SESSION, 'unknown');
export const toolLabel = gate(TOOL, 'other');
export const containerLabel = gate(CONTAINER, 'other');
export const stackLabel = gate(STACK, 'none');
export const appLabel = gate(APP, 'other');
export const volumeLabel = gate(APP, 'other');
export const interfaceLabel = gate(INTERFACE, 'other');
export const filesystemLabel = gate(FILESYSTEM, 'other');

/** Any number the page draws; anything else (a string, NaN) becomes 0. */
export function num(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0;
}

/** A list from the API; a field an older server does not send reads as empty instead of crashing the tab. */
export function rows<T>(v: readonly T[] | undefined): readonly T[] {
  return Array.isArray(v) ? v : [];
}

export function numOrNull(v: unknown): number | null {
  return typeof v === 'number' && Number.isFinite(v) ? v : null;
}
