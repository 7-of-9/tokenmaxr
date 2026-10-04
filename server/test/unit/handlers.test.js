// Handler tests on the in-memory store, over HTTP through the harness.
import test from 'node:test'
import assert from 'node:assert/strict'
import { createHarness } from '../harness.js'
import { memoryStore } from '../../src/lib/tables.js'
import { createInvite, handleEnroll } from '../../src/lib/enroll.js'
import { handleIngest } from '../../src/lib/ingest.js'
import { handleUsage } from '../../src/lib/usage.js'
import { handlePrompts } from '../../src/lib/prompts.js'
import { bumpDirty } from '../../src/lib/days.js'
import { limiter } from '../../src/lib/pool.js'
import { client, runScenario, OWNER_IDS, SCENARIO_CTX } from '../scenario.js'
import {
  ACCT, HOMES, KFP, activity, batch, codexUsage, cursorUsage, geminiUsage, grokUsage, principal, prompt, testKey, usage,
} from '../fixtures.js'
import { eventId } from '../../src/lib/ids.js'

async function listen(ctx) {
  const server = createHarness({ ...ctx, quiet: true })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  return { server, call: client(`http://127.0.0.1:${server.address().port}`) }
}

test('API scenario on the memory store', async (t) => {
  const store = memoryStore()
  const env = { PROMPT_ENC_KEY: testKey(), AGENTS_OWNER_IDS: OWNER_IDS }
  const { server, call } = await listen({ store, env, ...SCENARIO_CTX })
  try {
    await runScenario(t, { call, store })
    // Prompt text is stored only as ciphertext.
    const promptRows = [...store._tables.get('prompts').values()]
    assert.equal(promptRows.length, 10)
    for (const { entity } of promptRows) {
      assert.ok(!JSON.stringify(entity).includes('synthetic'), 'plaintext in prompts row')
    }
    assert.equal(store._blobs.size, 1)
    assert.ok(![...store._tables.get('accounts').values()].some(({ entity }) => 'label' in entity))
  } finally {
    server.close()
  }
})

async function enroll(store, machineLabel = 't') {
  const { invite } = await createInvite(store, new Date(), 'test')
  const r = await handleEnroll({ method: 'POST', headers: {}, query: {}, body: { invite, machineLabel, kFingerprint: KFP }, now: new Date() }, { store })
  assert.equal(r.status, 200)
  return r.jsonBody
}

const enrolled = async (store) => (await enroll(store)).token

const ingestReq = (token, body, now = new Date()) => ({ method: 'POST', headers: { 'x-d0m1-token': token }, query: {}, body, now })

test('an exhausted time budget returns every id in retry', async () => {
  const store = memoryStore()
  const env = { PROMPT_ENC_KEY: testKey() }
  const token = await enrolled(store)
  const body = batch({ usage: [usage('b1'), usage('b2')], prompts: [prompt('b3')] })
  const r = await handleIngest(ingestReq(token, body), { store, env, budgetMs: 0 })
  assert.equal(r.status, 200)
  assert.deepEqual(r.jsonBody.accepted, [])
  assert.deepEqual(new Set(r.jsonBody.retry), new Set(body.usage.map((u) => u.id).concat(body.prompts[0].id)))
  // The retry succeeds once there is time.
  const ok = await handleIngest(ingestReq(token, body), { store, env })
  assert.deepEqual(ok.jsonBody.retry, [])
  assert.equal(ok.jsonBody.accepted.length, 3)
})

test('prompts go to retry when PROMPT_ENC_KEY is missing', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const body = batch({ usage: [usage('k1')], prompts: [prompt('k2')] })
  const r = await handleIngest(ingestReq(token, body), { store, env: {} })
  assert.deepEqual(r.jsonBody.accepted, [body.usage[0].id])
  assert.deepEqual(r.jsonBody.retry, [body.prompts[0].id])
})

