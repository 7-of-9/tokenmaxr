// Dirty-day / rollup races on the in-memory store (days.js): a recompute that
// lists a day's events before an ingest's writes land must never settle them.
import test from 'node:test'
import assert from 'node:assert/strict'
import { memoryStore } from '../../src/lib/tables.js'
import { createInvite, handleEnroll } from '../../src/lib/enroll.js'
import { handleIngest } from '../../src/lib/ingest.js'
import { handleUsage } from '../../src/lib/usage.js'
import { readMonths } from '../../src/lib/months.js'
import { recomputeDay } from '../../src/lib/days.js'
import { limiter } from '../../src/lib/pool.js'
import { KFP, batch, prompt, testKey, usage } from '../fixtures.js'

const DATE = '2026-09-20'

async function enrolled(store) {
  const { invite } = await createInvite(store, new Date(), 'test')
  const r = await handleEnroll({ method: 'POST', headers: {}, query: {}, body: { invite, machineLabel: 't', kFingerprint: KFP }, now: new Date() }, { store })
  assert.equal(r.status, 200)
  return r.jsonBody.token
}

const ingestReq = (token, body, now) => ({ method: 'POST', headers: { 'x-d0m1-token': token }, query: {}, body, now })
const clock = () => {
  const t0 = Date.now()
  return (s) => new Date(t0 + s * 1000)
}
const usageOn = async (store, now) =>
  (await handleUsage({ method: 'GET', headers: {}, query: { days: 'all' }, now }, { store })).jsonBody
const anthropicOut = (body) => body.days.find((d) => d.date === DATE)?.providers.anthropic.exact.out
const genOf = async (store, table, pk) => (await store.table(table).get(pk, DATE))?.gen
const httpErr = (status, message) => Object.assign(new Error(message), { statusCode: status })

// Wraps one table of the store; `patch(table)` returns the overridden methods.
function hookTable(store, name, patch) {
  const real = store.table.bind(store)
  store.table = (n) => (n === name ? { ...real(n), ...patch(real(n)) } : real(n))
  return () => {
    store.table = real
  }
}

// Reads back at the grace boundary, then settles: the rollup must end with
// the dirty gen and the expected total, and a further read must not list
// the day's events again.
async function assertConverges(store, at, { before, after }) {
  assert.equal(anthropicOut(await usageOn(store, at(30))), before, 'served as-is inside the grace period')
  assert.equal(anthropicOut(await usageOn(store, at(70))), after, 'settled after the grace period')
  assert.equal(await genOf(store, 'rollups', 'r'), await genOf(store, 'dirtydays', 'd'))
  let lists = 0
  const unhook = hookTable(store, 'events', (t) => ({ list: (...a) => (lists++, t.list(...a)) }))
  try {
    assert.equal(anthropicOut(await usageOn(store, at(600))), after)
    assert.equal(anthropicOut(await usageOn(store, at(3600))), after)
  } finally {
    unhook()
  }
  assert.equal(lists, 0, 'a settled day is not recomputed again')
}

test('a recompute racing a deferred ingest cannot leave its writes unrolled-up', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const at = clock()
  const ctx = { store, env: {} }
  await handleIngest(ingestReq(token, batch({ usage: [usage('r1', { out: 5 })] }), at(-300)), ctx)
  // A concurrent recompute (usage lazy / sweep, minAgeMs 0) reads the gen
  // right after this ingest's pre-write bump and lists before its write.
  let armed = true
  const unhook = hookTable(store, 'dirtydays', (t) => ({
    async replace(e, etag) {
      const r = await t.replace(e, etag)
      if (armed && e.rowKey === DATE) {
        armed = false
        unhook()
        await recomputeDay(store, limiter(4), DATE, at(0))
      }
      return r
    },
  }))
  const r = await handleIngest(ingestReq(token, batch({ usage: [usage('r2', { out: 7 })] }), at(0)), ctx)
  assert.deepEqual(r.jsonBody.retry, [])
  assert.equal(r.jsonBody.accepted.length, 1)
  // The racing rollup carries gen 2 without r2; the deferred ingest left the
  // day dirty at gen 3.
  assert.equal(await genOf(store, 'rollups', 'r'), 2)
  assert.equal(await genOf(store, 'dirtydays', 'd'), 3)
  await assertConverges(store, at, { before: 5, after: 12 })
})

