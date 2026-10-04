// GET /api/prompts order=asc|desc: oldest first by default, ties broken by id,
// and cursor pages that cover every match exactly once in either order, over
// the all-months index and a single month.
import test from 'node:test'
import assert from 'node:assert/strict'
import { memoryStore } from '../../src/lib/tables.js'
import { encryptJson, parseKey } from '../../src/lib/crypto.js'
import { handlePrompts } from '../../src/lib/prompts.js'
import { eventId } from '../../src/lib/ids.js'
import { principal, testKey } from '../fixtures.js'
import { OWNER_IDS } from '../scenario.js'

async function archive() {
  const store = memoryStore()
  const b64 = testKey()
  const key = parseKey(b64)
  const t = store.table('prompts')
  const months = ['2026-07', '2026-08', '2026-09']
  const rows = []
  for (let i = 0; i < 47; i++) {
    const month = months[i % 3]
    // Many prompts share one timestamp, so the id tie-break decides their order.
    const ts = `${month}-1${i % 4}T10:00:00.000Z`
    const id = eventId('prompt', 'anthropic', 'claude-code', `order${i}`)
    rows.push({ id, ts, month, odd: i % 2 === 1 })
    await t.create({
      partitionKey: month, rowKey: id, ts, tzOffsetMin: 0, provider: 'anthropic', source: 'claude-code', model: i % 2 ? 'odd' : 'even',
      acctQ: 'recorded', blob: false,
      enc: encryptJson(key, id, { text: `p${i}`, workspace: '', acctLabel: 'l', acct: '', machine: '', session: '' }),
    })
  }
  await store.table('accounts').create({ partitionKey: 'meta', rowKey: 'months', months: JSON.stringify([...months].reverse()) })
  const env = { PROMPT_ENC_KEY: b64, AGENTS_OWNER_IDS: OWNER_IDS }
  const owner = { 'x-ms-client-principal': principal('github', 'test-owner') }
  const get = (query) => handlePrompts({ method: 'GET', headers: owner, query, params: {}, now: new Date() }, { store, env })
  return { rows, get }
}

const asc = (a, b) => (a.ts === b.ts ? (a.id < b.id ? -1 : 1) : a.ts < b.ts ? -1 : 1)

async function walk(get, query, limit) {
  const seen = []
  let cursor = null
  for (let n = 0; n < 100; n++) {
    const r = await get({ ...query, limit: String(limit), ...(cursor ? { cursor } : {}) })
    assert.equal(r.status, 200)
    seen.push(...r.jsonBody.items.map((i) => i.id))
    cursor = r.jsonBody.cursor
    if (!cursor) return seen
  }
  throw new Error('cursor never ended')
}

test('order defaults to oldest first; desc is its exact reverse; ties by id', async () => {
  const { rows, get } = await archive()
  const want = [...rows].sort(asc).map((r) => r.id)
  const def = await get({})
  assert.equal(def.jsonBody.order, 'asc')
  assert.deepEqual(def.jsonBody.items.map((i) => i.id), want)
  assert.deepEqual((await get({ order: 'asc' })).jsonBody.items.map((i) => i.id), want)
  assert.deepEqual((await get({ order: 'desc' })).jsonBody.items.map((i) => i.id), [...want].reverse())
  assert.equal((await get({ order: 'newest' })).status, 400)
})

test('cursor pages cover every match once in both orders, all months and one month, filtered', async () => {
  const { rows, get } = await archive()
  const cases = [
    [{}, rows],
    [{ month: '2026-08' }, rows.filter((r) => r.month === '2026-08')],
    [{ model: 'odd' }, rows.filter((r) => r.odd)],
  ]
  for (const [query, subset] of cases) {
    const want = [...subset].sort(asc).map((r) => r.id)
    for (const limit of [1, 2, 5, 7, 46, 47, 100]) {
      assert.deepEqual(await walk(get, { ...query, order: 'asc' }, limit), want, `asc ${JSON.stringify(query)} limit ${limit}`)
      assert.deepEqual(await walk(get, { ...query, order: 'desc' }, limit), [...want].reverse(), `desc ${JSON.stringify(query)} limit ${limit}`)
      assert.deepEqual(await walk(get, query, limit), want, `default ${JSON.stringify(query)} limit ${limit}`)
    }
  }
})

test('a cursor continues only its own order; legacy cursors (no order) were newest first', async () => {
  const { rows, get } = await archive()
  const first = (await get({ order: 'asc', limit: '5' })).jsonBody
  assert.equal((await get({ order: 'desc', limit: '5', cursor: first.cursor })).status, 400)
  assert.equal((await get({ limit: '5', cursor: 'not-a-cursor' })).status, 400)
  const want = [...rows].sort(asc).map((r) => r.id).reverse()
  const legacy = Buffer.from(JSON.stringify({ ts: rows.find((r) => r.id === want[4]).ts, id: want[4] })).toString('base64url')
  const r = await get({ order: 'desc', limit: '3', cursor: legacy })
  assert.equal(r.status, 200)
  assert.deepEqual(r.jsonBody.items.map((i) => i.id), want.slice(5, 8))
})
