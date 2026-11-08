import test from 'node:test'
import assert from 'node:assert/strict'
import { handleLimitProjects, weeklyRange } from '../../src/lib/limit-projects.js'
import { memoryStore } from '../../src/lib/tables.js'
import { encryptJson } from '../../src/lib/crypto.js'
import { accountHash } from '../../src/lib/ids.js'
import { upsertLimits } from '../../src/lib/limits.js'
import { upsertPrompt } from '../../src/lib/ingest.js'
import { addMonths } from '../../src/lib/months.js'
import { localDate } from '../../src/lib/validate.js'
import { validateUsage } from '../../src/lib/validate.js'
import { mergeEvent, rowToEvent, storedFields } from '../../src/lib/merge.js'
import { K, ACCT, SESSION, principal, usage, activity, prompt } from '../fixtures.js'
import { createHarness } from '../harness.js'

const NOW = new Date('2026-10-30T02:00:00.000Z')
const RESET = '2026-11-03T15:00:00.000Z'
const SINCE = '2026-10-27T15:00:00.000Z'
const ID = 'a'.repeat(32)
const KEY = Buffer.alloc(32, 0x33)
const OWNER = { 'x-ms-client-principal': principal('github', 'owner', ['authenticated', 'owner']) }
const ws = path => 'w_' + accountHash(K, 'workspace', path).slice(2)
const meter = overrides => ({ id: ID, provider: 'anthropic', source: 'claude-code', acct: ACCT.anthropic, acctQ: 'recorded',
  window: 'week', usedPercent: 72, resetsAt: RESET, observedAt: NOW.toISOString(), ...overrides })

async function fixture(overrides = {}) {
  const store = memoryStore()
  const ctx = { store, env: { PROMPT_ENC_KEY: KEY.toString('base64') } }
  await upsertLimits(store, f => f(), [meter(overrides)])
  const event = async (e, day = localDate(Date.parse(e.ts), e.tzOffsetMin || 0)) => {
    await store.table('events').upsertMerge({ partitionKey: day, rowKey: e.id, kind: 'hasUsage' in e ? 'activity' : 'usage', ...e })
  }
  const addPrompt = async (name, over = {}) => {
    const p = prompt(name, { ts: '2026-10-28T09:00:00.000Z', tzOffsetMin: 540, acctQ: 'recorded', text: 'PRIVATE PROMPT CONTENT', ...over })
    p.tsMs = Date.parse(p.ts)
    const month = await upsertPrompt(store, f => f(), KEY, p)
    await addMonths(store, f => f(), [month])
    return p
  }
  const read = (over = {}) => handleLimitProjects({ method: 'GET', params: { id: ID }, headers: OWNER, now: NOW, ...over }, ctx)
  return { store, ctx, event, addPrompt, read }
}

test('weekly window uses the precise reset, including repeated and exact-boundary rollovers', () => {
  assert.deepEqual(weeklyRange(RESET, NOW.getTime()), { since: SINCE, resetsAt: RESET, projected: false })
  assert.deepEqual(weeklyRange('2026-10-13T15:00:00Z', NOW.getTime()), { since: SINCE, resetsAt: RESET, projected: true })
  assert.equal(weeklyRange(SINCE, Date.parse(SINCE)).since, SINCE)
  assert.equal(weeklyRange('2026-11-03T15:00:00', NOW.getTime()), null)
  assert.equal(weeklyRange(null, NOW.getTime()), null)
  assert.equal(weeklyRange('2027-11-03T15:00:00Z', NOW.getTime()), null)
})

test('private breakdown requires the owner; method, key and missing meter errors are uncached', async () => {
  const f = await fixture()
  const visitor = { 'x-ms-client-principal': principal('github', 'visitor') }
  for (const [over, status] of [[{ headers: {} }, 401], [{ headers: visitor }, 403], [{ method: 'POST' }, 405],
    [{ params: { id: '../secret' } }, 400], [{ params: { id: 'f'.repeat(32) } }, 404]]) {
    const r = await f.read(over)
    assert.equal(r.status, status)
    assert.equal(r.headers['Cache-Control'], 'private, no-store')
    assert.equal('projects' in r.jsonBody, false)
  }
})

