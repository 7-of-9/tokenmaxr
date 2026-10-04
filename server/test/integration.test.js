// Integration test against real Azure Table/Blob storage, isolated by a
// random AGENTS_TABLE_PREFIX; every prefixed table and blob container is
// dropped afterwards. Opt-in only:
//
//   AGENTS_IT=1 node --test test/integration.test.js
//   npm run test:it
import test from 'node:test'
import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'
import { createHarness } from './harness.js'
import { azureStore, dropPrefixed } from '../src/lib/tables.js'
import { resolveConnectionString } from '../scripts/storage-env.js'
import { client, runScenario, OWNER_IDS, SCENARIO_CTX } from './scenario.js'
import { KFP, batch, codexUsage, testKey, usage } from './fixtures.js'
import { createInvite, handleEnroll } from '../src/lib/enroll.js'
import { handleIngest } from '../src/lib/ingest.js'
import { handleUsage } from '../src/lib/usage.js'
import { ROLLUP_VERSION } from '../src/lib/rollup.js'
import { eventId } from '../src/lib/ids.js'
import { ACCOUNT_LEDGER_VERSION } from '../src/lib/account-usage.js'

const RUN = process.env.AGENTS_IT === '1' || process.argv.includes('--it')

test('API scenario on Azure storage', { skip: !RUN && 'set AGENTS_IT=1 to run', timeout: 10 * 60 * 1000 }, async (t) => {
  const connectionString = resolveConnectionString()
  const prefix = 't' + randomBytes(5).toString('hex')
  const store = azureStore({ connectionString, prefix })
  const env = { PROMPT_ENC_KEY: testKey(), AGENTS_OWNER_IDS: OWNER_IDS }
  const server = createHarness({ store, env, quiet: true, ...SCENARIO_CTX })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  t.diagnostic(`table prefix ${prefix}`)
  try {
    await runScenario(t, { call: client(`http://127.0.0.1:${server.address().port}`), store })

    await t.test('storage holds ciphertext only and pins ids', async () => {
      const rows = await store.table('prompts').list('2026-09')
      assert.equal(rows.length, 3)
      for (const r of rows) assert.ok(!JSON.stringify(r).includes('synthetic'))
      const blobRow = rows.find((r) => r.blob)
      assert.ok(blobRow && !blobRow.enc && blobRow.penc)
      const blob = await store.blobs.get(blobRow.rowKey)
      assert.ok(blob && blob.length > 30000)
      const rollups = await store.table('rollups').list('r')
      assert.deepEqual(rollups.map((r) => r.rowKey), ['2026-09-20', '2026-09-21', '2026-09-22'])
      assert.ok(rollups.every((r) => JSON.parse(r.data).v === ROLLUP_VERSION))
      // Event rows carry the first reporter and its country.
      const events = await store.table('events').list('2026-09-20')
      assert.ok(events.length > 0 && events.every((e) => /^m_[0-9a-f]{16}$/.test(e.machine) && ['DE', 'GB'].includes(e.cc)))
    })

    await t.test('batched inserts, 409 fallback and the deferred recompute on Azure', async () => {
      const { invite } = await createInvite(store, new Date(), 'test')
      const enrolled = await handleEnroll({ method: 'POST', headers: {}, query: {}, body: { invite, machineLabel: 'it', kFingerprint: KFP }, now: new Date() }, { store })
      assert.equal(enrolled.status, 200)
      const t0 = Date.now()
      const at = (s) => new Date(t0 + s * 1000)
      const day = '2026-09-24'
      const ev = (i, over = {}) => usage(`it-${i}`, { ts: `${day}T08:00:00Z`, tzOffsetMin: 0, in: 1, cacheW: 2, cacheW1h: 1, cacheR: 3, out: i, ...over })
      const send = async (usageEvents, now) => {
        const r = await handleIngest({ method: 'POST', headers: { 'x-d0m1-token': enrolled.jsonBody.token }, query: {}, body: batch({ usage: usageEvents }), now }, { store, env })
        assert.equal(r.status, 200)
        assert.deepEqual(r.jsonBody.retry, [])
        return r.jsonBody
      }
      const outOn = async (now) => {
        const u = await handleUsage({ method: 'GET', headers: {}, query: { days: 'all' }, now }, { store })
        return u.jsonBody.days.find((d) => d.date === day)?.providers.anthropic.exact
      }
      // 150 fresh ids: one transaction of 100 and one of 50; the first write
      // of the day computes its rollup at once.
      const first = Array.from({ length: 150 }, (_, i) => ev(i + 1))
      await send(first, at(0))
      assert.equal((await outOn(at(1))).out, 150 * 151 / 2)
      // Five seconds later: two ids whose rows exist behind fresh pins (the
      // transaction gets a 409 and falls back), one new id; recompute deferred.
      for (const e of first.slice(0, 2)) await store.table('eventindex').delete(e.id.slice(0, 2), e.id)
      await send([ev(1, { out: 1000 }), ev(2), ev(151)], at(5))
      assert.equal((await outOn(at(30))).out, 150 * 151 / 2)
      const settled = await outOn(at(66))
      assert.equal(settled.out, 151 * 152 / 2 - 1 + 1000)
      assert.equal(settled.events, 151)
      assert.equal(settled.cacheW1h, 151)
    })

    await t.test('account token history round trips through Azure with private UTC ledger and overlap removal', async () => {
      const now = new Date()
      const { invite } = await createInvite(store, now, 'test')
      const enrollment = await handleEnroll({ method: 'POST', headers: {}, query: {}, now,
        body: { invite, machineLabel: 'history-it', kFingerprint: KFP } }, { store })
      assert.equal(enrollment.status, 200)
      const ctx = { store, env, rollupMinAgeMs: 0 }
      const acct = 'a_abcdabcdabcdabcd'
      const day = '2026-09-26'
      const snapshot = { id: eventId('account-usage', 'openai', 'codex', `${acct}|UTC|${day}`),
        source: 'codex', provider: 'openai', acct, acctQ: 'recorded', timezone: 'UTC', date: day,
        totalTokens: 14022144768, observedAt: now.toISOString() }
      const send = (body) => handleIngest({ method: 'POST', headers: { 'x-d0m1-token': enrollment.jsonBody.token }, query: {}, body, now }, ctx)
      const response = await send(batch({ accountUsage: [snapshot], usage: [codexUsage('account-history-it', {
        ts: `${day}T10:00:00Z`, tzOffsetMin: 0, acct, acctQ: 'recorded', in: 3514059624, cacheW: 0, cacheR: 0, out: 0,
      })] }))
      assert.equal(response.status, 200)
      assert.deepEqual(response.jsonBody.retry, [])
      assert.deepEqual(response.jsonBody.rejected, [])
      const result = await handleUsage({ method: 'GET', headers: {}, query: { days: 'all' }, now }, ctx)
      assert.equal(result.status, 200)
      const provider = result.jsonBody.days.find((d) => d.date === day).providers.openai
      assert.equal(provider.exact.unattributed, 10508085144)
      assert.equal(provider.exact.in, 3514059624)
      assert.ok(!JSON.stringify(result.jsonBody).includes(acct))
      assert.ok(!JSON.stringify(result.jsonBody).includes('accountLedger'))
      const row = await store.table('rollups').get('r', day)
      assert.equal(JSON.parse(row.accountLedger).v, ACCOUNT_LEDGER_VERSION)
      assert.equal(JSON.parse(row.data).accountLedger, undefined)
      const repeat = await send(batch({ accountUsage: [snapshot] }))
      assert.deepEqual(repeat.jsonBody.retry, [])
      assert.equal((await store.table('accounts').list('usage')).length, 1)
    })
  } finally {
    server.close()
    const dropped = await dropPrefixed(connectionString, prefix)
    t.diagnostic(`dropped ${dropped.length} tables/containers: ${dropped.join(', ')}`)
  }
})
