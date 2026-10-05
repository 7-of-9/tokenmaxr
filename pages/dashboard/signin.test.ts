// The GitHub sign-in (signin.ts): the device flow through the relay, the fleet key read with the user's token, the
// owner key derived exactly as the collector derives it, checked on the owner files, and the failures the owner
// reads. Run: node --experimental-strip-types --no-warnings pages/dashboard/signin.test.ts
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { githubSource } from './githubSource.ts'
import { createOwnerStore, fromBase64, memoryVault } from './owner.ts'
import {
  createDeviceSignIn, deriveOwnerKey, FLEET_VARIABLE, GITHUB_API, pagesRepository, parseDashboardConfig, parseFleetKey,
  parseRepository, pollDeviceToken, readFleetKey, requestDeviceCode, SignInError, signInMessage, type DeviceCode, type SignInState,
} from './signin.ts'
// @ts-expect-error -- plain JS module without types (the test vector generator).
import { VECTOR_PATH } from './testdata/owner-vector.mjs'

const vector = JSON.parse(readFileSync(VECTOR_PATH, 'utf8')) as { fleetKey: string; ownerKey: string; machine: string; file: unknown }
const RELAY = 'https://relay.test/api/github/device'
const REPO = '7-of-9/tokenmaxr-usage'
const TOKEN = 'ghu_test_token_0123456789'
const VARIABLE_PATH = `/repos/${REPO}/actions/variables/${FLEET_VARIABLE}`

type Answer = { status?: number; body: unknown } | Error
interface Call { url: string; init?: RequestInit; body: unknown }

/** A fetch answering by URL (a list is answered in turn, the last repeating). */
function fakeFetch(routes: Record<string, Answer | Answer[]>, calls: Call[] = []) {
  const fetch = async (input: string, init?: RequestInit) => {
    let body: unknown = null
    try {
      body = typeof init?.body === 'string' ? JSON.parse(init.body) : null
    } catch {
      body = init?.body
    }
    calls.push({ url: input, init, body })
    const route = routes[input]
    const answer = Array.isArray(route) ? (route.length > 1 ? route.shift()! : route[0]) : route
    if (!answer) return new Response('{"message":"Not Found"}', { status: 404 })
    if (answer instanceof Error) throw answer
    return new Response(JSON.stringify(answer.body), { status: answer.status ?? 200 })
  }
  return { fetch, calls }
}

/** No wait, but a turn of the event loop, so a test's timers still run beside a poll that never ends. */
const noSleep = async (_ms: number, signal?: AbortSignal) => {
  if (signal?.aborted) throw signal.reason
  await new Promise(resolve => setTimeout(resolve, 0))
  if (signal?.aborted) throw signal.reason
}

const CODE_ANSWER = { device_code: 'dc_0123456789abcdef', user_code: 'WDJB-MJHT', verification_uri: 'https://github.com/login/device', expires_in: 900, interval: 5 }

