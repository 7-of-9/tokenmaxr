import test from 'node:test'
import assert from 'node:assert/strict'
import { validateLimit, checkCaps } from '../../src/lib/validate.js'
import { handleLimits, upsertLimits, readLimits, stale } from '../../src/lib/limits.js'
import { memoryStore } from '../../src/lib/tables.js'
import { principal } from '../fixtures.js'

const NOW = new Date('2026-10-01T12:00:00Z')
const ID = 'a'.repeat(32)

function snap(over = {}) {
  return {
    id: ID,
    provider: 'anthropic',
    source: 'claude-code',
    acct: 'a_1539dc8e48fdf4d2',
    acctQ: 'recorded',
    plan: 'Max (20x)',
    window: 'week',
    name: 'Example Org',
    usedPercent: 60,
    resetsAt: '2026-10-06T15:00:00Z',
    observedAt: '2026-10-01T02:35:48Z',
    status: 'ok',
    ...over,
  }
}

test('validateLimit keeps a meter and rejects an email', () => {
  const ok = validateLimit(snap(), NOW)
  assert.equal(ok.ok, true)
  assert.equal(ok.value.usedPercent, 60)
  assert.equal(ok.value.resetsAt, '2026-10-06T15:00:00.000Z')
  assert.equal(validateLimit(snap({ name: 'person@example.com' }), NOW).ok, false)
  assert.equal(validateLimit(snap({ label: 'person@example.com' }), NOW).value.label, 'person@example.com')
  assert.equal(validateLimit(snap({ label: 'not-an-email' }), NOW).ok, false)
  assert.equal(validateLimit(snap({ usedPercent: 101 }), NOW).ok, false)
  assert.equal(validateLimit(snap({ window: 'Week' }), NOW).ok, false)
  const plan = validateLimit(snap({ usedPercent: undefined, resetsAt: null, window: 'plan', provider: 'xai', source: 'grok-cli' }), NOW)
  assert.equal(plan.ok, true)
  assert.equal(plan.value.usedPercent, undefined)
  assert.equal(checkCaps({ usage: [], prompts: [], limits: new Array(51).fill({}) }).status, 413)
})

test('newer limit snapshot replaces an older one, and only the owner can read them', async () => {
  const store = memoryStore()
  const lim = (fn) => fn()
  const older = validateLimit(snap({ observedAt: '2026-09-01T00:00:00Z', usedPercent: 10 }), NOW).value
  const newer = validateLimit(snap({ label: 'person@example.com' }), NOW).value
  assert.deepEqual(await upsertLimits(store, lim, [newer]), [])
  assert.deepEqual(await upsertLimits(store, lim, [older]), [])
  let items = await readLimits(store)
  assert.equal(items.length, 1)
  assert.equal(items[0].usedPercent, 60)

  const anon = await handleLimits({ method: 'GET', headers: {} }, { store, env: {} })
  assert.equal(anon.status, 401)
  const owner = await handleLimits({
    method: 'GET',
    headers: { 'x-ms-client-principal': principal('github', '7-of-9', ['anonymous', 'authenticated', 'owner']) },
  }, { store, env: {} })
  assert.equal(owner.status, 200)
  assert.equal(owner.jsonBody.items.length, 1)
  assert.equal(owner.jsonBody.items[0].name, 'Example Org')
  assert.equal(owner.jsonBody.items[0].label, 'person@example.com')
})

test('validateLimit passes the organisation through and rejects a malformed one', () => {
  const team = validateLimit(snap({ org: 'a_00112233445566ff', orgKind: 'team' }), NOW)
  assert.equal(team.ok, true)
  assert.equal(team.value.org, 'a_00112233445566ff')
  assert.equal(team.value.orgKind, 'team')
  const legacy = validateLimit(snap(), NOW).value
  assert.equal('org' in legacy, false)
  assert.equal('orgKind' in legacy, false)
  assert.equal(validateLimit(snap({ org: 'org-uuid' }), NOW).ok, false)
  assert.equal(validateLimit(snap({ orgKind: 'claude_team' }), NOW).ok, false)
  assert.equal(validateLimit(snap({ orgKind: 'personal' }), NOW).value.orgKind, 'personal')
})