test('usage lazily recomputes a day left dirty by a failed ingest', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  await handleIngest(ingestReq(token, batch({ usage: [usage('l1', { out: 5 })] })), { store, env: {} })
  // Simulate an ingest that bumped the gen and wrote an event, then died.
  const lim = limiter(4)
  const past = new Date(Date.now() - 5 * 60 * 1000)
  await bumpDirty(store, lim, '2026-09-20', past)
  const events = store.table('events')
  const row = await events.get('2026-09-20', usage('l1').id)
  await events.replace({ ...row, out: 77 }, row.etag)

  const get = async (now) => (await handleUsage({ method: 'GET', headers: {}, query: { days: '1500' }, now }, { store })).jsonBody
  // Within the grace period the old rollup is served.
  const fresh = await get(new Date(past.getTime() + 10 * 1000))
  assert.equal(fresh.days[0].providers.anthropic.exact.out, 5)
  const later = await get(new Date())
  assert.equal(later.days[0].providers.anthropic.exact.out, 77)
  const rollup = await store.table('rollups').get('r', '2026-09-20')
  assert.equal(rollup.gen, 2)
})

test('a stale rollup computation never overwrites a newer one', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  await handleIngest(ingestReq(token, batch({ usage: [usage('g1', { out: 1 })] })), { store, env: {} })
  const rollups = store.table('rollups')
  const before = await rollups.get('r', '2026-09-20')
  // Pretend another writer already published gen 99.
  await rollups.replace({ ...before, gen: 99 }, before.etag)
  await handleIngest(ingestReq(token, batch({ usage: [usage('g1', { out: 2 })] })), { store, env: {} })
  const after = await rollups.get('r', '2026-09-20')
  assert.equal(after.gen, 99)
  assert.equal(after.data, before.data)
})

const clock = () => {
  const t0 = Date.now()
  return (s) => new Date(t0 + s * 1000)
}
const usageOn = async (store, now, ctx = {}) =>
  (await handleUsage({ method: 'GET', headers: {}, query: { days: 'all' }, now }, { store, ...ctx })).jsonBody
const anthropicOut = (body) => body.days.find((d) => d.date === '2026-09-20')?.providers.anthropic.exact.out
const genOf = async (store, table, pk) => (await store.table(table).get(pk, '2026-09-20'))?.gen

test('a day written again within 20 s stays dirty until 60 s after its last write', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const at = clock()
  const ctx = { store, env: {} }
  await handleIngest(ingestReq(token, batch({ usage: [usage('d1', { out: 5 })] }), at(0)), ctx)
  // No rollup yet, so the first write computes one at once.
  assert.equal(anthropicOut(await usageOn(store, at(1))), 5)
  await handleIngest(ingestReq(token, batch({ usage: [usage('d2', { out: 7 })] }), at(5)), ctx)
  assert.ok(await genOf(store, 'dirtydays', 'd') > await genOf(store, 'rollups', 'r'), 'recompute deferred')
  // Inside the grace period the previous rollup is served...
  assert.equal(anthropicOut(await usageOn(store, at(30))), 5)
  // ...and after it the read recomputes.
  assert.equal(anthropicOut(await usageOn(store, at(66))), 12)
  assert.equal(await genOf(store, 'rollups', 'r'), await genOf(store, 'dirtydays', 'd'))
  // A write once the rollup is 20 s old recomputes at once again.
  await handleIngest(ingestReq(token, batch({ usage: [usage('d3', { out: 1 })] }), at(91)), ctx)
  const row = await store.table('rollups').get('r', '2026-09-20')
  assert.equal(JSON.parse(row.data).providers.anthropic.exact.out, 13)
  assert.equal(row.gen, await genOf(store, 'dirtydays', 'd'))
})

