// A busy day's rollup can exceed Azure Table's 64 KiB string-property limit.
// The memory store does not enforce that limit, so these tests wrap the
// rollups table to reject oversized strings and to apply `select` like Azure.
import test from 'node:test'
import assert from 'node:assert/strict'
import { memoryStore } from '../../src/lib/tables.js'
import { createInvite, handleEnroll } from '../../src/lib/enroll.js'
import { handleIngest } from '../../src/lib/ingest.js'
import { handleUsage } from '../../src/lib/usage.js'
import { joinRollupData, splitRollupData, ROLLUP_CHUNK_CHARS, ROLLUP_VERSION } from '../../src/lib/rollup.js'
import { KFP, batch, usage } from '../fixtures.js'

const DATE = '2026-09-20'
// Azure: a String property holds at most 64 KiB of UTF-16.
const AZURE_STRING_CHARS = 32 * 1024
const httpErr = (status, message) => Object.assign(new Error(message), { statusCode: status })

function azureLimits(store) {
  const real = store.table.bind(store)
  let eventLists = 0
  const check = (entity) => {
    for (const [k, v] of Object.entries(entity)) {
      if (typeof v === 'string' && v.length > AZURE_STRING_CHARS) throw httpErr(400, `PropertyValueTooLarge: ${k}`)
    }
  }
  store.table = (name) => {
    const t = real(name)
    if (name === 'events') return { ...t, list: (...a) => (eventLists++, t.list(...a)) }
    if (name !== 'rollups') return t
    return {
      ...t,
      create: async (e) => (check(e), t.create(e)),
      replace: async (e, etag) => (check(e), t.replace(e, etag)),
      list: async (pk, opts = {}) => {
        const rows = await t.list(pk, opts)
        if (!opts.select) return rows
        const keep = new Set(['partitionKey', 'rowKey', 'etag', ...opts.select])
        return rows.map((r) => Object.fromEntries(Object.entries(r).filter(([k]) => keep.has(k))))
      },
    }
  }
  return { lists: () => eventLists }
}

async function enrolled(store) {
  const { invite } = await createInvite(store, new Date(), 'test')
  const r = await handleEnroll({ method: 'POST', headers: {}, query: {}, body: { invite, machineLabel: 't', kFingerprint: KFP }, now: new Date() }, { store })
  assert.equal(r.status, 200)
  return r.jsonBody.token
}

const usageOn = async (store, now, days = 'all') =>
  (await handleUsage({ method: 'GET', headers: {}, query: { days }, now }, { store })).jsonBody

test('split and join round-trip, never splitting a surrogate pair', () => {
  const emoji = '\u{1F600}'
  const json = JSON.stringify({ a: 'x'.repeat(ROLLUP_CHUNK_CHARS - 6) + emoji + 'y'.repeat(70000) })
  const props = splitRollupData(json)
  assert.ok(Object.keys(props).length >= 3)
  for (const v of Object.values(props)) {
    assert.ok(v.length <= ROLLUP_CHUNK_CHARS)
    assert.ok(!/^[\uDC00-\uDFFF]/.test(v), 'no chunk starts with a low surrogate')
  }
  assert.equal(joinRollupData(props), json)
  assert.deepEqual(splitRollupData('{}'), { data: '{}' })
  assert.equal(joinRollupData({ data: '{"v":3}' }), '{"v":3}')
})

test('an oversized day upgrades once, is served at the current version, and is read back through select', async () => {
  const store = memoryStore()
  const token = await enrolled(store)
  const t0 = Date.parse(`${DATE}T12:00:00Z`)
  const at = (s) => new Date(t0 + s * 1000)
  // Enough workspaces to put this day's rollup well past one string property.
  const events = Array.from({ length: 90 }, (_, i) =>
    usage(`big${i}`, { ws: `w_${i.toString(16).padStart(16, '0')}`, model: `claude-test-${i % 9}` }))
  const res = await handleIngest({ method: 'POST', headers: { 'x-d0m1-token': token }, query: {}, body: batch({ usage: events }), now: at(-300) }, { store, env: {} })
  assert.equal(res.status, 200, JSON.stringify(res.jsonBody))

  // Production state before the fix: a v3 rollup that fit, whose v4 does not.
  const rollups = store.table('rollups')
  const row = await rollups.get('r', DATE)
  const v4 = JSON.parse(joinRollupData(row))
  assert.ok(JSON.stringify(v4).length > AZURE_STRING_CHARS, `fixture is oversized (${JSON.stringify(v4).length})`)
  const v3 = { ...v4, v: 3, providers: Object.fromEntries(Object.entries(v4.providers).map(([k, { bySource: _s, ...p }]) => [k, p])) }
  const { etag: _e, ...old } = row
  for (const k of Object.keys(old)) if (/^data\d+$/.test(k)) delete old[k]
  await rollups.replace({ ...old, data: JSON.stringify(v3) }, '*')

  const limits = azureLimits(store)
  const first = await usageOn(store, at(0))
  const day = first.days.find((d) => d.date === DATE)
  assert.equal(day.v, ROLLUP_VERSION, 'upgraded in the first read')
  assert.equal(day.providers.anthropic.bySource, undefined, 'the response is by provider')
  assert.equal(limits.lists(), 1, 'one listing for the upgrade')

  const stored = await store.table('rollups').get('r', DATE)
  assert.ok(JSON.parse(joinRollupData(stored)).providers.anthropic.bySource['claude-code'], 'the stored v4 keeps its source split')
  assert.ok(typeof stored.data1 === 'string', 'stored across properties')
  for (const [k, v] of Object.entries(stored)) if (typeof v === 'string') assert.ok(v.length <= AZURE_STRING_CHARS, k)

  await usageOn(store, at(60))
  assert.equal(limits.lists(), 1, 'a settled day is not recomputed on later reads')

  // A range that starts after DATE reads it only through the `earlier` select.
  const later = await usageOn(store, new Date(t0 + 5 * 86400000), '1')
  assert.equal(later.firstDate, DATE)
  assert.equal(limits.lists(), 1)
})
