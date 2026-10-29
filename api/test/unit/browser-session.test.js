import test from 'node:test'
import assert from 'node:assert/strict'
import { memoryStore } from '../../src/lib/tables.js'
import { handleBrowserSession, withBrowserSession, SESSION_COOKIE, COOKIE_SECONDS } from '../../src/lib/browser-session.js'
import { handleLimits } from '../../src/lib/limits.js'
import { handleLink } from '../../src/lib/link.js'
import { handlePrompts } from '../../src/lib/prompts.js'

const principal = { identityProvider: 'github', userId: 'owner-id', userDetails: 'owner', userRoles: ['anonymous', 'authenticated'] }
const encoded = p => Buffer.from(JSON.stringify(p)).toString('base64')
const req = (over = {}) => ({ method: 'POST', headers: { 'sec-fetch-site': 'same-origin', origin: 'https://d0m1.com' }, params: {}, query: {}, now: new Date('2026-10-12T00:00:00Z'), ...over })
const setup = async (p = principal) => {
  const ctx = { store: memoryStore(), env: { AGENTS_OWNER_IDS: 'github:owner' } }
  const login = await handleBrowserSession(req({ headers: { 'x-ms-client-principal': encoded(p) } }), ctx)
  const cookie = login.headers['Set-Cookie']?.split(';')[0]
  return { ctx, login, cookie }
}

test('opaque, secure persistent cookie; only its hash is stored and no server expiry is set', async () => {
  const { ctx, login, cookie } = await setup()
  assert.equal(login.status, 200)
  assert.match(cookie, new RegExp(`^${SESSION_COOKIE}=[a-f0-9]{64}$`))
  assert.match(login.headers['Set-Cookie'], /Path=\/; Max-Age=34560000; HttpOnly; Secure; SameSite=Strict/)
  const rows = await ctx.store.table('browsersessions').list('s')
  assert.equal(rows.length, 1)
  assert.ok(!JSON.stringify(rows).includes(cookie.split('=')[1]))
  assert.equal(rows[0].expiresAt, undefined)
})

test('restores after 24 hours, weeks, years and a new process cache without platform credentials', async () => {
  const { ctx, cookie } = await setup()
  for (const days of [1, 8, 31, 399, 1000]) {
    ctx.store.cache.clear()
    const request = req({ headers: { cookie }, now: new Date(Date.UTC(2026, 9, 12 + days)) })
    const response = await handleBrowserSession(request, ctx)
    assert.deepEqual(response.jsonBody.clientPrincipal, principal)
    assert.match(response.headers['Set-Cookie'], new RegExp(`Max-Age=${COOKIE_SECONDS}`))
    assert.equal((await withBrowserSession(handleLimits)({ ...request, method: 'GET' }, ctx)).status, 200)
  }
})

test('logout revokes server-side; replay cannot restore or read private data even with stale SWA auth', async () => {
  const { ctx, cookie } = await setup()
  const logout = await handleBrowserSession(req({ method: 'GET', headers: { cookie }, params: { action: 'logout' }, query: { post_logout_redirect_uri: '/tokens/agents?x=1#quota' } }), ctx)
  assert.equal(logout.status, 302)
  assert.match(logout.headers['Set-Cookie'], /Max-Age=0/)
  assert.equal(logout.headers.Location, '/.auth/logout?post_logout_redirect_uri=%2Ftokens%2Fagents%3Fx%3D1%23quota')
  const replay = req({ headers: { cookie, 'x-ms-client-principal': encoded(principal) } })
  assert.equal((await handleBrowserSession(replay, ctx)).jsonBody.clientPrincipal, null)
  for (const [handler, method] of [[handleLimits, 'GET'], [handlePrompts, 'GET'], [handleLink, 'POST']]) {
    assert.equal((await withBrowserSession(handler)({ ...replay, method }, ctx)).status, 401)
  }
  assert.equal((await ctx.store.table('browsersessions').list('s')).length, 0)
})

test('anonymous, forged, malformed cookies cannot read private data', async () => {
  const { ctx, cookie } = await setup()
  for (const c of ['', `${SESSION_COOKIE}=nope`, `${SESSION_COOKIE}=${'0'.repeat(64)}`, cookie.slice(0, -1)]) {
    const request = req({ headers: { cookie: c } })
    assert.equal((await handleBrowserSession(request, ctx)).jsonBody.clientPrincipal, null)
    assert.equal((await withBrowserSession(handleLimits)({ ...request, method: 'GET' }, ctx)).status, 401)
  }
})

