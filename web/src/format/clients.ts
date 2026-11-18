import type { Schemas } from '../api';

type ClientId = Schemas['Client'];

const NAMES: Record<ClientId, string> = {
  'claude-code': 'Claude Code',
  codex: 'Codex',
  opencode: 'OpenCode',
  'gemini-cli': 'Gemini CLI',
  'qwen-code': 'Qwen Code',
  'copilot-cli': 'Copilot CLI',
};

export const UNVERIFIED = 'format unverified';
export const NOT_FOR_CLIENT = 'Not available for this client';
export const CLAUDE_CODE: ClientId = 'claude-code';

export const clientName = (id: unknown) => (typeof id === 'string' && Object.hasOwn(NAMES, id) ? NAMES[id as ClientId] : 'other');

/** The name as shown beside a figure or in the filter; a reader built from upstream source only says so. */
export function clientLabel(info: { id: string; verified: boolean }): string {
  return info.verified ? clientName(info.id) : `${clientName(info.id)} · ${UNVERIFIED}`;
}

export const isOtherClient = (client: string | null) => client !== null && client !== CLAUDE_CODE;