test('the ingest sweep settles quiet dirty days with no reader', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const at = clock()
  const ctx = { store, env: {} }
  await handleIngest(ingestReq(token, batch({ usage: [usage('s1', { out: 5 })] }), at(0)), ctx)
  await handleIngest(ingestReq(token, batch({ usage: [usage('s2', { out: 7 })] }), at(5)), ctx)
  assert.ok(await genOf(store, 'dirtydays', 'd') > await genOf(store, 'rollups', 'r'))
  // Too early: the day is not quiet yet (and the sweep runs once a minute).
  store.cache.delete('sweepAt')
  await handleIngest(ingestReq(token, batch({ heartbeat: { machineLabel: 't' } }), at(30)), ctx)
  assert.ok(await genOf(store, 'dirtydays', 'd') > await genOf(store, 'rollups', 'r'))
  // A minute later a heartbeat-only request settles it.
  store.cache.delete('sweepAt')
  await handleIngest(ingestReq(token, batch({ heartbeat: { machineLabel: 't' } }), at(70)), ctx)
  const row = await store.table('rollups').get('r', '2026-09-20')
  assert.equal(row.gen, await genOf(store, 'dirtydays', 'd'))
  assert.equal(JSON.parse(row.data).providers.anthropic.exact.out, 12)
})

test('v1 rollups are recomputed on read, and served in the v2 shape meanwhile', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  await handleIngest(ingestReq(token, batch({ usage: [usage('v1', { out: 5, cacheW1h: 3 })] })), { store, env: {}, rollupMinAgeMs: 0 })
  const rollups = store.table('rollups')
  const row = await rollups.get('r', '2026-09-20')
  const v2 = JSON.parse(row.data)
  const v1 = { date: v2.date, providers: { anthropic: { ...v2.providers.anthropic, models: { 'claude-test-model': 1234.5 } } } }
  delete v1.providers.anthropic.byMachine
  delete v1.providers.anthropic.byCountry
  await rollups.replace({ ...row, data: JSON.stringify(v1) }, row.etag)

  // No time left to recompute: the v1 day is still served, marked v: 1.
  const old = await usageOn(store, new Date(), { budgetMs: 0 })
  assert.equal(old.days[0].v, 1)
  assert.deepEqual(old.days[0].providers.anthropic.models, { exact: {}, estimated: {} })
  assert.equal(anthropicOut(old), 5)

  const fresh = await usageOn(store, new Date())
  assert.equal(fresh.days[0].v, 3)
  assert.equal(fresh.days[0].providers.anthropic.models.exact['claude-test-model'].cacheW1h, 3)
  assert.equal(JSON.parse((await rollups.get('r', '2026-09-20')).data).v, 3)
})

test('ingest clamps cacheW1h to cacheW and says so', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const ev = usage('w1', { cacheW: 10, cacheW1h: 30 })
  const r = await handleIngest(ingestReq(token, batch({ usage: [ev, usage('w2', { cacheW1h: 4 })] })), { store, env: {} })
  assert.deepEqual(r.jsonBody.retry, [])
  assert.equal(r.jsonBody.accepted.length, 2)
  assert.deepEqual(r.jsonBody.warnings, [{ id: ev.id, warning: 'cacheW1h clamped to cacheW' }])
  assert.equal((await store.table('events').get('2026-09-20', ev.id)).cacheW1h, 10)
  assert.equal((await store.table('events').get('2026-09-20', usage('w2').id)).cacheW1h, 4)
})

