import test from 'node:test'
import assert from 'node:assert/strict'
import { memoryStore } from '../../src/lib/tables.js'
import { ACCOUNT_LEDGER_VERSION, accountLedger, datesBetween, reconcileAccountUsage, surroundingDates, upsertAccountUsage } from '../../src/lib/account-usage.js'
import { validateAccountUsage, checkCaps } from '../../src/lib/validate.js'
import { computeRollup, aliasAccts } from '../../src/lib/rollup.js'
import { limiter } from '../../src/lib/pool.js'
import { handleIngest } from '../../src/lib/ingest.js'
import { handleUsage } from '../../src/lib/usage.js'
import { createInvite, handleEnroll } from '../../src/lib/enroll.js'
import { eventId } from '../../src/lib/ids.js'
import { KFP, batch, codexUsage } from '../fixtures.js'

const acct = 'a_0123456789abcdef'
const date = '2026-09-20'
const now = new Date('2026-10-02T12:00:00Z')
const snapshot = (overrides = {}) => ({ id: eventId('account-usage', 'openai', 'codex', `${acct}|UTC|${date}`),
  provider: 'openai', source: 'codex', acct, acctQ: 'recorded', date, timezone: 'UTC', totalTokens: 1000,
  observedAt: now.toISOString(), ...overrides })
const event = (overrides = {}) => ({ kind: 'usage', provider: 'openai', source: 'codex', acct,
  acctQ: 'recorded', ts: '2026-09-20T10:00:00.000Z', q: 'exact', in: 100, cacheW: 0, cacheR: 0, out: 0, ...overrides })
const roll = (date, events = []) => ({ ...computeRollup(date, events), accountLedger: accountLedger(events) })
const recovered = (result, date = '2026-09-20') => result.days.find((d) => d.date === date)?.providers.openai?.exact.unattributed || 0

test('daily snapshots require known source, recorded account, UTC and valid integer totals', () => {
  assert.equal(validateAccountUsage(snapshot(), now).ok, true)
  for (const change of [{ acct: '' }, { acctQ: 'inferred' }, { timezone: 'Europe/Berlin' }, { date: '2026-02-30' },
    { totalTokens: -1 }, { totalTokens: 1.5 }, { totalTokens: null }, { source: 'web-chatgpt' }, { observedAt: '2026-01-01' }]) {
    assert.equal(validateAccountUsage(snapshot(change), now).ok, false, JSON.stringify(change))
  }
  assert.equal(checkCaps({ accountUsage: {} }).status, 400)
  assert.equal(checkCaps({ accountUsage: Array(91).fill(snapshot()) }).status, 413)
})

test('account history subtracts matching events from every machine and preserves local attribution', () => {
  const days = surroundingDates(date).map((d) => roll(d, d === date ? [event({ machine: 'mac', in: 100 }), event({ machine: 'pc', in: 200 })] : []))
  const result = reconcileAccountUsage(days, [snapshot(), snapshot({ id: 'f'.repeat(32) })])
  assert.equal(recovered(result), 700)
  const p = result.days.find((d) => d.date === date).providers.openai
  assert.equal(p.exact.in, 300)
  assert.equal(p.byMachine.mac.in, 100)
  assert.equal(p.byMachine.pc.in, 200)
  assert.equal(p.byMachine.unknown.unattributed, 700)
  assert.equal(p.byWorkspace.unknown.exact.unattributed, 700)
  assert.equal(p.models.exact.unknown.unattributed, 700)
  assert.equal(p.accountUsage.length, 1)
  assert.equal(p.exact.calls, 0)
  assert.equal(days.find((d) => d.date === date).providers.openai.exact.unattributed, undefined, 'stored rollup not mutated')
})

test('UTC comparison covers both adjacent local dates instead of manufacturing timezone gaps', () => {
  const days = [roll('2026-09-19', [event({ ts: '2026-09-20T01:00:00.000Z', in: 400 })]),
    roll(date, [event({ ts: '2026-09-19T23:00:00.000Z', in: 900 })]),
    roll('2026-09-21', [event({ ts: '2026-09-20T23:00:00.000Z', in: 500 })])]
  assert.equal(recovered(reconcileAccountUsage(days, [snapshot()])), 100)
  assert.equal(accountLedger([event({ ts: '2026-09-21T05:00:00+07:00' })]).entries[0].date, date)
})

test('later local backfill reduces residual; local totals are never discarded when larger', () => {
  for (const [recorded, expected] of [[0, 1000], [600, 400], [1000, 0], [1300, 0]]) {
    const days = surroundingDates(date).map((d) => roll(d, d === date ? [event({ in: recorded })] : []))
    const result = reconcileAccountUsage(days, [snapshot()])
    assert.equal(recovered(result), expected)
    assert.equal(result.days.find((d) => d.date === date).providers.openai.exact.in, recorded)
  }
})