test('the owner key from the fleet key is the collector\'s (the shared test vector)', async () => {
  const fleet = parseFleetKey(vector.fleetKey)!
  assert.equal(fleet.length, 32)
  assert.deepEqual(await deriveOwnerKey(fleet), fromBase64(vector.ownerKey))
  // The variable as the collector writes it (Go StdEncoding), with stray space trimmed as Join trims it.
  assert.deepEqual(parseFleetKey(`  ${vector.fleetKey}\n`), fleet)
  for (const bad of [null, 42, '', 'not base64', vector.fleetKey.slice(0, -4), btoa('x'.repeat(31)), btoa('x'.repeat(33)),
    vector.fleetKey.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')]) {
    assert.equal(parseFleetKey(bad), null, String(bad))
  }
})

test('the repository from the Pages address or tokenmaxr.json', () => {
  assert.equal(pagesRepository('7-of-9.github.io', '/tokenmaxr-usage/'), '7-of-9/tokenmaxr-usage')
  assert.equal(pagesRepository('7-of-9.GitHub.io', '/tokenmaxr-usage/index.html'), '7-of-9/tokenmaxr-usage')
  assert.equal(pagesRepository('someone.github.io', '/'), 'someone/someone.github.io')
  assert.equal(pagesRepository('someone.github.io', '/index.html'), 'someone/someone.github.io')
  assert.equal(pagesRepository('tokens.example.com', '/'), null)
  assert.equal(pagesRepository('github.io', '/x/'), null)
  assert.equal(parseRepository('octo/usage'), 'octo/usage')
  for (const bad of ['octo', 'octo/', '/usage', 'octo/usage/x', 'oc to/usage', 'octo/../x', 42, null]) assert.equal(parseRepository(bad), null, String(bad))
  assert.deepEqual(parseDashboardConfig({ tokenmaxr: 1, title: 'x' }), { background: false, repository: null })
  assert.deepEqual(parseDashboardConfig({ background: true, repository: 'octo/usage' }), { background: true, repository: 'octo/usage' })
  assert.deepEqual(parseDashboardConfig({ background: 'yes', repository: 'nope' }), { background: false, repository: null })
  assert.deepEqual(parseDashboardConfig(null), { background: false, repository: null })
})

test('the device code through the relay: a simple request (no preflight), no credentials, GitHub\'s page only', async () => {
  const { fetch, calls } = fakeFetch({ [`${RELAY}/code`]: { body: CODE_ANSWER } })
  const code = await requestDeviceCode(RELAY, fetch, () => 1_000)
  assert.deepEqual(code, { deviceCode: CODE_ANSWER.device_code, userCode: 'WDJB-MJHT', verificationUri: 'https://github.com/login/device', expiresAt: 901_000, interval: 5 })
  const headers = calls[0].init!.headers as Record<string, string>
  assert.equal(calls[0].init!.method, 'POST')
  assert.match(headers['Content-Type'], /^text\/plain/)
  assert.equal(calls[0].init!.credentials, 'omit')
  const odd = await requestDeviceCode(RELAY, fakeFetch({ [`${RELAY}/code`]: { body: { ...CODE_ANSWER, verification_uri: 'https://evil.example/' } } }).fetch, () => 0)
  assert.equal(odd.verificationUri, 'https://github.com/login/device')
  await assert.rejects(requestDeviceCode(RELAY, fakeFetch({ [`${RELAY}/code`]: new TypeError('offline') }).fetch, () => 0), (e: SignInError) => e.kind === 'network')
  await assert.rejects(requestDeviceCode(RELAY, fakeFetch({ [`${RELAY}/code`]: { status: 429, body: { error: 'rate_limited' } } }).fetch, () => 0), (e: SignInError) => e.kind === 'rate-limited')
  await assert.rejects(requestDeviceCode(RELAY, fakeFetch({ [`${RELAY}/code`]: { status: 502, body: { error: 'github_unreachable' } } }).fetch, () => 0), (e: SignInError) => e.kind === 'network')
  await assert.rejects(requestDeviceCode(RELAY, fakeFetch({ [`${RELAY}/code`]: { body: { error: 'device_flow_disabled', error_description: 'Device Flow must be explicitly enabled' } } }).fetch, () => 0),
    (e: SignInError) => e.kind === 'github' && /Device Flow/.test(signInMessage(e)))
})

const code = (over: Partial<DeviceCode> = {}): DeviceCode => ({ deviceCode: 'dc_0123456789abcdef', userCode: 'WDJB-MJHT', verificationUri: 'https://github.com/login/device', expiresAt: 900_000, interval: 5, ...over })

test('polling: waits the interval, slows down when asked, and ends on a token, expiry or denial', async () => {
  const waits: number[] = []
  const sleep = async (ms: number) => { waits.push(ms) }
  const { fetch, calls } = fakeFetch({ [`${RELAY}/token`]: [
    { body: { error: 'authorization_pending' } },
    { body: { error: 'slow_down', interval: 10 } },
    { body: { error: 'authorization_pending' } },
    { body: { error: 'slow_down' } },
    new TypeError('offline for a moment'),
    { body: { access_token: TOKEN, token_type: 'bearer' } },
  ] })
  assert.equal(await pollDeviceToken(RELAY, code(), { fetch, now: () => 0, sleep }), TOKEN)
  assert.deepEqual(waits, [5000, 5000, 10000, 10000, 15000, 15000])
  assert.deepEqual(calls.map(c => c.body), Array(6).fill({ device_code: 'dc_0123456789abcdef' }))

  const poll = (answer: Answer, now = () => 0) => pollDeviceToken(RELAY, code(), { fetch: fakeFetch({ [`${RELAY}/token`]: answer }).fetch, now, sleep: noSleep })
  await assert.rejects(poll({ body: { error: 'expired_token' } }), (e: SignInError) => e.kind === 'expired')
  await assert.rejects(poll({ body: { error: 'access_denied' } }), (e: SignInError) => e.kind === 'denied')
  await assert.rejects(poll({ body: { error: 'authorization_pending' } }, () => 900_000), (e: SignInError) => e.kind === 'expired')
  await assert.rejects(poll({ body: { error: 'incorrect_device_code' } }), (e: SignInError) => e.kind === 'github')
  await assert.rejects(poll(new TypeError('offline')), (e: SignInError) => e.kind === 'network')
  // Cancelled: the abort, not a sign-in error.
  const controller = new AbortController()
  controller.abort(new Error('cancelled'))
  await assert.rejects(pollDeviceToken(RELAY, code(), { fetch, now: () => 0, sleep: noSleep, signal: controller.signal }), /cancelled/)
})

/** api.github.com as one account sees it. */
function github({ variable, installed = 'selected', repos = [REPO], permissions = { admin: true, push: true } }: {
  variable: Answer; installed?: 'all' | 'selected' | 'none'; repos?: string[]; permissions?: Record<string, boolean> | null
}) {
  return {
    [`${GITHUB_API}${VARIABLE_PATH}`]: variable,
    [`${GITHUB_API}/user/installations?per_page=100`]: { body: { total_count: installed === 'none' ? 0 : 1, installations: installed === 'none' ? [] : [{ id: 77, account: { login: '7-of-9' }, repository_selection: installed }] } },
    [`${GITHUB_API}/user/installations/77/repositories?per_page=100`]: { body: { repositories: repos.map(full_name => ({ full_name })) } },
    [`${GITHUB_API}/repos/${REPO}`]: { body: { full_name: REPO, ...(permissions ? { permissions } : {}) } },
  } as Record<string, Answer>
}

test('the fleet key with the user\'s token, and why it could not be read', async () => {
  const ok = fakeFetch(github({ variable: { body: { name: FLEET_VARIABLE, value: vector.fleetKey } } }))
  assert.deepEqual(await readFleetKey(TOKEN, REPO, ok.fetch), parseFleetKey(vector.fleetKey))
  const headers = ok.calls[0].init!.headers as Record<string, string>
  assert.equal(headers.Authorization, `Bearer ${TOKEN}`)
  assert.equal(ok.calls[0].init!.credentials, 'omit')

  const kind = async (routes: Record<string, Answer>) => {
    try {
      await readFleetKey(TOKEN, REPO, fakeFetch(routes).fetch)
    } catch (err) {
      return err instanceof SignInError ? [err.kind, signInMessage(err)] : ['?', String(err)]
    }
    return ['none', '']
  }
  const notFound = { status: 404, body: { message: 'Not Found' } }
  const forbidden = { status: 403, body: { message: 'Resource not accessible by integration' } }
  assert.deepEqual(await kind(github({ variable: forbidden, installed: 'none' })), ['not-installed', 'tokenmaxor isn\'t installed with access to 7-of-9/tokenmaxr-usage.'])
  assert.deepEqual(await kind(github({ variable: notFound, repos: ['7-of-9/other'] })), ['not-installed', 'tokenmaxor isn\'t installed with access to 7-of-9/tokenmaxr-usage.'])
  assert.deepEqual(await kind(github({ variable: notFound, installed: 'none', permissions: { admin: false, push: false, pull: true } })), ['cant-read', 'This GitHub account can\'t read 7-of-9/tokenmaxr-usage.'])
  assert.deepEqual(await kind(github({ variable: forbidden, installed: 'all', permissions: { pull: true } })), ['cant-read', 'This GitHub account can\'t read 7-of-9/tokenmaxr-usage.'])
  assert.deepEqual((await kind(github({ variable: notFound })))[0], 'no-key')
  assert.deepEqual((await kind(github({ variable: { body: { value: 'garbage' } } })))[0], 'no-key')
  assert.deepEqual((await kind({ [`${GITHUB_API}${VARIABLE_PATH}`]: new TypeError('offline') }))[0], 'network')
  assert.deepEqual((await kind({ [`${GITHUB_API}${VARIABLE_PATH}`]: { status: 401, body: {} } }))[0], 'github')
})

/** The published files, with the vector's owner.json (or none). */
function pagesFiles(owner: boolean): Record<string, Answer> {
  return {
    'data/index.json': { body: { schema: 3, machines: [{ id: vector.machine, files: owner ? ['meta.json', 'owner.json'] : ['meta.json'] }] } },
    ...(owner ? { [`data/machines/${vector.machine}/owner.json`]: { body: vector.file } } : {}),
  }
}

async function settle(signIn: { state(): SignInState; subscribe(l: () => void): () => void }) {
  return new Promise<SignInState>(resolve => {
    const done = (s: SignInState) => s.step === 'done' || s.step === 'error'
    if (done(signIn.state())) return resolve(signIn.state())
    const stop = signIn.subscribe(() => {
      if (done(signIn.state())) {
        stop()
        resolve(signIn.state())
      }
    })
  })
}

function flow(routes: Record<string, Answer | Answer[]>, repository: string | null = REPO) {
  const { fetch, calls } = fakeFetch({
    [`${RELAY}/code`]: { body: CODE_ANSWER },
    [`${RELAY}/token`]: [{ body: { error: 'authorization_pending' } }, { body: { access_token: TOKEN } }],
    ...routes,
  })
  const store = createOwnerStore({ name: 'tokenmaxr.owner.v1:/tokenmaxr-usage/', vault: memoryVault() })
  const source = githubSource({ fetch: fetch as unknown as typeof globalThis.fetch, ownerKey: store.ready })
  const states: string[] = []
  let accepted: Uint8Array | null = null
  const signIn = createDeviceSignIn({
    relay: RELAY,
    repository: async () => repository,
    check: (key, signal) => source.checkOwnerKey(key, signal),
    accept: async bytes => {
      accepted = Uint8Array.from(bytes)
      await store.unlock(bytes)
    },
    fetch,
    now: () => 0,
    sleep: noSleep,
  })
  signIn.subscribe(() => states.push(signIn.state().step))
  return { signIn, store, source, calls, states, accepted: () => accepted }
}

test('the whole sign-in: code, wait, the key checked on owner.json and kept; the token reaches only GitHub', async () => {
  const { signIn, store, source, calls, states, accepted } = flow({ ...github({ variable: { body: { value: vector.fleetKey } } }), ...pagesFiles(true) })
  signIn.start()
  const end = await settle(signIn)
  assert.equal(end.step, 'done')
  assert.deepEqual(states, ['requesting', 'code', 'checking', 'done'])
  assert.deepEqual(accepted(), fromBase64(vector.ownerKey))
  assert.equal(store.state().status, 'unlocked')
  // The kept key opens the owner files: the Agents page reads them.
  assert.ok((await source.fetchLimits(new AbortController().signal)).length > 0)
  // The token went to api.github.com and nowhere else; the fleet and owner keys went nowhere.
  for (const call of calls) {
    const sent = JSON.stringify({ url: call.url, init: call.init })
    if (!call.url.startsWith(GITHUB_API)) assert.ok(!sent.includes(TOKEN), `${call.url} carries the token`)
    assert.ok(!sent.includes(vector.fleetKey) && !sent.includes(vector.ownerKey), `${call.url} carries a key`)
  }
})

test('a key that opens none of the owner files is not kept; no owner files yet: kept', async () => {
  const other = btoa(String.fromCharCode(...new Uint8Array(32).fill(9)))
  const wrong = flow({ ...github({ variable: { body: { value: other } } }), ...pagesFiles(true) })
  wrong.signIn.start()
  const end = await settle(wrong.signIn)
  assert.equal(end.step, 'error')
  assert.equal(end.step === 'error' && end.error.kind, 'wrong-key')
  assert.equal(wrong.store.state().status === 'unlocked', false)
  assert.equal(wrong.accepted(), null)

  const none = flow({ ...github({ variable: { body: { value: vector.fleetKey } } }), ...pagesFiles(false) })
  none.signIn.start()
  assert.equal((await settle(none.signIn)).step, 'done')
})

test('expired and denied end in errors the panel offers to retry; cancel stops polling', async () => {
  for (const [error, kind] of [['expired_token', 'expired'], ['access_denied', 'denied']]) {
    const { signIn } = flow({ [`${RELAY}/token`]: { body: { error } } })
    signIn.start()
    const end = await settle(signIn)
    assert.equal(end.step === 'error' && end.error.kind, kind)
  }
  const pending = flow({ [`${RELAY}/token`]: { body: { error: 'authorization_pending' } } })
  pending.signIn.start()
  await new Promise(resolve => setTimeout(resolve, 5))
  assert.equal(pending.signIn.state().step, 'code')
  pending.signIn.opened()
  assert.deepEqual(pending.signIn.state().step === 'code' && pending.signIn.state(), { step: 'code', code: code({ deviceCode: CODE_ANSWER.device_code, expiresAt: 900_000 }), opened: true })
  pending.signIn.cancel()
  await new Promise(resolve => setTimeout(resolve, 5))
  assert.equal(pending.signIn.state().step, 'idle')
})

test('a custom domain naming no repository: the account\'s repository whose key opens the owner files', async () => {
  const other = '7-of-9/notes'
  const otherKey = btoa(String.fromCharCode(...new Uint8Array(32).fill(3)))
  const routes = {
    ...github({ variable: { body: { value: vector.fleetKey } }, repos: [other, REPO] }),
    [`${GITHUB_API}/repos/${other}/actions/variables/${FLEET_VARIABLE}`]: { body: { value: otherKey } },
    ...pagesFiles(true),
  }
  const { signIn, accepted } = flow(routes, null)
  signIn.start()
  assert.equal((await settle(signIn)).step, 'done')
  assert.deepEqual(accepted(), fromBase64(vector.ownerKey))

  const nothing = flow({ ...github({ variable: { status: 404, body: {} }, installed: 'none' }) }, null)
  nothing.signIn.start()
  const end = await settle(nothing.signIn)
  assert.equal(end.step === 'error' && end.error.kind, 'no-repo')
})

console.log('signin.ts: device flow, fleet key, owner key and failures')