test('heartbeats keep the public label, time zone and country; events take the country', async () => {
  const store = memoryStore()
  const { token, machineId } = await enroll(store, '  Old\u0000 \t Name  ')
  const machines = store.table('machines')
  assert.equal((await machines.get('m', machineId)).label, 'Old Name')
  const ctx = { store, env: {} }
  const send = async (parts) => {
    const r = await handleIngest(ingestReq(token, batch(parts)), ctx)
    assert.equal(r.status, 200)
    return r.jsonBody
  }
  // Before any tz the country is unknown.
  await send({ activity: [activity('h0')] })
  assert.equal((await store.table('events').get('2026-09-20', activity('h0').id)).cc, '')

  await send({ heartbeat: { machineLabel: 'Studio‮ PC', os: 'windows', tz: { iana: 'Europe/Berlin', windowsId: 'W. Europe Standard Time', country: 'DE', source: 'windows+region' } } })
  let row = await machines.get('m', machineId)
  assert.deepEqual([row.label, row.tzIana, row.cc], ['Studio PC', 'Europe/Berlin', 'DE'])
  // The next request (token cache still warm) stamps the new country.
  await send({ usage: [usage('h1')] })
  const ev = await store.table('events').get('2026-09-20', usage('h1').id)
  assert.deepEqual([ev.machine, ev.cc], [machineId, 'DE'])
  // A heartbeat without tz leaves the country alone; a bad one clears it.
  await send({ heartbeat: { machineLabel: 'Studio PC' } })
  assert.equal((await machines.get('m', machineId)).cc, 'DE')
  await send({ heartbeat: { machineLabel: '', tz: { iana: 'x y', country: 'Germany' } } })
  row = await machines.get('m', machineId)
  assert.deepEqual([row.label, row.tzIana, row.cc], ['Studio PC', '', ''])
  // The event keeps the country it was inserted with.
  await send({ usage: [usage('h1', { out: 999 })] })
  const again = await store.table('events').get('2026-09-20', usage('h1').id)
  assert.deepEqual([again.out, again.machine, again.cc], [999, machineId, 'DE'])
  // A malformed heartbeat is reported and does not block the events.
  const bad = await send({ usage: [usage('h2')], heartbeat: { machineLabel: 'x', tz: 'DE' } })
  assert.ok(bad.accepted.includes(usage('h2').id))
  assert.match(bad.rejected[0].error, /heartbeat: tz must be an object/)
})

test('a batch insert that meets an existing row falls back to per-event merges', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const ctx = { store, env: {}, rollupMinAgeMs: 0 }
  const first = [usage('b1', { out: 1 }), usage('b2', { out: 2 }), usage('b3', { out: 3 })]
  assert.deepEqual((await handleIngest(ingestReq(token, batch({ usage: first })), ctx)).jsonBody.retry, [])
  // Drop the index rows so the same ids pin as fresh again while their event
  // rows exist: the transaction fails with 409 and nothing is lost.
  for (const e of first) await store.table('eventindex').delete(e.id.slice(0, 2), e.id)
  const second = [usage('b1', { out: 10 }), usage('b2', { out: 2 }), usage('b4', { out: 4 })]
  const r = await handleIngest(ingestReq(token, batch({ usage: second })), ctx)
  assert.deepEqual(r.jsonBody.retry, [])
  assert.equal(r.jsonBody.accepted.length, 3)
  assert.equal(anthropicOut(await usageOn(store, new Date())), 10 + 2 + 3 + 4)
})

test('memory createBatch is all or nothing within one partition', async () => {
  const t = memoryStore().table('x')
  await t.createBatch([{ partitionKey: 'p', rowKey: 'a' }, { partitionKey: 'p', rowKey: 'b' }])
  await assert.rejects(t.createBatch([{ partitionKey: 'p', rowKey: 'c' }, { partitionKey: 'p', rowKey: 'a' }]), { statusCode: 409 })
  assert.equal(await t.get('p', 'c'), null)
  await assert.rejects(t.createBatch([{ partitionKey: 'p', rowKey: 'd' }, { partitionKey: 'q', rowKey: 'e' }]), { statusCode: 400 })
  assert.deepEqual((await t.list('p')).map((r) => r.rowKey), ['a', 'b'])
})