test('owner permission is rechecked; non-owners stay forbidden and allowlist removal takes effect', async () => {
  const { ctx, cookie } = await setup()
  const read = req({ method: 'GET', headers: { cookie } })
  ctx.env.AGENTS_OWNER_IDS = 'github:someone-else'
  assert.equal((await withBrowserSession(handleLimits)(read, ctx)).status, 403)
  const other = await setup({ ...principal, userDetails: 'someone-else' })
  assert.equal((await withBrowserSession(handleLimits)({ ...read, headers: { cookie: other.cookie } }, other.ctx)).status, 403)
})

test('cross-site and sibling-subdomain requests cannot create, use or revoke a session', async () => {
  const { ctx, cookie } = await setup()
  for (const h of [{ origin: 'https://evil.example' }, { origin: 'https://other.d0m1.com' }, { 'sec-fetch-site': 'cross-site' }, { 'sec-fetch-site': 'same-site' }]) {
    const request = req({ headers: { cookie, 'x-ms-client-principal': encoded(principal), ...h } })
    assert.equal((await handleBrowserSession(request, ctx)).status, 403)
    assert.equal((await handleBrowserSession({ ...request, method: 'GET', params: { action: 'logout' } }, ctx)).status, 403)
    assert.equal((await withBrowserSession(handleLimits)({ ...request, method: 'GET' }, ctx)).status, 403)
  }
  assert.equal((await ctx.store.table('browsersessions').list('s')).length, 1)
})

test('same-origin sign-in, private reads and logout work on both production hostnames', async () => {
  for (const origin of ['https://d0m1.com', 'https://www.d0m1.com']) {
    const ctx = { store: memoryStore(), env: { AGENTS_OWNER_IDS: 'github:owner' } }
    const headers = { origin, 'sec-fetch-site': 'same-origin', 'x-ms-client-principal': encoded(principal) }
    const login = await handleBrowserSession(req({ headers }), ctx)
    assert.equal(login.status, 200)
    assert.deepEqual(login.jsonBody.clientPrincipal, principal)
    const cookie = login.headers['Set-Cookie'].split(';')[0]
    const session = req({ headers: { origin, 'sec-fetch-site': 'same-origin', cookie } })
    assert.deepEqual((await handleBrowserSession(session, ctx)).jsonBody.clientPrincipal, principal)
    assert.equal((await withBrowserSession(handleLimits)({ ...session, method: 'GET' }, ctx)).status, 200)
    // Browser requests between the two hosts are same-site, not same-origin.
    assert.equal((await handleBrowserSession({ ...session, headers: { ...session.headers, 'sec-fetch-site': 'same-site' } }, ctx)).status, 403)
    assert.equal((await handleBrowserSession({ ...session, method: 'GET', params: { action: 'logout' } }, ctx)).status, 302)
    assert.equal((await handleBrowserSession(session, ctx)).jsonBody.clientPrincipal, null)
  }
})

test('logout redirects are local; platform-only auth still works during migration', async () => {
  const { ctx } = await setup()
  for (const target of ['https://evil.example', '//evil.example', '/%2fexample', '/\\evil.example', '/api/session', '/.auth/login/github', '/%0aLocation:bad']) {
    const out = await handleBrowserSession(req({ method: 'GET', params: { action: 'logout' }, query: { post_logout_redirect_uri: target } }), ctx)
    assert.equal(out.headers.Location, '/.auth/logout?post_logout_redirect_uri=%2Ftokens')
  }
  assert.equal((await withBrowserSession(handleLimits)(req({ method: 'GET', headers: { 'x-ms-client-principal': encoded(principal) } }), ctx)).status, 200)
})

test('a read overlapping logout cannot recreate the deleted session', async () => {
  const { ctx, cookie } = await setup()
  let release, started
  const ready = new Promise(resolve => { started = resolve })
  const pending = withBrowserSession(async () => { started(); await new Promise(resolve => { release = resolve }); return { status: 200, headers: {} } })(req({ headers: { cookie } }), ctx)
  await ready
  await handleBrowserSession(req({ method: 'GET', headers: { cookie }, params: { action: 'logout' } }), ctx)
  release()
  await pending
  assert.equal((await handleBrowserSession(req({ headers: { cookie } }), ctx)).jsonBody.clientPrincipal, null)
})
