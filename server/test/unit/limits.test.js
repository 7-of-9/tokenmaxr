import test from 'node:test'
import assert from 'node:assert/strict'
import { validateLimit, checkCaps } from '../../src/lib/validate.js'
import { handleLimits, upsertLimits, readLimits } from '../../src/lib/limits.js'
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