test('cursor is a fourth provider: ingest, rollup, usage totals, prompts filter, idempotent re-send', async () => {
  const store = memoryStore()
  const env = { PROMPT_ENC_KEY: testKey(), AGENTS_OWNER_IDS: OWNER_IDS }
  const { token, machineId } = await enroll(store)
  const ctx = { store, env, rollupMinAgeMs: 0 }
  const day = '2026-09-20'
  const cursorActivity = (k, over = {}) => activity(k, {
    id: eventId('activity', 'cursor', 'cursor', k), provider: 'cursor', source: 'cursor', acct: ACCT.cursor, session: '', ...over,
  })
  const cursorPrompt = (k, over = {}) => prompt(k, {
    id: eventId('prompt', 'cursor', 'cursor', k), provider: 'cursor', source: 'cursor', model: 'cursor-test-model', acct: ACCT.cursor, session: '', ...over,
  })
  const body = batch({
    usage: [
      cursorUsage('k1', { in: 300, out: 40 }),
      cursorUsage('k2', { in: 5, out: 1, model: '', acct: '', acctQ: 'unknown' }),
      usage('c1', { out: 9 }),
    ],
    activity: [cursorActivity('ka1'), cursorActivity('ka2', { hasUsage: false })],
    prompts: [cursorPrompt('kp1', { text: 'synthetic cursor prompt' }), prompt('cp1')],
  })
  const send = async () => {
    const r = await handleIngest(ingestReq(token, body), ctx)
    assert.equal(r.status, 200)
    assert.deepEqual(r.jsonBody.retry, [])
    assert.deepEqual(r.jsonBody.rejected, [])
    assert.equal(r.jsonBody.accepted.length, 7)
    return r.jsonBody
  }
  await send()
  const u = await usageOn(store, new Date())
  const d = u.days.find((x) => x.date === day)
  assert.deepEqual(Object.keys(d.providers), ['anthropic', 'cursor'])
  const k = d.providers.cursor
  assert.deepEqual(k.exact, { in: 305, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 41, reasoning: 0, calls: 2, events: 2 })
  assert.deepEqual(k.models.exact, {
    'cursor-test-model': { in: 300, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 40, reasoning: 0, calls: 1 },
    unknown: { in: 5, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 1, reasoning: 0, calls: 1 },
  })
  assert.deepEqual([k.prompts, k.promptsNoUsage], [2, 1])
  assert.deepEqual(k.byMachine, { [machineId]: { in: 305, cacheW: 0, cacheR: 0, out: 41, prompts: 2 } })
  assert.deepEqual(k.byCountry, { ZZ: { in: 305, cacheW: 0, cacheR: 0, out: 41, prompts: 2 } })
  assert.deepEqual(k.accts, ['acct2'])
  assert.deepEqual(d.providers.anthropic.exact.out, 9)
  assert.deepEqual(u.totals.accountsByProvider, { anthropic: 1, openai: 0, xai: 0, cursor: 1, google: 0 })
  assert.equal(u.totals.accounts, 2)
  assert.ok(!JSON.stringify(u).includes(ACCT.cursor))
  // The archive filters by provider.
  const owner = { 'x-ms-client-principal': principal('github', 'test-owner') }
  const list = async (query) => (await handlePrompts({ method: 'GET', headers: owner, query, params: {}, now: new Date() }, { store, env })).jsonBody
  const all = await list({ month: '2026-09' })
  assert.equal(all.items.length, 2)
  const onlyCursor = await list({ month: '2026-09', provider: 'cursor' })
  assert.deepEqual(onlyCursor.items.map((i) => [i.provider, i.source, i.model, i.text]), [['cursor', 'cursor', 'cursor-test-model', 'synthetic cursor prompt']])
  // Re-sending the same batch, any number of times, changes nothing.
  const before = JSON.stringify(u.days)
  await send()
  await send()
  assert.equal(JSON.stringify((await usageOn(store, new Date())).days), before)
  assert.equal((await list({ month: '2026-09' })).items.length, 2)
})