test('exact timestamp/account filtering, sorted projects, cache accounting and uncertain attribution', async () => {
  const f = await fixture()
  await f.store.table('accounts').create({ partitionKey: 'fleet', rowKey: 'kenc', enc: encryptJson(KEY, 'fleet-k', K.toString('base64')) })
  await f.addPrompt('alpha', { workspace: '/example/projects/alpha' })
  await f.addPrompt('beta', { workspace: '/example/projects/beta', session: 's_2222222222222222' })
  const e = (id, over) => usage(id, { ts: '2026-10-28T00:30:00.000Z', acctQ: 'recorded',
    ws: ws('/example/projects/alpha'), tzOffsetMin: -720, in: 100, cacheW: 50, cacheR: 800, out: 50, cacheW1h: 50, reasoning: 25, ...over })
  await f.event(e('alpha-at-reset', { ts: SINCE, tzOffsetMin: 840 }))
  await f.event(e('alpha-later', {}))
  await f.event(e('beta', { ws: ws('/example/projects/beta'), in: 10, cacheW: 0, cacheR: 200, out: 40, q: 'estimated' }))
  await f.event(e('unmapped-project', { ws: 'w_1111111111111111', in: 0, cacheW: 0, cacheR: 0, out: 250 }))
  await f.event(e('before-reset', { ts: '2026-10-27T14:59:59.999Z' }))
  await f.event(e('future', { ts: '2026-10-30T02:00:00.001Z' }))
  await f.event(e('wrong-account', { acct: 'a_1111111111111111' }))
  await f.event(e('wrong-provider', { provider: 'openai' }))
  await f.event(e('weak-account', { acctQ: 'inferred' }))
  await f.event(activity('activity-is-not-usage', { ts: SINCE, ws: ws('/example/projects/alpha') }))
  const r = await f.read()
  assert.equal(r.status, 200)
  const b = r.jsonBody
  assert.equal(b.since, SINCE)
  assert.equal(b.until, NOW.toISOString())
  assert.deepEqual(b.projects.map(p => [p.name, p.tokens, p.percent]), [['alpha', 2000, 80], ['beta', 250, 10], ['Unassigned project', 250, 10]])
  assert.deepEqual(b.totals, { tokens: 2500, exactTokens: 2250, estimatedTokens: 250, calls: 4, uncertainTokens: 1000 })
  assert.equal(JSON.stringify(b).includes('PRIVATE PROMPT CONTENT'), false)
  assert.equal(JSON.stringify(b).includes(KEY.toString('base64')), false)
})

test('machine-created fleet names an ongoing project using its exact pre-reset prompt/activity pair', async () => {
  const f = await fixture()
  const oldTs = '2026-10-24T14:00:00.000Z'
  await f.addPrompt('ongoing', { ts: oldTs, workspace: 'C:\\Users\\Someone\\src\\research' })
  await f.event(activity('ongoing', { ts: oldTs, ws: 'w_0123456789abcdef', session: SESSION }))
  await f.event(usage('research', { ts: '2026-10-29T14:00:00Z', ws: 'w_0123456789abcdef' }))
  const b = (await f.read()).jsonBody
  assert.equal(b.projects[0].name, 'research')
  assert.equal(b.projects[0].tokens, 1170)
  assert.equal(b.projects[0].percent, 100)
  assert.equal(await f.store.table('accounts').get('fleet', 'kenc'), null, 'GET never creates escrow')
})

test('a session changing workspace cannot silently assign unknown tags to its last project', async () => {
  const f = await fixture()
  const a = await f.addPrompt('first', { workspace: '/example/alpha', ts: '2026-10-28T01:00:00Z' })
  const b = await f.addPrompt('second', { workspace: '/example/beta', ts: '2026-10-28T02:00:00Z' })
  for (const [p, tag] of [[a, 'w_1111111111111111'], [b, 'w_2222222222222222']]) {
    await f.event(activity(p.id, { ts: p.ts, ws: tag }))
    await f.event(usage(p.id, { ts: '2026-10-29T01:00:00Z', ws: tag }))
  }
  await f.event(usage('moved-without-a-prompt', { ts: '2026-10-29T02:00:00Z', ws: 'w_3333333333333333' }))
  const rows = (await f.read()).jsonBody.projects
  assert.deepEqual(rows.map(r => r.name), ['alpha', 'beta', 'Unassigned project'])
  assert.ok(rows.every(r => r.tokens === 1170))
})

