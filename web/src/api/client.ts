import type { Schemas } from './index';

export type ErrorCode = Schemas['ErrorResponse']['code'];

export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode | 'network' | 'bad_body';
  constructor(status: number, code: ApiError['code']) {
    super(`api ${status} ${code}`);
    this.status = status;
    this.code = code;
  }
}

const CODES: readonly ErrorCode[] = [
  'bad_range', 'bad_date', 'bad_label', 'bad_group', 'bad_request', 'bad_method', 'not_found', 'history_paused', 'unavailable',
  'internal', 'not_for_client',
];

function codeOf(body: unknown): ErrorCode {
  const code = typeof body === 'object' && body !== null ? (body as { code?: unknown }).code : undefined;
  return CODES.find((c) => c === code) ?? 'internal';
}

export type Query = Record<string, string | undefined>;

export function apiUrl(path: string, query: Query = {}): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== '') params.set(k, v);
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

/** Same-origin GET; nginx (or the vite dev proxy) forwards /api to the server. */
export async function getJson<T>(path: string, query: Query = {}, signal?: AbortSignal): Promise<T> {
  let res: Response;
  try {
    res = await fetch(apiUrl(path, query), { headers: { Accept: 'application/json' }, ...(signal ? { signal } : {}) });
  } catch (e) {
    if (signal?.aborted) throw e;
    throw new ApiError(0, 'network');
  }
  let body: unknown;
  try {
    body = await res.json();
  } catch {
    throw new ApiError(res.status, res.ok ? 'bad_body' : 'internal');
  }
  if (!res.ok) throw new ApiError(res.status, codeOf(body));
  return body as T;
}