test('gemini-cli is a fifth provider (google) and heartbeats may list scanned homes', async () => {
  const store = memoryStore()
  const env = { PROMPT_ENC_KEY: testKey(), AGENTS_OWNER_IDS: OWNER_IDS }
  const { token, machineId } = await enroll(store)
  const ctx = { store, env, rollupMinAgeMs: 0 }
  const machines = store.table('machines')
  const geminiActivity = (k, over = {}) => activity(k, {
    id: eventId('activity', 'google', 'gemini-cli', k), provider: 'google', source: 'gemini-cli', acct: ACCT.google, session: '', ...over,
  })
  const geminiPrompt = (k, over = {}) => prompt(k, {
    id: eventId('prompt', 'google', 'gemini-cli', k), provider: 'google', source: 'gemini-cli', model: 'gemini-test-model', acct: ACCT.google, session: '', ...over,
  })
  // One event per provider, so the day's rollup carries all five.
  const body = batch({
    usage: [
      geminiUsage('g1', { in: 120, cacheR: 800, out: 60, reasoning: 25 }),
      geminiUsage('g2', { in: 10, cacheR: 0, out: 4, reasoning: 0, model: 'gemini-flash-test' }),
      usage('a1', { out: 9 }),
      codexUsage('o1', { in: 200, cacheR: 0, cacheW: 0, out: 100, reasoning: 40 }),
      grokUsage('x1', { in: 70, cacheR: 0, cacheW: 0, out: 30 }),
      cursorUsage('k1', { in: 300, out: 40 }),
    ],
    activity: [geminiActivity('ga1'), geminiActivity('ga2', { hasUsage: false })],
    prompts: [geminiPrompt('gp1', { text: 'synthetic gemini prompt' })],
    heartbeat: { machineLabel: 'Studio PC', os: 'windows', homes: HOMES },
  })
  const send = async (b = body) => {
    const r = await handleIngest(ingestReq(token, b), ctx)
    assert.equal(r.status, 200)
    assert.deepEqual(r.jsonBody.retry, [])
    return r.jsonBody
  }
  const first = await send()
  assert.deepEqual(first.rejected, [])
  assert.equal(first.accepted.length, 9)
  const u = await usageOn(store, new Date())
  const d = u.days.find((x) => x.date === '2026-09-20')
  assert.deepEqual(Object.keys(d.providers), ['anthropic', 'cursor', 'google', 'openai', 'xai'])
  const g = d.providers.google
  assert.deepEqual(g.exact, { in: 130, cacheW: 0, cacheW1h: 0, cacheR: 800, out: 64, reasoning: 25, calls: 2, events: 2 })
  assert.deepEqual(Object.keys(g.models.exact), ['gemini-flash-test', 'gemini-test-model'])
  assert.deepEqual([g.prompts, g.promptsNoUsage], [2, 1])
  assert.deepEqual(g.byMachine, { [machineId]: { in: 130, cacheW: 0, cacheR: 800, out: 64, prompts: 2 } })
  assert.deepEqual(u.totals.accountsByProvider, { anthropic: 1, openai: 1, xai: 1, cursor: 1, google: 1 })
  assert.equal(u.totals.accounts, 5)
  assert.ok(!JSON.stringify(u).includes(ACCT.google))
  // The archive filters google prompts like any other provider.
  const owner = { 'x-ms-client-principal': principal('github', 'test-owner') }
  const list = async (query) => (await handlePrompts({ method: 'GET', headers: owner, query, params: {}, now: new Date() }, { store, env })).jsonBody
  assert.deepEqual((await list({ month: '2026-09', provider: 'google' })).items.map((i) => [i.provider, i.source, i.model, i.text]),
    [['google', 'gemini-cli', 'gemini-test-model', 'synthetic gemini prompt']])

  // Homes: stored on the machine row, never in the public response.
  let row = await machines.get('m', machineId)
  assert.deepEqual(JSON.parse(row.homes), HOMES)
  const raw = JSON.stringify(u)
  for (const h of HOMES) assert.ok(!raw.includes(h), 'usage leaks a home')
  assert.ok(!raw.includes('homes'))
  // A heartbeat without homes leaves the list alone; an empty list clears it.
  await send(batch({ heartbeat: { machineLabel: 'Studio PC' } }))
  assert.deepEqual(JSON.parse((await machines.get('m', machineId)).homes), HOMES)
  await send(batch({ heartbeat: { machineLabel: 'Studio PC', homes: [] } }))
  assert.equal((await machines.get('m', machineId)).homes, '[]')
  // A malformed list rejects the heartbeat only; the events still land.
  const bad = await send(batch({ usage: [geminiUsage('g3')], heartbeat: { machineLabel: 'Studio PC', homes: Array.from({ length: 17 }, (_, i) => `/h${i}`) } }))
  assert.ok(bad.accepted.includes(geminiUsage('g3').id))
  assert.match(bad.rejected[0].error, /heartbeat: homes exceeds 16/)
  assert.equal((await machines.get('m', machineId)).homes, '[]')
  await send(batch({ heartbeat: { machineLabel: 'Studio PC', homes: ['/ok', 42] } }))
  assert.equal((await machines.get('m', machineId)).homes, '[]')

  // Re-sending the same batch, any number of times, changes nothing.
  const before = JSON.stringify((await usageOn(store, new Date())).days)
  await send()
  await send()
  assert.equal(JSON.stringify((await usageOn(store, new Date())).days), before)
  row = await machines.get('m', machineId)
  assert.deepEqual(JSON.parse(row.homes), HOMES)
})

