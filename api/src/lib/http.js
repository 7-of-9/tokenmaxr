// Shared request plumbing. Pure handlers take
//   { method, headers (lowercased keys), query (plain object), params, body, now }
// and return { status, headers, jsonBody }. runHandler() does the body limits
// and JSON parsing once, for both the Functions adapters and the local harness.

export const MAX_BODY_BYTES = 1024 * 1024

export function json(status, jsonBody, headers = {}) {
  return { status, headers: { 'Cache-Control': 'no-store', ...headers }, jsonBody }
}

export function error(status, message, extra = {}) {
  return json(status, { ok: false, error: message, ...extra })
}

export function lowerHeaders(entries) {
  const out = {}
  for (const [k, v] of entries) out[k.toLowerCase()] = v
  return out
}

// readBody: async () => string | null, called only after the length check.
export async function runHandler(handler, { method, headers, query, params, readBody }, ctx) {
  // The answers made here carry the handler's responseHeaders (optional) too.
  const fail = (status, message) => {
    const r = error(status, message)
    return { ...r, headers: { ...r.headers, ...handler.responseHeaders } }
  }
  const len = Number(headers['content-length'] || 0)
  if (len > MAX_BODY_BYTES) return fail(413, 'body too large')
  const rawBody = method === 'GET' || method === 'HEAD' ? null : await readBody()
  let body = null
  if (rawBody != null && rawBody !== '') {
    if (Buffer.byteLength(rawBody, 'utf8') > MAX_BODY_BYTES) return fail(413, 'body too large')
    try {
      body = JSON.parse(rawBody)
    } catch {
      return fail(400, 'invalid JSON')
    }
  }
  try {
    return await handler({ method, headers, query: query || {}, params: params || {}, body, now: new Date() }, ctx)
  } catch (err) {
    // Never log request bodies: they can hold prompt text.
    console.error(`[agents-api] ${method} failed: ${err?.name || 'Error'} ${err?.statusCode || ''} ${err?.message || ''}`)
    return fail(503, 'temporarily unavailable')
  }
}