test('workspace aliases and child directories use the same project grouping as the archive', async () => {
  const f = await fixture()
  for (const [name, path, tag, ts] of [
    ['root', 'C:\\Users\\Someone\\src\\research', 'w_1111111111111111', '2026-10-28T01:00:00Z'],
    ['child', 'c:\\users\\someone\\src\\research\\case', 'w_2222222222222222', '2026-10-28T02:00:00Z'],
  ]) {
    await f.addPrompt(name, { workspace: path, ts })
    await f.event(activity(name, { ts, ws: tag }))
    await f.event(usage(name, { ts: '2026-10-29T02:00:00Z', ws: tag }))
  }
  const rows = (await f.read()).jsonBody.projects
  assert.equal(rows.length, 1)
  assert.equal(rows[0].name, 'research')
  assert.equal(rows[0].tokens, 2340)
})

test('missing reset, unreliable identity and shared-organisation identity stay explicitly unavailable', async () => {
  for (const [over, reason] of [[{ resetsAt: null }, 'reset-unavailable'], [{ acct: '' }, 'account-unavailable'],
    [{ acctQ: 'inferred' }, 'account-unavailable'], [{ scope: 'Fable' }, 'reset-unavailable']]) {
    const f = await fixture(over)
    assert.equal((await f.read()).jsonBody.reason, reason)
  }
  const f = await fixture({ org: 'a_1111111111111111', orgKind: 'personal' })
  await upsertLimits(f.store, f => f(), [meter({ id: 'b'.repeat(32), org: 'a_2222222222222222', orgKind: 'team' })])
  assert.equal((await f.read()).jsonBody.reason, 'organisation-unavailable')
})

test('a new week has zero recorded tokens instead of carrying forward the previous week', async () => {
  const f = await fixture({ resetsAt: SINCE })
  await f.event(usage('old-week', { ts: '2026-10-27T14:00:00Z' }))
  const b = (await f.read()).jsonBody
  assert.equal(b.projected, true)
  assert.equal(b.since, SINCE)
  assert.equal(b.totals.tokens, 0)
  assert.deepEqual(b.projects, [])
})

test('the HTTP route accepts a persistent owner session and rejects it after logout', async t => {
  const now = new Date()
  const f = await fixture({ resetsAt: new Date(now.getTime() + 2 * 86_400_000).toISOString(), observedAt: now.toISOString() })
  const server = createHarness({ ...f.ctx, quiet: true })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  t.after(() => new Promise(resolve => server.close(resolve)))
  const origin = `http://127.0.0.1:${server.address().port}`
  const path = origin + `/api/limits/${ID}/projects?range=30m`
  assert.equal((await fetch(path)).status, 401)
  const session = await fetch(origin + '/api/session', { method: 'POST', headers: OWNER })
  assert.equal(session.status, 200)
  const cookie = session.headers.get('set-cookie').split(';')[0]
  const response = await fetch(path, { headers: { Cookie: cookie } })
  assert.equal(response.status, 200)
  assert.equal(response.headers.get('cache-control'), 'private, no-store')
  const body = await response.json()
  assert.equal(body.available, true)
  assert.equal(body.range, '30m')
  assert.equal(Date.parse(body.until) - Date.parse(body.since), 30 * 60_000)
  await fetch(origin + '/api/session/logout', { headers: { Cookie: cookie }, redirect: 'manual' })
  assert.equal((await fetch(path, { headers: { Cookie: cookie } })).status, 401)
})