test('different account/source never counted as matching and unknown account reserves possible overlap', () => {
  const events = [event({ in: 100 }), event({ in: 200, acct: '' }), event({ in: 300, acct: 'a_fedcba9876543210' }),
    event({ in: 400, source: 'web-chatgpt' })]
  const days = surroundingDates(date).map((d) => roll(d, d === date ? events : []))
  const result = reconcileAccountUsage(days, [snapshot()])
  assert.equal(recovered(result), 700)
  const meta = result.days.find((d) => d.date === date).providers.openai.accountUsage[0]
  assert.equal(meta.matchedTokens, 100)
  assert.equal(meta.uncertainTokens, 200)
})

test('weak attribution to another account reserves possible overlap rather than double counting', () => {
  for (const acctQ of ['lineage', 'inferred', 'unknown']) {
    const days = surroundingDates(date).map((d) => roll(d, d === date ? [
      event({ in: 300, acct: 'a_fedcba9876543210', acctQ }), event({ in: 200, acctQ }),
    ] : []))
    const result = reconcileAccountUsage(days, [snapshot()])
    assert.equal(recovered(result), 500)
    const meta = result.days.find((d) => d.date === date).providers.openai.accountUsage[0]
    assert.equal(meta.matchedTokens, 0)
    assert.equal(meta.uncertainTokens, 500, 'weak same-account attribution also reserved exactly once')
  }
})

test('missing ledger defers recovery rather than treating unscanned history as zero', () => {
  const days = surroundingDates(date).map((d) => d === date ? computeRollup(d, [event()]) : roll(d))
  const result = reconcileAccountUsage(days, [snapshot()])
  assert.equal(result.pending, 1)
  assert.equal(recovered(result), 0)
})

test('cross-day overage suppresses conflicting additions instead of fabricating a daily allocation', () => {
  const days = ['2026-09-19', date, '2026-09-21', '2026-09-22'].map((d) => roll(d, d === date ? [event({ in: 200 })] : []))
  const result = reconcileAccountUsage(days, [snapshot({ totalTokens: 100 }), snapshot({ date: '2026-09-21', totalTokens: 1000 })])
  assert.equal(result.conflicts, 1)
  assert.equal(recovered(result), 0)
  assert.equal(recovered(result, '2026-09-21'), 0)
  assert.equal(result.days.find((d) => d.date === '2026-09-21').providers.openai.accountUsage[0].status, 'conflict')
})

test('month boundaries cannot hide shifted account totals and inflate historical recovery', () => {
  const days = ['2026-08-30', '2026-08-31', '2026-09-01', '2026-09-02'].map((d) =>
    roll(d, d === '2026-09-01' ? [event({ ts: '2026-09-01T10:00:00Z', in: 200 })] : []))
  const result = reconcileAccountUsage(days, [snapshot({ date: '2026-08-31', totalTokens: 100 }),
    snapshot({ date: '2026-09-01', totalTokens: 100 })], now)
  assert.equal(result.conflicts, 1)
  assert.equal(recovered(result, '2026-08-31'), 0)
  assert.equal(result.days.find((d) => d.date === '2026-09-01').providers.openai.exact.in, 200)
})

test('local usage on an omitted provider date is included in account-window conflict detection', () => {
  const days = ['2026-09-19', date, '2026-09-21', '2026-09-22', '2026-09-23'].map((d) =>
    roll(d, d === '2026-09-21' ? [event({ ts: '2026-09-21T12:00:00Z', in: 300 })] : []))
  const result = reconcileAccountUsage(days, [snapshot({ totalTokens: 100 }), snapshot({ date: '2026-09-22', totalTokens: 1000 })])
  assert.equal(result.conflicts, 1)
  assert.equal(recovered(result), 0)
})

test('a lagging current-day snapshot cannot remove recovered historical months', () => {
  const dates = datesBetween('2026-09-19', '2026-10-03')
  const days = dates.map((d) => roll(d, d === '2026-10-02' ? [event({ ts: '2026-10-02T09:00:00Z', in: 500 })] : []))
  const result = reconcileAccountUsage(days, [snapshot(), snapshot({ date: '2026-10-02', totalTokens: 100 })], now)
  assert.equal(recovered(result), 1000)
  assert.equal(recovered(result, '2026-10-02'), 0)
  assert.equal(result.conflicts, 0)
})

test('account hashes and private UTC ledger do not appear in public payload', () => {
  const days = surroundingDates(date).map((d) => roll(d, d === date ? [event()] : []))
  const publicDays = aliasAccts(reconcileAccountUsage(days, [snapshot()]).days)
  const json = JSON.stringify(publicDays)
  assert.ok(!json.includes(acct))
  assert.ok(!json.includes('accountLedger'))
  assert.ok(json.includes('recoveredTokens'))
})

