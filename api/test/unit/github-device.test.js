// POST /api/github/device/code and /token: a CORS relay for exactly GitHub's two device-flow calls, with the
// tokenmaxor App's client id pinned and GitHub's JSON passed back as it is.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import functions from '@azure/functions'
import { adapt } from '../../src/lib/adapter.js'
import { runHandler } from '../../src/lib/http.js'
import {
  ACCESS_TOKEN_URL, DEVICE_CODE_URL, DEVICE_GRANT, GITHUB_CLIENT_ID, RATE_LIMITS,
  allowedOrigin, createRateLimiter, handleGithubDevice,
} from '../../src/lib/github-device.js'

const { HttpRequest } = functions
const NOW = new Date('2026-10-05T09:00:00Z')
const PAGES = 'https://7-of-9.github.io'
const DEVICE_CODE = '3584d83530557fdd1f46af8289938c8ef79f9dc5'

/** A fetch that records each call and answers with the next canned response. */
function fakeGitHub(...answers) {
  const calls = []
  const fetch = async (url, init) => {
    calls.push({ url, init, params: Object.fromEntries(new URLSearchParams(init.body)) })
    const next = answers.shift() ?? { status: 200, body: {} }
    if (next instanceof Error) throw next
    return new Response(typeof next.body === 'string' ? next.body : JSON.stringify(next.body), { status: next.status })
  }
  return { fetch, calls }
}

const fresh = () => ({ code: createRateLimiter(RATE_LIMITS.code), token: createRateLimiter(RATE_LIMITS.token) })

const call = (step, { method = 'POST', origin = PAGES, body = null, headers = {}, ctx = {} } = {}) =>
  handleGithubDevice({ method, headers: { ...(origin ? { origin } : {}), ...headers }, query: {}, params: { step }, body, now: NOW },
    { limiters: fresh(), ...ctx })

test('the pinned client id is the collector\'s tokenmaxor App', () => {
  const release = JSON.parse(readFileSync(new URL('../../../collector/release.json', import.meta.url), 'utf8'))
  assert.equal(GITHUB_CLIENT_ID, release.githubClientId)
})

test('allowedOrigin: any https origin and a local preview, nothing else', () => {
  for (const ok of [PAGES, 'https://tokens.example.com', 'https://example.com:8443', 'http://localhost:8000', 'http://127.0.0.1:5173', 'http://localhost']) {
    assert.equal(allowedOrigin(ok), ok, ok)
  }
  for (const bad of [undefined, null, '', 'null', 'http://7-of-9.github.io', 'https://7-of-9.github.io/', 'https://a.github.io/path',
    'http://localhost.evil.com', 'ftp://example.com', 'https://', 'chrome-extension://abc', 'x'.repeat(400)]) {
    assert.equal(allowedOrigin(bad), null, String(bad))
  }
})

test('code: posts only the pinned client id to GitHub and returns its JSON as is, with CORS for the page', async () => {
  const github = { device_code: DEVICE_CODE, user_code: 'WDJB-MJHT', verification_uri: 'https://github.com/login/device', expires_in: 899, interval: 5 }
  const { fetch, calls } = fakeGitHub({ status: 200, body: github })
  // Whatever the page sends is ignored: no client id, scope or URL of its own reaches GitHub.
  const r = await call('code', { body: { client_id: 'Iv1.someoneelse', scope: 'repo', url: 'https://evil.example' }, ctx: { fetch } })
  assert.equal(r.status, 200)
  assert.deepEqual(r.jsonBody, github)
  assert.equal(calls.length, 1)
  assert.equal(calls[0].url, DEVICE_CODE_URL)
  assert.equal(calls[0].init.method, 'POST')
  assert.equal(calls[0].init.headers.Accept, 'application/json')
  assert.deepEqual(calls[0].params, { client_id: GITHUB_CLIENT_ID })
  assert.equal(r.headers['Access-Control-Allow-Origin'], PAGES)
  assert.equal(r.headers['Cache-Control'], 'no-store')
  assert.equal(r.headers.Vary, 'Origin')
  assert.equal(r.headers['Access-Control-Allow-Credentials'], undefined)
})

test('token: posts the client id, the device code and the grant type only; GitHub\'s answers pass through', async () => {
  for (const [status, body] of [
    [200, { access_token: 'ghu_example', token_type: 'bearer', scope: '' }],
    [200, { error: 'authorization_pending', error_description: 'The authorization request is still pending.' }],
    [200, { error: 'slow_down', interval: 10 }],
    [200, { error: 'expired_token' }],
    [200, { error: 'access_denied' }],
    [400, { error: 'bad_request' }],
  ]) {
    const { fetch, calls } = fakeGitHub({ status, body })
    const r = await call('token', { body: { device_code: DEVICE_CODE, client_id: 'other', grant_type: 'password', redirect_uri: 'x' }, ctx: { fetch } })
    assert.equal(r.status, status)
    assert.deepEqual(r.jsonBody, body)
    assert.equal(calls[0].url, ACCESS_TOKEN_URL)
    assert.deepEqual(calls[0].params, { client_id: GITHUB_CLIENT_ID, device_code: DEVICE_CODE, grant_type: DEVICE_GRANT })
    assert.equal(r.headers['Access-Control-Allow-Origin'], PAGES)
  }
})