test('every rolling preset filters exact boundaries across local dates and all five providers without resets', async () => {
  const ranges = { '30m': 30, '1h': 60, '3h': 180, '6h': 360, '12h': 720, '24h': 1440, '3d': 4320, '7d': 10080 }
  for (const [provider, source] of [['anthropic', 'claude-code'], ['openai', 'codex'], ['xai', 'grok-cli'], ['cursor', 'cursor'], ['google', 'gemini-cli']]) {
    const f = await fixture({ provider, source, window: 'plan', resetsAt: null })
    for (const [range, minutes] of Object.entries(ranges)) {
      const cutoff = NOW.getTime() - minutes * 60_000
      // Isolate this preset's records by using an independent account.
      const acct = 'a_' + String(minutes).padStart(16, '0')
      await upsertLimits(f.store, x => x(), [meter({ provider, source, acct, window: 'plan', resetsAt: null,
        observedAt: new Date(NOW.getTime() + minutes).toISOString() })])
      for (const [key, at, amount, tz] of [
        ['before', cutoff - 1, 1000, -720], ['start', cutoff, 100, 840],
        ['last', NOW.getTime() - 1, 200, -720], ['end', NOW.getTime(), 2000, 0],
      ]) {
        const e = usage(`${provider}-${range}-${key}`, { provider, source, acct, ts: new Date(at).toISOString(), tsQ: 'event',
          tzOffsetMin: tz, in: amount, out: 0, cacheR: 0, cacheW: 0 })
        await f.event(e)
        if (key === 'last') await f.event(e, new Date(at).toISOString().slice(0, 10)) // A second machine's local date.
      }
      const b = (await f.read({ query: { range } })).jsonBody
      assert.equal(b.available, true, `${provider}/${range}`)
      assert.equal(b.totals.tokens, 300, `${provider}/${range}`)
      assert.equal(b.projects[0].percent, 100)
      assert.equal(b.since, new Date(cutoff).toISOString())
      assert.equal(b.until, NOW.toISOString())
      assert.equal(b.resetsAt, null)
    }
  }
})

test('shorter ranges recalculate project shares, include unassigned tokens and cross resets independently', async () => {
  const f = await fixture({ resetsAt: new Date(NOW.getTime() + 6 * 86_400_000).toISOString() })
  await f.store.table('accounts').create({ partitionKey: 'fleet', rowKey: 'kenc', enc: encryptJson(KEY, 'fleet-k', K.toString('base64')) })
  await f.addPrompt('alpha', { workspace: '/example/alpha' })
  await f.addPrompt('beta', { workspace: '/example/beta' })
  for (const [key, ageHours, tokens, path] of [['a', 0.25, 20, '/example/alpha'], ['b', 0.4, 30, '/example/beta'],
    ['c', 2, 50, '/example/alpha'], ['d', 48, 100, '/example/beta'], ['e', 0.1, 50, '']]) {
    await f.event(usage(key, { ts: new Date(NOW.getTime() - ageHours * 3_600_000).toISOString(),
      in: tokens, cacheW: 0, cacheR: 0, out: 0, ws: path ? ws(path) : '' }))
  }
  const short = (await f.read({ query: { range: '30m' } })).jsonBody
  assert.deepEqual(short.projects.map(p => [p.name, p.percent]), [['Unassigned project', 50], ['beta', 30], ['alpha', 20]])
  const day = (await f.read({ query: { range: '24h' } })).jsonBody
  assert.equal(day.projects.find(p => p.name === 'alpha').percent, 100 * 70 / 150)
  assert.equal((await f.read({ query: { range: '7d' } })).jsonBody.totals.tokens, 250)
  assert.equal((await f.read({ query: { range: 'reset' } })).jsonBody.totals.tokens, 150)
  const empty = (await f.read({ query: { range: '30m' }, now: new Date(NOW.getTime() + 86_400_000) })).jsonBody
  assert.equal(empty.totals.tokens, 0)
  assert.deepEqual(empty.projects, [])
})

test('rolling filters keep account and organisation guards and reject unbounded or malformed ranges', async () => {
  for (const over of [{ acct: '' }, { acctQ: 'inferred' }]) {
    const f = await fixture({ ...over, window: 'plan', resetsAt: null })
    assert.equal((await f.read({ query: { range: '1h' } })).jsonBody.reason, 'account-unavailable')
  }
  const f = await fixture({ org: 'a_1111111111111111' })
  await upsertLimits(f.store, x => x(), [meter({ id: 'b'.repeat(32), org: 'a_2222222222222222' })])
  assert.equal((await f.read({ query: { range: '7d' } })).jsonBody.reason, 'organisation-unavailable')
  for (const range of ['', 'ALL', '365d', '__proto__', 'toString', ['1h']]) {
    const result = await f.read({ query: { range } })
    assert.equal(result.status, 400)
  }
})

