// POST /api/github/device/code and POST /api/github/device/token (SPEC "GitHub dashboard sign-in relay"): the
// tokenmaxr GitHub Pages dashboard signs its owner in with GitHub's device flow, on any device. GitHub's two
// device-flow endpoints send no CORS headers, so a static page cannot call them; this relays exactly those two
// calls and nothing else.
//
// - The client id is pinned to the tokenmaxor GitHub App (the collector's, collector/release.json): the caller
//   cannot name another App, another URL or any other parameter. The device flow needs no client secret, and
//   this relay holds none.
// - code: POST https://github.com/login/device/code with client_id only. token: POST
//   https://github.com/login/oauth/access_token with client_id, the caller's device_code and the device-code
//   grant type only. GitHub's JSON comes back as it is, with GitHub's status.
// - The token answer carries the user's GitHub token to the page that asked: it is never logged or kept here.
// - CORS for any https origin (Pages sites are https://<user>.github.io, or a custom domain) and for a local
//   preview (http://localhost, 127.0.0.1). Never credentials: the page sends no cookies and gets none.
// - A small per-address rate limit per Functions instance, as GitHub limits the App's device flow too.
import { error, json } from './http.js'

/** The tokenmaxor GitHub App's client id (public; test/unit/github-device.test.js checks it against the collector's). */
export const GITHUB_CLIENT_ID = 'Iv23lisUk4XpDfcdK0Mh'
export const DEVICE_CODE_URL = 'https://github.com/login/device/code'
export const ACCESS_TOKEN_URL = 'https://github.com/login/oauth/access_token'
export const DEVICE_GRANT = 'urn:ietf:params:oauth:grant-type:device_code'
const UPSTREAM_TIMEOUT_MS = 10_000
// GitHub's device codes are 40 hex characters; allow some slack, never anything that could smuggle a parameter.
const DEVICE_CODE_RE = /^[A-Za-z0-9_-]{8,128}$/

/** Per address and Functions instance: a sign-in takes one code and about 180 polls (5 s for 15 minutes). */
export const RATE_LIMITS = {
  code: { limit: 20, windowMs: 15 * 60_000 },
  token: { limit: 400, windowMs: 15 * 60_000 },
}

const LOCAL_RE = /^http:\/\/(?:localhost|127\.0\.0\.1|\[::1\])(?::\d{1,5})?$/

/** The origin CORS lets read the answer: any https origin or a local preview; null for anything else. */
export function allowedOrigin(origin) {
  if (typeof origin !== 'string' || origin.length > 300) return null
  if (LOCAL_RE.test(origin)) return origin
  let url
  try {
    url = new URL(origin)
  } catch {
    return null
  }
  // An origin is scheme://host[:port], nothing after it.
  return url.protocol === 'https:' && url.origin === origin ? origin : null
}

function corsHeaders(origin) {
  const allowed = allowedOrigin(origin)
  return {
    'Cache-Control': 'no-store',
    Vary: 'Origin',
    ...(allowed ? {
      'Access-Control-Allow-Origin': allowed,
      'Access-Control-Allow-Methods': 'POST, OPTIONS',
      'Access-Control-Allow-Headers': 'Content-Type, Accept',
      'Access-Control-Max-Age': '600',
    } : {}),
  }
}

/** A fixed-window counter per key, swept as it goes; one per instance (the Functions host may run several). */
export function createRateLimiter({ limit, windowMs }) {
  const hits = new Map()
  return (key, now = Date.now()) => {
    if (hits.size > 10_000) {
      for (const [k, v] of hits) if (now - v.start >= windowMs) hits.delete(k)
    }
    let entry = hits.get(key)
    if (!entry || now - entry.start >= windowMs) {
      entry = { start: now, count: 0 }
      hits.set(key, entry)
    }
    entry.count++
    return entry.count <= limit ? 0 : Math.ceil((entry.start + windowMs - now) / 1000)
  }
}

const limiters = {
  code: createRateLimiter(RATE_LIMITS.code),
  token: createRateLimiter(RATE_LIMITS.token),
}

/** The caller's address as Static Web Apps hands it on. */
function clientAddress(headers) {
  const forwarded = String(headers['x-forwarded-for'] || '').split(',')[0].trim()
  return String(headers['x-azure-clientip'] || '').trim() || forwarded.replace(/:\d+$/, '') || 'unknown'
}

/**
 * Handles both steps (params.step: code | token). ctx.fetch replaces the global fetch in tests; ctx.limiters the
 * instance's rate limiters.
 */
export async function handleGithubDevice(req, ctx = {}) {
  const cors = corsHeaders(req.headers.origin)
  const answer = (res) => ({ ...res, headers: { ...res.headers, ...cors } })
  const step = req.params?.step
  if (step !== 'code' && step !== 'token') return answer(error(404, 'not found'))
  if (req.method === 'OPTIONS') return { status: 204, headers: cors }
  if (req.method !== 'POST') return answer(error(405, 'method not allowed'))
  // A browser on an origin this does not serve: refuse before anything reaches GitHub.
  if (req.headers.origin !== undefined && !allowedOrigin(req.headers.origin)) return answer(error(403, 'origin not allowed'))

  const params = new URLSearchParams({ client_id: GITHUB_CLIENT_ID })
  if (step === 'token') {
    const deviceCode = req.body && typeof req.body === 'object' && !Array.isArray(req.body) ? req.body.device_code : undefined
    if (typeof deviceCode !== 'string' || !DEVICE_CODE_RE.test(deviceCode)) return answer(error(400, 'device_code is required'))
    params.set('device_code', deviceCode)
    params.set('grant_type', DEVICE_GRANT)
  }

  const limited = (ctx.limiters ?? limiters)[step](clientAddress(req.headers), req.now?.getTime())
  if (limited) return answer(json(429, { error: 'rate_limited', error_description: 'Too many sign-in requests. Try again in a few minutes.' }, { 'Retry-After': String(limited) }))

  const get = ctx.fetch ?? globalThis.fetch
  let upstream
  try {
    upstream = await get(step === 'code' ? DEVICE_CODE_URL : ACCESS_TOKEN_URL, {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/x-www-form-urlencoded', 'User-Agent': 'tokenmaxr-dashboard-relay' },
      body: params.toString(),
      redirect: 'error',
      signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS),
    })
  } catch {
    return answer(json(502, { error: 'github_unreachable', error_description: 'GitHub could not be reached. Try again shortly.' }))
  }
  let body
  try {
    body = JSON.parse(await upstream.text())
  } catch {
    body = undefined
  }
  if (body === null || typeof body !== 'object' || Array.isArray(body)) {
    return answer(json(502, { error: 'github_bad_answer', error_description: `GitHub answered ${upstream.status} without JSON.` }))
  }
  return answer(json(upstream.status, body))
}