test('the prompt archive index: 20k prompts, one build per TTL, dropped by ingest', async () => {
  const { encryptJson, parseKey } = await import('../../src/lib/crypto.js')
  const store = memoryStore()
  const env = { PROMPT_ENC_KEY: testKey(), AGENTS_OWNER_IDS: OWNER_IDS }
  const key = parseKey(env.PROMPT_ENC_KEY)
  const t = store.table('prompts')
  const months = Array.from({ length: 12 }, (_, i) => `2025-${String(i + 1).padStart(2, '0')}`)
  const N = 20000
  for (let i = 0; i < N; i++) {
    const month = months[i % 12]
    const id = eventId('prompt', 'anthropic', 'claude-code', `scale${i}`)
    const payload = {
      text: `synthetic prompt number ${i} `.repeat(20), workspace: `/synthetic/ws${i % 40}${i % 3 ? '/sub' : ''}`,
      acctLabel: i % 5 ? 'synthetic-label' : '', acct: ACCT.anthropic, machine: `m${i % 3}`, session: '',
    }
    await t.create({
      partitionKey: month, rowKey: id, ts: `${month}-15T10:${String(i % 60).padStart(2, '0')}:00.000Z`, tzOffsetMin: 0,
      provider: 'anthropic', source: 'claude-code', model: 'claude-test-model', blob: false, enc: encryptJson(key, id, payload),
    })
  }
  await store.table('accounts').create({ partitionKey: 'meta', rowKey: 'months', months: JSON.stringify(months) })
  const owner = { 'x-ms-client-principal': principal('github', 'test-owner') }
  const get = (query, params = {}) => handlePrompts({ method: 'GET', headers: owner, query, params, now: new Date() }, { store, env })

  const t0 = performance.now()
  const first = await get({ month: 'all', limit: '50', facets: '1' })
  const built = performance.now() - t0
  assert.equal(first.status, 200)
  assert.equal(first.jsonBody.total, N)
  assert.equal(first.jsonBody.items.length, 50)
  assert.ok(built < 10000, `index build took ${built} ms`)
  const f = first.jsonBody.facets
  assert.equal(f.workspaces.length, 40, 'sub-folders fold into their workspace')
  assert.deepEqual(f.accounts.map((a) => [a.acct, a.count, a.probable]), [[`${ACCT.anthropic}~timeline`, N * 0.8, undefined], [`unknown:anthropic:${ACCT.anthropic}`, N * 0.2, 'synthetic-label']])
  assert.equal(f.months.reduce((s, m) => s + m.count, 0), N)

  // Cached: a filtered search over the whole archive is fast.
  const t1 = performance.now()
  const search = await get({ q: 'number 19999 ', machine: 'm1' })
  assert.ok(performance.now() - t1 < built, 'served from the cached index')
  assert.equal(search.jsonBody.total, 1) // prompt 19999 is on m1

  // A row written behind the index is not seen until ingest drops it.
  const id = eventId('prompt', 'anthropic', 'claude-code', 'late')
  await t.create({ partitionKey: '2025-12', rowKey: id, ts: '2025-12-31T23:00:00.000Z', tzOffsetMin: 0, provider: 'anthropic', source: 'claude-code', model: '', blob: false,
    enc: encryptJson(key, id, { text: 'late', workspace: '', acctLabel: '', acct: '', machine: '', session: '' }) })
  assert.equal((await get({})).jsonBody.total, N)
  const { token } = await enroll(store)
  const r = await handleIngest(ingestReq(token, batch({ prompts: [prompt('fresh', { ts: '2025-06-01T00:00:00Z' })] })), { store, env })
  assert.deepEqual(r.jsonBody.retry, [])
  assert.equal((await get({})).jsonBody.total, N + 2)
  assert.equal((await get({}, { id: 'facets' })).jsonBody.total, N + 2)
})

