// Password-locked documents (the CV). The PDFs live only in this package
// (api/private-docs/), never on the public site or CDN.
//   POST /api/locked/unlock  {password}  -> 204 + session cookie | 401 | 429
//   GET  /api/locked/session             -> 204 when the cookie is valid | 401
//   GET  /api/locked/doc/{id}            -> the PDF when the cookie is valid | 401 | 404
// The password is checked against a scrypt hash (private-docs/access.json, written by
// scripts/set-cv-password.js); the cookie is an expiry signed with a key derived from
// that hash, so changing the password ends every session. An empty password is stored as
// { "open": true }: every session check passes, so the documents open with no prompt.
import { createHash, createHmac, scryptSync, timingSafeEqual } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

export const DOCS_DIR = join(dirname(fileURLToPath(import.meta.url)), '../../private-docs')
export const COOKIE = 'd0m1_locked'
export const SESSION_SECONDS = 12 * 60 * 60
const COOKIE_PATH = '/api/locked'
const MAX_FAILURES = 8
const FAILURE_WINDOW_MS = 15 * 60 * 1000

const noStore = {
  'Cache-Control': 'private, no-store',
  'X-Robots-Tag': 'noindex, nofollow, noarchive',
  'X-Content-Type-Options': 'nosniff',
}

const readJson = (dir, name) => {
  const path = join(dir, name)
  return existsSync(path) ? JSON.parse(readFileSync(path, 'utf8')) : null
}

export function hashPassword(password, salt, { N, r, p }) {
  return scryptSync(password.normalize('NFC'), Buffer.from(salt, 'base64'), 32, { N, r, p, maxmem: 256 * N * r })
}

function sessionKey(access) {
  return createHash('sha256').update(`locked-session|${access.salt}|${access.hash}`).digest()
}

const sign = (key, expires) => createHmac('sha256', key).update(String(expires)).digest('base64url')

function cookieValue(headers, name) {
  for (const part of String(headers.cookie ?? '').split(';')) {
    const [key, ...rest] = part.trim().split('=')
    if (key === name) return rest.join('=')
  }
  return null
}

const isOpen = (access) => access?.open === true

function validSession(access, headers, nowMs) {
  if (isOpen(access)) return true
  const value = cookieValue(headers, COOKIE)
  if (!access || !value) return false
  const [expires, signature] = value.split('.')
  if (!/^\d+$/.test(expires ?? '') || Number(expires) * 1000 <= nowMs || !signature) return false
  const expected = Buffer.from(sign(sessionKey(access), expires))
  const given = Buffer.from(signature)
  return expected.length === given.length && timingSafeEqual(expected, given)
}

function clientKey(headers) {
  return headers['x-azure-clientip'] || String(headers['x-forwarded-for'] ?? '').split(',')[0].trim() || 'unknown'
}

/**
 * Handlers over plain requests ({ method, headers (lowercase), body (string|null), params }),
 * returning { status, headers, body }. One instance per Functions host or dev server:
 * the failure counts are per instance, which is enough to make online guessing slow.
 */
export function createLockedDocs({ dir = DOCS_DIR, now = () => Date.now() } = {}) {
  const failures = new Map()

  const load = () => ({ access: readJson(dir, 'access.json'), index: readJson(dir, 'index.json') ?? {} })

  function unlock({ headers, body }) {
    const { access } = load()
    if (!access) return { status: 503, headers: noStore, body: 'Locked documents are not configured.' }
    if (isOpen(access)) return { status: 204, headers: noStore, body: '' }
    const key = clientKey(headers)
    const nowMs = now()
    const record = failures.get(key)
    if (record && record.resetAt <= nowMs) failures.delete(key)
    const current = failures.get(key)
    if (current && current.count >= MAX_FAILURES) {
      const retryAfter = Math.ceil((current.resetAt - nowMs) / 1000)
      return { status: 429, headers: { ...noStore, 'Retry-After': String(retryAfter) }, body: '' }
    }
    if ((body?.length ?? 0) > 4096) return { status: 413, headers: noStore, body: '' }
    let password = ''
    try { password = String(JSON.parse(body ?? '{}').password ?? '') } catch { /* empty password fails below */ }
    const expected = Buffer.from(access.hash, 'base64')
    const given = password && password.length <= 256 ? hashPassword(password, access.salt, access.scrypt) : Buffer.alloc(expected.length)
    if (!password || !timingSafeEqual(given, expected)) {
      const next = failures.get(key) ?? { count: 0, resetAt: nowMs + FAILURE_WINDOW_MS }
      next.count++
      failures.set(key, next)
      return { status: 401, headers: noStore, body: '' }
    }
    failures.delete(key)
    const expires = Math.floor(nowMs / 1000) + SESSION_SECONDS
    const cookie = `${COOKIE}=${expires}.${sign(sessionKey(access), expires)}; Path=${COOKIE_PATH}; Max-Age=${SESSION_SECONDS}; HttpOnly; Secure; SameSite=Strict`
    return { status: 204, headers: { ...noStore, 'Set-Cookie': cookie }, body: '' }
  }

  function session({ headers }) {
    const { access } = load()
    return { status: validSession(access, headers, now()) ? 204 : 401, headers: noStore, body: '' }
  }

  function doc({ headers, params }) {
    const { access, index } = load()
    if (!validSession(access, headers, now())) return { status: 401, headers: noStore, body: '' }
    const entry = Object.hasOwn(index, params.id ?? '') ? index[params.id] : null
    const path = entry && /^[\w.()-]+\.pdf$/i.test(entry.file) ? join(dir, entry.file) : null
    if (!path || !existsSync(path)) return { status: 404, headers: noStore, body: '' }
    return {
      status: 200,
      headers: {
        ...noStore,
        'Content-Type': 'application/pdf',
        'Content-Disposition': `inline; filename="${entry.downloadName ?? entry.file}"`,
      },
      body: readFileSync(path),
    }
  }

  return { unlock, session, doc }
}