test('Cursor includes only event-timed usage; provenance survives validation, storage, reparse and out-of-order merges', async () => {
  const f = await fixture({ provider: 'cursor', source: 'cursor', window: 'plan', resetsAt: null })
  for (const [key, tsQ] of [['message', 'event'], ['conversation', 'session-start'], ['legacy', '']]) {
    await f.event(usage(key, { provider: 'cursor', source: 'cursor', ts: new Date(NOW.getTime() - 60_000).toISOString(), tsQ }))
  }
  const b = (await f.read({ query: { range: '1h' } })).jsonBody
  assert.equal(b.totals.tokens, 1170)
  assert.equal(b.timingLimited, true)
  const input = usage('provenance', { source: 'cursor', provider: 'cursor', tsQ: 'event', pv: 2 })
  const validated = validateUsage(input, NOW)
  assert.equal(validated.ok, true)
  const event = validated.value
  assert.equal(rowToEvent({ rowKey: input.id, ...storedFields(event) }).tsQ, 'event')
  assert.equal(validateUsage({ ...input, tsQ: 'invented' }, NOW).ok, false)
  const fallback = { ...event, tsQ: 'session-start', ts: '2026-01-01T00:00:00Z' }
  for (const [a, c] of [[event, fallback], [fallback, event], [event, { ...fallback, pv: 1 }], [{ ...fallback, pv: 1 }, event]]) {
    const merged = mergeEvent(a, c)
    assert.equal(merged.tsQ, 'event')
    assert.equal(merged.ts, event.ts)
  }
})

test('range=all answers every period from one read, each equal to its single-range answer', async () => {
  const f = await fixture({ resetsAt: new Date(NOW.getTime() + 6 * 86_400_000).toISOString() })
  await f.store.table('accounts').create({ partitionKey: 'fleet', rowKey: 'kenc', enc: encryptJson(KEY, 'fleet-k', K.toString('base64')) })
  await f.addPrompt('alpha', { workspace: '/example/alpha' })
  for (const [key, ageHours, tokens] of [['a', 0.25, 100], ['b', 5, 200], ['c', 48, 400]]) {
    await f.event(usage(key, { ts: new Date(NOW.getTime() - ageHours * 3_600_000).toISOString(),
      in: tokens, cacheW: 0, cacheR: 0, out: 0, ws: ws('/example/alpha') }))
  }
  const all = (await f.read({ query: { range: 'all' } })).jsonBody
  assert.equal(all.range, 'all')
  assert.deepEqual(Object.keys(all.ranges).sort(), ['12h', '1h', '24h', '30m', '3d', '3h', '6h', '7d', 'reset'])
  for (const [range, body] of Object.entries(all.ranges)) {
    assert.deepEqual(body, (await f.read({ query: { range } })).jsonBody, range)
  }
  assert.equal(all.ranges['30m'].totals.tokens, 100)
  assert.equal(all.ranges['6h'].totals.tokens, 300)
  assert.equal(all.ranges['7d'].totals.tokens, 700)
})

test('range=all reports a missing reset for that period only', async () => {
  const f = await fixture({ resetsAt: null, window: 'plan' })
  await f.store.table('accounts').create({ partitionKey: 'fleet', rowKey: 'kenc', enc: encryptJson(KEY, 'fleet-k', K.toString('base64')) })
  const all = (await f.read({ query: { range: 'all' } })).jsonBody
  assert.equal(all.ranges.reset.available, false)
  assert.equal(all.ranges.reset.reason, 'reset-unavailable')
  assert.equal(all.ranges['7d'].available, true)
})

test('one project on two machines is one row', async () => {
  const f = await fixture({ resetsAt: new Date(NOW.getTime() + 6 * 86_400_000).toISOString() })
  await f.store.table('accounts').create({ partitionKey: 'fleet', rowKey: 'kenc', enc: encryptJson(KEY, 'fleet-k', K.toString('base64')) })
  await f.addPrompt('mac', { workspace: '/Users/someone/acx_custody' })
  await f.addPrompt('vps', { workspace: '/home/agent/acx_custody' })
  for (const [key, tokens, path] of [['a', 60, '/Users/someone/acx_custody'], ['b', 40, '/home/agent/acx_custody']]) {
    await f.event(usage(key, { ts: new Date(NOW.getTime() - 3_600_000).toISOString(), in: tokens, cacheW: 0, cacheR: 0, out: 0, ws: ws(path) }))
  }
  const b = (await f.read({ query: { range: '24h' } })).jsonBody
  assert.deepEqual(b.projects.map(p => [p.name, p.tokens, p.percent]), [['acx_custody', 100, 100]])
})