test('workspace tags survive ingest, old rollup upgrade and public aliases without losing accounting', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const ws1 = 'w_' + '1'.repeat(16)
  const ws2 = 'w_' + '2'.repeat(16)
  const ctx = { store, env: {}, rollupMinAgeMs: 0 }
  const ingested = await handleIngest(ingestReq(token, batch({
    usage: [usage('ws-small', { ws: ws1, out: 10 }), usage('ws-large', { ws: ws2, out: 100 })],
    activity: [activity('ws-recorded', { ws: ws1, hasUsage: true }), activity('ws-missing', { ws: ws2, hasUsage: false }), activity('ws-legacy', { hasUsage: false })],
  })), ctx)
  assert.equal(ingested.status, 200)
  assert.deepEqual(ingested.jsonBody.retry, [])
  const rollups = store.table('rollups')
  const row = await rollups.get('r', '2026-09-20')
  const current = JSON.parse(row.data)
  assert.equal(current.providers.anthropic.byWorkspace[ws1].exact.out, 10)
  assert.equal(current.providers.anthropic.byWorkspace[ws2].promptsNoUsage, 1)
  const v2 = { ...current, v: 2 }
  delete v2.providers.anthropic.byWorkspace
  await rollups.replace({ ...row, data: JSON.stringify(v2) }, row.etag)
  const pending = await usageOn(store, new Date(), { budgetMs: 0 })
  assert.equal(pending.days[0].v, 2)
  assert.equal(pending.days[0].providers.anthropic.models.exact['claude-test-model'].out, 110, 'pending v2 keeps pricing data')
  const upgraded = await usageOn(store, new Date())
  assert.equal(upgraded.days[0].v, 3)
  const p = upgraded.days[0].providers.anthropic
  assert.deepEqual(Object.keys(p.byWorkspace).sort(), ['unknown', 'workspace1', 'workspace2'])
  assert.equal(p.byWorkspace.workspace1.exact.out, 10)
  assert.equal(p.byWorkspace.workspace2.exact.out, 100)
  assert.equal(p.byWorkspace.unknown.promptsNoUsage, 1)
  assert.equal(Object.values(p.byWorkspace.workspace2.byMachine)[0].promptsNoUsage, 1)
  const response = JSON.stringify(upgraded)
  assert.ok(!response.includes(ws1) && !response.includes(ws2), 'public response never includes stored workspace hashes')
  for (const tier of ['exact', 'estimated']) for (const field of ['in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning', 'calls', 'events']) {
    assert.equal(Object.values(p.byWorkspace).reduce((n, w) => n + w[tier][field], 0), p[tier][field])
  }
  assert.equal(Object.values(p.byWorkspace).reduce((n, w) => n + w.prompts, 0), p.prompts)
  assert.equal(Object.values(p.byWorkspace).reduce((n, w) => n + w.promptsNoUsage, 0), p.promptsNoUsage)
})