test('two machines ingesting one day: the second cannot be settled by the first', async () => {
  const store = memoryStore()
  const [tokenA, tokenB] = [await enrolled(store), await enrolled(store)]
  const at = clock()
  const ctx = { store, env: {} }
  await handleIngest(ingestReq(tokenA, batch({ usage: [usage('m0', { out: 5 })] }), at(-300)), ctx)
  // B bumps (gen 2), then A bumps (gen 3), writes and recomputes from gen 3
  // before B's write lands; B then writes and its own recompute is deferred.
  let bumpedB
  const bBumped = new Promise((resolve) => (bumpedB = resolve))
  let releaseB
  const aDone = new Promise((resolve) => (releaseB = resolve))
  const evB = usage('mB', { out: 9 })
  const unhook = hookTable(store, 'dirtydays', (t) => ({
    async replace(e, etag) {
      const r = await t.replace(e, etag)
      if (e.rowKey === DATE && e.gen === 2) bumpedB()
      return r
    },
  }))
  const unhookEvents = hookTable(store, 'events', (t) => ({
    async create(e) {
      if (e.rowKey === evB.id) await aDone
      return t.create(e)
    },
  }))
  try {
    const b = handleIngest(ingestReq(tokenB, batch({ usage: [evB] }), at(0)), ctx)
    await bBumped
    const a = await handleIngest(ingestReq(tokenA, batch({ usage: [usage('mA', { out: 7 })] }), at(0)), ctx)
    assert.deepEqual(a.jsonBody.retry, [])
    assert.equal(await genOf(store, 'rollups', 'r'), 3, "A's recompute took B's gen")
    assert.equal(anthropicOut(await usageOn(store, at(1))), 12)
    releaseB()
    const rb = await b
    assert.deepEqual(rb.jsonBody.retry, [])
    assert.deepEqual(rb.jsonBody.accepted, [evB.id])
  } finally {
    unhook()
    unhookEvents()
  }
  assert.equal(await genOf(store, 'rollups', 'r'), 3)
  assert.equal(await genOf(store, 'dirtydays', 'd'), 4, 'B left the day dirty past the racing rollup')
  await assertConverges(store, at, { before: 12, after: 21 })
})

test('an ingest out of time after its writes leaves the day dirty past any racing rollup', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const at = clock()
  const ctx = { store, env: {} }
  await handleIngest(ingestReq(token, batch({ usage: [usage('t1', { out: 5 })] }), at(-300)), ctx)
  // As above, a recompute races the pre-write bump; then the write itself
  // outlasts the budget, so there is no time for the ingest's own recompute.
  let armed = true
  const unhookDirty = hookTable(store, 'dirtydays', (t) => ({
    async replace(e, etag) {
      const r = await t.replace(e, etag)
      if (armed && e.rowKey === DATE) {
        armed = false
        await recomputeDay(store, limiter(4), DATE, at(0))
      }
      return r
    },
  }))
  const ev = usage('t2', { out: 7 })
  const unhookEvents = hookTable(store, 'events', (t) => ({
    async create(e) {
      if (e.rowKey === ev.id) await new Promise((resolve) => setTimeout(resolve, 400))
      return t.create(e)
    },
  }))
  let r
  try {
    r = await handleIngest(ingestReq(token, batch({ usage: [ev] }), at(0)), { ...ctx, budgetMs: 300 })
  } finally {
    unhookDirty()
    unhookEvents()
  }
  assert.deepEqual(r.jsonBody.accepted, [ev.id])
  assert.equal(await genOf(store, 'rollups', 'r'), 2)
  assert.equal(await genOf(store, 'dirtydays', 'd'), 3)
  await assertConverges(store, at, { before: 5, after: 12 })
})

test('a contended post-write bump recomputes at once instead of deferring', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const at = clock()
  const ctx = { store, env: {} }
  await handleIngest(ingestReq(token, batch({ usage: [usage('c1', { out: 5 })] }), at(-10)), ctx)
  // The pre-write bump goes through; every later replace of the row is a
  // lost ETag race.
  let replaces = 0
  const unhook = hookTable(store, 'dirtydays', (t) => ({
    async replace(e, etag) {
      if (e.rowKey === DATE && ++replaces > 1) throw httpErr(412, 'UpdateConditionNotSatisfied')
      return t.replace(e, etag)
    },
  }))
  let r
  try {
    r = await handleIngest(ingestReq(token, batch({ usage: [usage('c2', { out: 7 })] }), at(0)), ctx)
  } finally {
    unhook()
  }
  assert.deepEqual(r.jsonBody.retry, [])
  assert.equal(r.jsonBody.accepted.length, 1)
  assert.ok(replaces > 1, 'the deferred path tried to bump')
  const roll = await store.table('rollups').get('r', DATE)
  assert.equal(roll.gen, 2)
  assert.equal(await genOf(store, 'dirtydays', 'd'), 2)
  assert.equal(JSON.parse(roll.data).providers.anthropic.exact.out, 12)
})

test('a prompt whose month could not be recorded goes to retry, and a re-send records it', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const env = { PROMPT_ENC_KEY: testKey() }
  let fail = true
  const unhook = hookTable(store, 'accounts', (t) => ({
    async create(e) {
      if (fail && e.partitionKey === 'meta') throw httpErr(503, 'ServerBusy')
      return t.create(e)
    },
  }))
  try {
    const p = prompt('old1', { ts: '2026-01-15T10:00:00.000Z' })
    const r = await handleIngest(ingestReq(token, batch({ prompts: [p] }), new Date()), { store, env })
    assert.equal(r.status, 200)
    assert.deepEqual(r.jsonBody.retry, [p.id])
    assert.deepEqual(r.jsonBody.accepted, [])
    assert.deepEqual(await readMonths(store), [])
    fail = false
    const again = await handleIngest(ingestReq(token, batch({ prompts: [p] }), new Date()), { store, env })
    assert.deepEqual(again.jsonBody.retry, [])
    assert.deepEqual(again.jsonBody.accepted, [p.id])
    assert.deepEqual(await readMonths(store), ['2026-01'])
    assert.equal((await store.table('prompts').list('2026-01')).length, 1)
  } finally {
    unhook()
  }
})