test('snapshot ETag merge keeps newest observation and accepts downward provider correction', async () => {
  const store = memoryStore()
  const lim = limiter(8)
  await Promise.all([100, 400, 200].map((totalTokens, n) => upsertAccountUsage(store, lim,
    snapshot({ totalTokens, observedAt: `2026-10-02T12:00:0${n}Z` }))))
  const rows = await store.table('accounts').list('usage')
  assert.equal(rows.length, 1)
  assert.equal(rows[0].totalTokens, 200)
  await upsertAccountUsage(store, lim, snapshot({ totalTokens: 999 }))
  assert.equal((await store.table('accounts').list('usage'))[0].totalTokens, 200)
})

test('ingest stores history, upgrades only necessary ledgers and subsequent local ingest updates recovery', async () => {
  const store = memoryStore()
  const { invite } = await createInvite(store, now, 'test')
  const enrollment = await handleEnroll({ method: 'POST', headers: {}, query: {}, now,
    body: { invite, machineLabel: 'pc', kFingerprint: KFP } }, { store })
  const ctx = { store, env: {}, rollupMinAgeMs: 0 }
  const send = (body) => handleIngest({ method: 'POST', headers: { 'x-d0m1-token': enrollment.jsonBody.token }, query: {}, now, body }, ctx)
  const read = async () => (await handleUsage({ method: 'GET', headers: {}, query: { days: 'all' }, now }, ctx)).jsonBody
  const response = await send(batch({ accountUsage: [snapshot()] }))
  assert.deepEqual(response.jsonBody.retry, [])
  assert.deepEqual(response.jsonBody.accepted, [snapshot().id])
  const first = await read()
  assert.equal(recovered(first), 1000)
  assert.equal(first.firstDate, date)
  assert.equal(first.totals.accounts, 1)
  assert.ok(!JSON.stringify(first).includes(acct))
  const initialStored = await store.table('rollups').get('r', date)
  assert.equal(JSON.parse(initialStored.data).accountLedger, undefined, 'old API cannot expose a new private ledger by spreading rollup.data')
  assert.equal(JSON.parse(initialStored.accountLedger).v, ACCOUNT_LEDGER_VERSION)
  let lists = 0
  const original = store.table.bind(store)
  store.table = (name) => {
    const table = original(name)
    if (name !== 'events') return table
    return { ...table, list: (...args) => { lists++; return table.list(...args) } }
  }
  await send(batch({ accountUsage: [snapshot()] }))
  assert.equal(lists, 0, 'already indexed history is not rescanned on every poll')
  await send(batch({ usage: [codexUsage('history-local', { acct, acctQ: 'recorded', in: 300, cacheW: 0, cacheR: 0, out: 0 })] }))
  assert.equal(recovered(await read()), 700)
  const short = (await handleUsage({ method: 'GET', headers: {}, query: { days: '13' }, now }, ctx)).jsonBody
  assert.equal(recovered(short), 700, 'range selection does not change account reconciliation')
  // A new provider day with zero reported tokens plus local overage makes
  // September ambiguous, even when that offending day is outside the query.
  await send(batch({ accountUsage: [snapshot({ date: '2026-09-18', totalTokens: 0 }), snapshot()],
    usage: [codexUsage('history-overage', { ts: '2026-09-18T10:00:00Z', acct, acctQ: 'recorded', in: 400, cacheW: 0, cacheR: 0, out: 0 })] }))
  const allConflict = await read()
  const rangeConflict = (await handleUsage({ method: 'GET', headers: {}, query: { days: '12' }, now }, ctx)).jsonBody
  assert.equal(allConflict.accountUsageConflicts, 1)
  assert.equal(recovered(allConflict), 0)
  assert.equal(recovered(rangeConflict), 0, 'hiding the overage day cannot inflate a selected range')
  // Only the new date is uploaded; all missing dates between the old and new
  // account windows must still become indexed (collector sends changed rows).
  await send(batch({ accountUsage: [snapshot({ date: '2026-08-31', totalTokens: 100 })] }))
  const extended = await read()
  assert.equal(extended.accountUsagePending, 0)
  assert.equal(extended.firstDate, '2026-08-31', 'empty neighboring ledger date is not public activity')
  assert.equal(JSON.parse((await store.table('rollups').get('r', '2026-09-10')).accountLedger).v, ACCOUNT_LEDGER_VERSION)
  // Simulate the pre-feature serializer spreading the stored public JSON.
  // Its existing account aliasing is safe; the new ledger must be absent.
  const localStored = await store.table('rollups').get('r', date)
  const legacyDay = { ...JSON.parse(localStored.data) }
  assert.ok(!Object.hasOwn(legacyDay, 'accountLedger'))
  assert.ok(!Object.hasOwn(legacyDay, 'accountLedgerVersion'))
  assert.ok(JSON.parse(localStored.accountLedger).entries.some((entry) => entry.acct === acct))
})
