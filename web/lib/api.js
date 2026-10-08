// api.js wraps the server's JSON endpoints.

// APIError carries the HTTP status and, for an invalid query, the byte
// offset of the problem in the query.
export class APIError extends Error {
  constructor(message, status, pos) {
    super(message);
    this.status = status;
    this.pos = pos;
  }
}

async function decode(response) {
  let body = null;
  try {
    body = await response.json();
  } catch {
    // Error pages and empty bodies aren't JSON; the status says enough.
  }
  if (!response.ok) {
    throw new APIError(body?.error || `HTTP ${response.status}`, response.status, body?.pos);
  }
  return body;
}

export async function getJSON(path, params, signal) {
  const query = params ? '?' + new URLSearchParams(params).toString() : '';
  return decode(await fetch(path + query, { signal }));
}

export async function postJSON(path, payload) {
  return decode(await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: payload === undefined ? undefined : JSON.stringify(payload),
  }));
}

export function isAbort(error) {
  return error?.name === 'AbortError';
}