test('a Team seat and the personal organisation of one login are separate rows', async () => {
  const store = memoryStore()
  const lim = (fn) => fn()
  const team = validateLimit(snap({ id: 'b'.repeat(32), org: 'a_1111111111111111', orgKind: 'team', plan: 'Team', usedPercent: 97 }), NOW).value
  const max = validateLimit(snap({ id: 'c'.repeat(32), org: 'a_2222222222222222', orgKind: 'personal', usedPercent: 23 }), NOW).value
  assert.deepEqual(await upsertLimits(store, lim, [team, max]), [])
  const items = await readLimits(store, NOW)
  assert.deepEqual(items.map((i) => [i.orgKind, i.usedPercent]).sort(), [['personal', 23], ['team', 97]])
})

test('one batch holding an id twice keeps the newest reading', async () => {
  const store = memoryStore()
  const lim = (fn) => fn()
  const a = validateLimit(snap({ observedAt: '2026-10-01T02:00:00Z', usedPercent: 10 }), NOW).value
  const b = validateLimit(snap({ observedAt: '2026-10-01T03:00:00Z', usedPercent: 20 }), NOW).value
  assert.deepEqual(await upsertLimits(store, lim, [b, a]), [])
  assert.equal((await readLimits(store, NOW))[0].usedPercent, 20)
})

test('a writer that loses the race reads again, and the newer reading stays', async () => {
  const store = memoryStore()
  const t = store.table('accounts')
  const older = validateLimit(snap({ observedAt: '2026-10-01T01:00:00Z', usedPercent: 10 }), NOW).value
  const newer = validateLimit(snap({ observedAt: '2026-10-01T03:00:00Z', usedPercent: 30 }), NOW).value
  const middle = validateLimit(snap({ observedAt: '2026-10-01T02:00:00Z', usedPercent: 20 }), NOW).value
  await upsertLimits(store, (fn) => fn(), [older])
  // This writer reads the older row; before it replaces it, a concurrent
  // ingest stores the newer reading. Its conditional write fails, it reads
  // again and leaves the newer reading in place.
  let raced = false
  const lim = async (fn) => {
    const out = await fn()
    if (!raced && out && out.rowKey === ID) {
      raced = true
      await upsertLimits(store, (f) => f(), [newer])
    }
    return out
  }
  assert.deepEqual(await upsertLimits(store, lim, [middle]), [])
  assert.equal(raced, true)
  const row = await t.get('lim', ID)
  assert.equal(JSON.parse(row.body).usedPercent, 30)
})

test('readLimits leaves out only a meter whose reset passed more than 14 days ago', async () => {
  const store = memoryStore()
  const lim = (fn) => fn()
  const now = new Date('2026-10-30T00:00:00Z')
  const rows = [
    // Reset 24 days ago: stale.
    snap({ id: '1'.repeat(32), observedAt: '2026-10-01T00:00:00Z', resetsAt: '2026-10-06T00:00:00Z' }),
    // A plan row first uploaded 40 days ago (an unchanged plan row is re-sent at most daily, and older
    // collectors never re-send it): the account is still signed in, kept.
    snap({ id: '2'.repeat(32), observedAt: '2026-09-20T00:00:00Z', resetsAt: null, window: 'plan', usedPercent: undefined }),
    // Read 29 days ago, the window resets later: kept.
    snap({ id: '3'.repeat(32), observedAt: '2026-10-01T00:00:00Z', resetsAt: '2026-11-01T00:00:00Z' }),
    // Reset 13 days ago: kept (awaiting a new reading).
    snap({ id: '4'.repeat(32), observedAt: '2026-10-10T00:00:00Z', resetsAt: '2026-10-17T00:00:00Z' }),
    // A meter with no reset time (extra usage), read 40 days ago: kept.
    snap({ id: '5'.repeat(32), observedAt: '2026-09-20T00:00:00Z', resetsAt: null, window: 'extra' }),
    // A plan row with a stray reset long past: kept.
    snap({ id: '6'.repeat(32), observedAt: '2026-09-20T00:00:00Z', resetsAt: '2026-09-21T00:00:00Z', window: 'plan', usedPercent: undefined }),
  ].map((r) => validateLimit(r, now).value)
  assert.deepEqual(await upsertLimits(store, lim, rows), [])
  const ids = (await readLimits(store, now)).map((i) => i.id[0]).sort()
  assert.deepEqual(ids, ['2', '3', '4', '5', '6'])
  assert.equal(stale({ observedAt: 'garbage', resetsAt: 'garbage', window: 'week' }, now.getTime()), false)
})
