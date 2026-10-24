// SWA signs the user in; our revocable session keeps that login across SWA expiry.
// Store only a hash of the random cookie. There is deliberately no idle/absolute TTL.
import { randomBytes, createHash } from 'node:crypto'
import { parsePrincipal } from './auth.js'
import { resolveCtx } from './context.js'
import { error, json } from './http.js'

export const SESSION_COOKIE = '__Host-d0m1_session'
export const COOKIE_SECONDS = 400 * 24 * 60 * 60
const hash = token => createHash('sha256').update(token).digest('hex')
const cookie = (token, age = COOKIE_SECONDS) => `${SESSION_COOKIE}=${token}; Path=/; Max-Age=${age}; HttpOnly; Secure; SameSite=Strict`
const tokenFrom = headers => {
  const value = String(headers.cookie || '').split(';').map(s => s.trim()).find(s => s.startsWith(SESSION_COOKIE + '='))?.slice(SESSION_COOKIE.length + 1)
  return /^[a-f0-9]{64}$/.test(value || '') ? value : null
}
const authenticated = p => p?.userId && p.identityProvider && p.userRoles.includes('authenticated')
// Reject cross-origin cookie use, including sibling subdomains. Non-browser machine
// clients have neither Origin nor Sec-Fetch-Site and retain their existing token auth.
export const sameOrigin = headers => (!headers['sec-fetch-site'] || ['same-origin', 'none'].includes(headers['sec-fetch-site'])) &&
  (!headers.origin || headers.origin === 'https://d0m1.com')

async function readSession(req, store) {
  const token = tokenFrom(req.headers)
  if (!token) return null
  const row = await store.table('browsersessions').get('s', hash(token))
  if (!row) return null
  const principal = JSON.parse(row.principal)
  return authenticated(principal) ? { token, principal } : null
}

/** All private handlers still run their existing owner/allowlist authorization. */
export function withBrowserSession(handler) {
  return async (req, ctx) => {
    if (!sameOrigin(req.headers)) return error(403, 'same-origin access required')
    // Resolve stored credentials on each request: logout takes effect across instances.
    const session = tokenFrom(req.headers) ? await readSession(req, resolveCtx(ctx).store) : null
    const headers = { ...req.headers }
    if (session) headers['x-ms-client-principal'] = Buffer.from(JSON.stringify(session.principal)).toString('base64')
    // A revoked cookie cannot fall back to the platform's still-live login.
    else if (tokenFrom(headers)) delete headers['x-ms-client-principal']
    const response = await handler({ ...req, headers }, ctx)
    if (session) response.headers = { ...response.headers, 'Set-Cookie': cookie(session.token) }
    return response
  }
}

function returnTo(value) {
  if (typeof value !== 'string' || !value.startsWith('/') || /[\\\x00-\x20\x7f]/.test(value)) return '/tokens'
  try {
    const decoded = decodeURIComponent(value)
    if (decoded.startsWith('//') || /[\\\x00-\x1f\x7f]/.test(decoded) || /^\/(?:api|\.auth)(?:\/|$)/.test(decoded)) return '/tokens'
    return new URL(value, 'https://d0m1.com').origin === 'https://d0m1.com' ? value : '/tokens'
  } catch { return '/tokens' }
}

export async function handleBrowserSession(req, ctx) {
  if (!sameOrigin(req.headers)) return error(403, 'same-origin access required')
  const { store } = resolveCtx(ctx)
  const token = tokenFrom(req.headers)
  if (req.params.action === 'logout') {
    if (req.method !== 'GET') return error(405, 'method not allowed')
    if (token) await store.table('browsersessions').delete('s', hash(token))
    return json(302, null, { 'Set-Cookie': cookie('', 0), Location: '/.auth/logout?post_logout_redirect_uri=' + encodeURIComponent(returnTo(req.query.post_logout_redirect_uri)) })
  }
  if (req.method !== 'POST') return error(405, 'method not allowed')
  const existing = await readSession(req, store)
  if (existing) return json(200, { clientPrincipal: existing.principal }, { 'Set-Cookie': cookie(existing.token) })
  const principal = parsePrincipal(req.headers)
  if (token || !authenticated(principal)) return json(200, { clientPrincipal: null }, token ? { 'Set-Cookie': cookie('', 0) } : {})
  const fresh = randomBytes(32).toString('hex')
  await store.table('browsersessions').create({ partitionKey: 's', rowKey: hash(fresh), principal: JSON.stringify(principal), createdAt: req.now.toISOString() })
  return json(200, { clientPrincipal: principal }, { 'Set-Cookie': cookie(fresh) })
}