test('token: a missing or malformed device code never reaches GitHub', async () => {
  for (const body of [null, {}, [], { device_code: 42 }, { device_code: '' }, { device_code: 'abc' },
    { device_code: `${DEVICE_CODE}&client_id=x` }, { device_code: 'a'.repeat(200) }]) {
    const { fetch, calls } = fakeGitHub()
    const r = await call('token', { body, ctx: { fetch } })
    assert.equal(r.status, 400, JSON.stringify(body))
    assert.equal(calls.length, 0)
    assert.equal(r.headers['Access-Control-Allow-Origin'], PAGES)
  }
})

test('preflight, other methods and steps, and foreign origins', async () => {
  const { fetch, calls } = fakeGitHub()
  const pre = await call('token', { method: 'OPTIONS', origin: 'https://tokens.example.com', ctx: { fetch } })
  assert.equal(pre.status, 204)
  assert.equal(pre.headers['Access-Control-Allow-Origin'], 'https://tokens.example.com')
  assert.match(pre.headers['Access-Control-Allow-Methods'], /POST/)
  assert.match(pre.headers['Access-Control-Allow-Headers'], /Content-Type/)
  assert.equal((await call('code', { method: 'GET', ctx: { fetch } })).status, 405)
  assert.equal((await call('user', { ctx: { fetch } })).status, 404)
  assert.equal((await call('../../login/oauth/authorize', { ctx: { fetch } })).status, 404)
  const foreign = await call('code', { origin: 'http://evil.example', ctx: { fetch } })
  assert.equal(foreign.status, 403)
  assert.equal(foreign.headers['Access-Control-Allow-Origin'], undefined)
  // No Origin (curl, a server): allowed, nothing to tell a browser.
  const bare = await call('code', { origin: null, ctx: { fetch } })
  assert.equal(bare.status, 200)
  assert.equal(bare.headers['Access-Control-Allow-Origin'], undefined)
  assert.equal(calls.length, 1)
})

test('GitHub unreachable or not answering JSON: 502, readable by the page', async () => {
  const down = await call('code', { ctx: { fetch: fakeGitHub(new TypeError('fetch failed')).fetch } })
  assert.equal(down.status, 502)
  assert.equal(down.jsonBody.error, 'github_unreachable')
  assert.equal(down.headers['Access-Control-Allow-Origin'], PAGES)
  const html = await call('code', { ctx: { fetch: fakeGitHub({ status: 503, body: '<html>unicorn</html>' }).fetch } })
  assert.equal(html.status, 502)
  assert.equal(html.jsonBody.error, 'github_bad_answer')
})

test('rate limit: per address and step, with Retry-After; the window resets', async () => {
  const limiters = fresh()
  const { fetch, calls } = fakeGitHub()
  const ask = (ip, step = 'code', now = NOW) => handleGithubDevice({ method: 'POST', headers: { origin: PAGES, 'x-forwarded-for': `${ip}, 10.0.0.1` },
    query: {}, params: { step }, body: { device_code: DEVICE_CODE }, now }, { fetch, limiters })
  for (let i = 0; i < RATE_LIMITS.code.limit; i++) assert.equal((await ask('203.0.113.7')).status, 200)
  const over = await ask('203.0.113.7')
  assert.equal(over.status, 429)
  assert.equal(over.jsonBody.error, 'rate_limited')
  assert.ok(Number(over.headers['Retry-After']) > 0)
  assert.equal(over.headers['Access-Control-Allow-Origin'], PAGES)
  assert.equal(calls.length, RATE_LIMITS.code.limit)
  // Another address, and polling, have their own allowance.
  assert.equal((await ask('198.51.100.2')).status, 200)
  assert.equal((await ask('203.0.113.7', 'token')).status, 200)
  // A new window.
  assert.equal((await ask('203.0.113.7', 'code', new Date(NOW.getTime() + RATE_LIMITS.code.windowMs))).status, 200)
})

test('through runHandler and the Functions adapter: a text/plain JSON body (no preflight) is read', async () => {
  const { fetch, calls } = fakeGitHub({ status: 200, body: { error: 'authorization_pending' } })
  const r = await runHandler(handleGithubDevice, {
    method: 'POST', headers: { origin: PAGES, 'content-type': 'text/plain;charset=UTF-8' }, query: {}, params: { step: 'token' },
    readBody: async () => JSON.stringify({ device_code: DEVICE_CODE }),
  }, { fetch, limiters: fresh() })
  assert.equal(r.status, 200)
  assert.equal(calls[0].params.device_code, DEVICE_CODE)
  // The adapter hands the route's {step} through as params.
  const pre = await adapt(handleGithubDevice)(new HttpRequest({ method: 'OPTIONS', url: 'http://localhost/api/github/device/code', headers: { Origin: PAGES }, params: { step: 'code' } }))
  assert.equal(pre.status, 204)
  assert.equal(pre.headers['Access-Control-Allow-Origin'], PAGES)
})
