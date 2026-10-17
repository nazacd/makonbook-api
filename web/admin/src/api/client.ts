import createClient from 'openapi-fetch';

import type { components, paths } from './schema';

export type Schemas = components['schemas'];

// Same origin: the panel is served on the admin host, which also passes
// /v1/admin/* to the API. The session cookie is HttpOnly; the page never
// sees it.
export const api = createClient<paths>({ baseUrl: '', credentials: 'same-origin' });

/**
 * Every admin request that changes something must carry this header (a
 * page on another site can't add it), so it goes on every request.
 */
export const CSRF = { 'X-MakonBook-Admin': '1' } as const;
api.use({
  onRequest({ request }) {
    request.headers.set('X-MakonBook-Admin', '1');
    return request;
  },
});

/** An error answer from the API. Branch on `code`, never on `message`. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly requestId?: string,
    /** For a rejected form: what is wrong with each field. */
    readonly fields?: Record<string, string>,
  ) {
    super(message);
  }
}

type Result<T> = { data?: T; error?: unknown; response: Response };

/** Returns the data of a call, or throws an ApiError. */
export async function call<T>(promise: Promise<Result<T>>): Promise<T> {
  let res: Result<T>;
  try {
    res = await promise;
  } catch {
    throw new ApiError(0, 'network', 'Can’t reach MakonBook. Check the connection and try again.');
  }
  if (res.response.ok) return res.data as T;
  const body = (res.error ?? {}) as {
    error?: { code?: string; message?: string; request_id?: string; fields?: Record<string, string> };
  };
  throw new ApiError(
    res.response.status,
    body.error?.code ?? 'unknown',
    body.error?.message ?? `Unexpected answer (${res.response.status}).`,
    body.error?.request_id,
    body.error?.fields,
  );
}

/** The words to show for an error. */
export function describe(error: unknown): string {
  if (!(error instanceof ApiError)) return 'Something went wrong.';
  if (error.status >= 500 && error.code === 'internal') {
    return `MakonBook had a problem. Try again; if it keeps happening, quote ${error.requestId ?? 'the time'} to the developer.`;
  }
  return error.message.charAt(0).toUpperCase() + error.message.slice(1) + (error.message.endsWith('.') ? '' : '.');
}
