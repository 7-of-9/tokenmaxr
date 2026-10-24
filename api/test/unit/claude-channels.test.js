import test from 'node:test'
import assert from 'node:assert/strict'
import { computeRollup, legacyShape, aliasAccts } from '../../src/lib/rollup.js'
import { validatePrompt, validateUsage } from '../../src/lib/validate.js'
import { prompt, usage, principal, testKey } from '../fixtures.js'
import { eventId } from '../../src/lib/ids.js'
import { memoryStore } from '../../src/lib/tables.js'
import { encryptJson, parseKey } from '../../src/lib/crypto.js'
import { handlePrompts } from '../../src/lib/prompts.js'
import { handlePromptCounts } from '../../src/lib/prompt-counts.js'
import { mergeEvent, sourceUpgrade } from '../../src/lib/merge.js'
import { upsertPrompt } from '../../src/lib/ingest.js'
import { limiter } from '../../src/lib/pool.js'

const sources = ['claude-code', 'web-claude', 'native-claude']

test('Claude channels retain disjoint token, model, prompt and machine totals', () => {
  const events = sources.flatMap((source, i) => [
    { ...usage(`channel-${i}`, { source, in: 10 + i, cacheW: 20, cacheR: 30, out: 40 + i }), kind: 'usage', machine: `machine-${i}`, cc: 'TH' },
    { kind: 'activity', provider: 'anthropic', source, hasUsage: i !== 1, machine: `machine-${i}`, cc: 'TH' },
  ])
  for (const event of events.filter(e => e.kind === 'usage')) assert.equal(validateUsage(event, new Date()).ok, true)
  const day = computeRollup('2026-10-22', events)
  assert.equal(day.v, 4)
  const p = day.providers.anthropic
  assert.deepEqual(Object.keys(p.bySource).sort(), [...sources].sort())
  for (const key of ['in', 'cacheW', 'cacheR', 'out', 'events']) {
    assert.equal(Object.values(p.bySource).reduce((n, s) => n + s.exact[key], 0), p.exact[key])
  }
  assert.equal(p.bySource['web-claude'].promptsNoUsage, 1)
  assert.equal(p.bySource['native-claude'].byMachine['machine-2'].out, 42)
  assert.deepEqual(aliasAccts([day])[0].providers.anthropic.bySource, p.bySource)
  // Until an old rollup is rebuilt, keep its existing model/workspace data.
  const old = { ...day, v: 3 }
  delete old.providers.anthropic.bySource
  assert.deepEqual(legacyShape(old), old)
})

test('owner prompt filters and public model counts isolate each Claude channel', async () => {
  const store = memoryStore()
  const key64 = testKey(), key = parseKey(key64)
  for (const [i, source] of sources.entries()) {
    const id = eventId('prompt', 'anthropic', source, 'same-upstream-id')
    await store.table('prompts').create({ partitionKey: '2026-10', rowKey: id, ts: '2026-10-22T08:00:00Z', tzOffsetMin: 0,
      provider: 'anthropic', source, model: `model-${i}`, acctQ: 'recorded', blob: false,
      enc: encryptJson(key, id, { text: `private-${i}`, workspace: '', acct: '', acctLabel: '', machine: '', session: '' }) })
  }
  await store.table('accounts').create({ partitionKey: 'meta', rowKey: 'months', months: '["2026-10"]' })
  const ctx = { store, env: { PROMPT_ENC_KEY: key64 } }
  const owner = { 'x-ms-client-principal': principal('github', 'local-test', ['owner']) }
  for (const [i, source] of sources.entries()) {
    const req = { method: 'GET', headers: owner, query: { source, facets: '1' }, params: {} }
    const privateResponse = await handlePrompts(req, ctx)
    assert.equal(privateResponse.status, 200)
    assert.equal(privateResponse.jsonBody.items.length, 1)
    assert.equal(privateResponse.jsonBody.items[0].source, source)
    assert.equal(privateResponse.jsonBody.facets.sources.length, 3)
    const counts = await handlePromptCounts({ ...req, headers: {} }, ctx)
    assert.equal(counts.jsonBody.total, 1)
    assert.deepEqual(counts.jsonBody.days[0].providers.anthropic.byModel, { [`model-${i}`]: 1 })
    assert.ok(!JSON.stringify(counts.jsonBody).includes('private-'))
    assert.equal((await handlePrompts({ ...req, headers: {} }, ctx)).status, 401)
  }
  assert.equal((await handlePromptCounts({ method: 'GET', query: { source: 'invalid' } }, ctx)).status, 400)
})

// Claude chat (SPEC "Claude chat"): one id per turn in every channel, all
// estimated; a live record (pv 2) replaces a swept inference (pv 1).
const chatId = (kind, key) => eventId(kind, 'anthropic', 'claude-chat', key)

test('a live web turn replaces the swept Native inference of the same turn, tokens and reporter included', async () => {
  const { mergeEvent } = await import('../../src/lib/merge.js')
  const now = new Date('2026-10-23T12:00:00Z')
  const id = chatId('usage', 'msg:a0a0aaaa-0000-4000-8000-000000000002')
  const swept = { ...validateUsage(usage('x', { id, source: 'native-claude', q: 'estimated', pv: 1, in: 900, out: 40, cacheR: 0, cacheW: 0, ts: '2026-10-23T07:39:09.000Z' }), now).value, machine: 'sweeper', cc: 'TH' }
  const live = { ...validateUsage(usage('x', { id, source: 'web-claude', q: 'estimated', pv: 2, in: 1000, out: 50, cacheR: 0, cacheW: 0, ts: '2026-10-23T07:39:09.000Z' }), now).value, machine: 'laptop', cc: 'GB' }
  assert.ok(swept && live, 'both validate')
  for (const [first, second] of [[swept, live], [live, swept]]) {
    const merged = mergeEvent(mergeEvent(null, first), second)
    assert.equal(merged.source, 'web-claude')
    assert.equal(merged.in, 1000)
    assert.equal(merged.q, 'estimated')
    assert.equal(merged.machine, 'laptop')
    const p = computeRollup('2026-10-23', [{ ...merged, kind: 'usage' }]).providers.anthropic
    assert.equal(p.bySource['web-claude'].estimated.in, 1000)
    assert.equal(p.bySource['native-claude'], undefined)
    assert.equal(p.exact.in, 0)
  }
})

test('a prompt keeps the best evidence of its client whatever the order', async () => {
  const { mergeEvent } = await import('../../src/lib/merge.js')
  const base = { kind: 'activity', provider: 'anthropic', ts: '2026-10-23T07:39:07.000Z', hasUsage: true, acct: '', acctQ: 'unknown', session: '', ws: '' }
  const web = { ...base, source: 'web-claude', machine: 'laptop' }
  const app = { ...base, source: 'native-claude', machine: 'sweeper' }
  const old = { ...base, source: 'chat-claude', machine: 'sweeper' }
  assert.equal(mergeEvent(app, web).source, 'web-claude')
  assert.equal(mergeEvent(app, web).machine, 'laptop')
  assert.equal(mergeEvent(web, app).source, 'web-claude')
  assert.equal(mergeEvent(old, app).source, 'native-claude')
  // Other sources keep the first reporter's channel.
  assert.equal(mergeEvent({ ...base, source: 'claude-code' }, web).source, 'claude-code')
})

test('chat-claude is accepted and kept apart as estimated tokens', () => {
  const now = new Date('2026-10-23T12:00:00Z')
  const ev = validateUsage(usage('y', { id: chatId('usage', 'msg:y'), source: 'chat-claude', q: 'estimated', pv: 1, ts: '2026-09-01T00:00:00.000Z' }), now)
  assert.equal(ev.ok, true)
  assert.equal(validateUsage(usage('z', { source: 'chat-claude', provider: 'openai' }), now).ok, false)
  const day = computeRollup('2026-09-01', [{ ...ev.value, kind: 'usage' }, { kind: 'activity', provider: 'anthropic', source: 'chat-claude', hasUsage: true }])
  const g = day.providers.anthropic.bySource['chat-claude']
  assert.equal(g.estimated.in, 100)
  assert.equal(g.exact.in, 0)
  assert.equal(g.prompts, 1)
  assert.equal(g.promptsNoUsage, 0)
})

// Claude Native (SPEC "Claude Native"): a ~/.claude line the desktop app wrote
// keeps its claude-code id; collector pv 3 sends it as native-claude.
test('a desktop-app line re-sent as native-claude (pv 3) moves channel; an older claude-code copy never moves it back', () => {
  const now = new Date('2026-10-23T12:00:00Z')
  const code = { ...validateUsage(usage('desk', { pv: 2, in: 5, cacheW: 100, cacheR: 1000, out: 50 }), now).value, machine: 'rabbit', cc: 'TH' }
  const native = { ...validateUsage(usage('desk', { pv: 3, source: 'native-claude', in: 5, cacheW: 100, cacheR: 1000, out: 50 }), now).value, machine: 'rabbit', cc: 'TH' }
  assert.equal(code.id, native.id)
  for (const [first, second] of [[code, native], [native, code]]) {
    const merged = mergeEvent(mergeEvent(null, first), second)
    assert.equal(merged.source, 'native-claude')
    assert.equal(merged.q, 'exact')
    assert.equal(merged.pv, 3)
    assert.equal(merged.cacheR, 1000)
    const p = computeRollup('2026-09-20', [{ ...merged, kind: 'usage' }]).providers.anthropic
    assert.equal(p.bySource['native-claude'].exact.cacheR, 1000)
    assert.equal(p.bySource['claude-code'], undefined)
  }
  const base = { kind: 'activity', provider: 'anthropic', ts: '2026-09-20T09:59:00.000Z', hasUsage: true, acct: '', acctQ: 'unknown', session: '', ws: '', machine: 'rabbit' }
  assert.equal(mergeEvent({ ...base, source: 'claude-code' }, { ...base, source: 'native-claude' }).source, 'native-claude')
  assert.equal(mergeEvent({ ...base, source: 'native-claude' }, { ...base, source: 'claude-code' }).source, 'native-claude')
  assert.equal(sourceUpgrade('native-claude', 'claude-code'), false)
  assert.equal(sourceUpgrade('claude-code', 'web-claude'), false)
})

test('a prompt row moves from claude-code to native-claude without rewriting its ciphertext', async () => {
  const store = memoryStore()
  const key = parseKey(testKey())
  const lim = limiter(4)
  const t = store.table('prompts')
  const valid = (over) => {
    const r = validatePrompt(prompt('desk', over), new Date('2026-09-29T12:00:00Z'))
    assert.ok(r.ok, r.error)
    return r.value
  }
  const month = await upsertPrompt(store, lim, key, valid({}))
  let row = await t.get(month, valid({}).id)
  assert.equal(row.source, 'claude-code')
  const enc = row.enc
  await upsertPrompt(store, lim, key, valid({ source: 'native-claude', text: 'other' }))
  row = await t.get(month, row.rowKey)
  assert.equal(row.source, 'native-claude')
  assert.equal(row.enc, enc)
  await upsertPrompt(store, lim, key, valid({}))
  row = await t.get(month, row.rowKey)
  assert.equal(row.source, 'native-claude')
})

test('usage of the same pv moves channel with its activity, whatever the order', () => {
  const base = { provider: 'anthropic', ts: '2026-10-23T07:39:09.000Z', acct: '', acctQ: 'unknown', session: '', ws: '', q: 'estimated', pv: 1, in: 900, out: 40, cacheR: 0, cacheW: 0 }
  for (const [a, b, want] of [['chat-claude', 'native-claude', 'native-claude'], ['native-claude', 'web-claude', 'web-claude'], ['chat-claude', 'web-claude', 'web-claude'], ['web-claude', 'chat-claude', 'web-claude'], ['native-claude', 'chat-claude', 'native-claude']]) {
    const u = mergeEvent({ ...base, kind: 'usage', source: a, machine: 'm1' }, { ...base, kind: 'usage', source: b, machine: 'm2' })
    const act = mergeEvent({ ...base, kind: 'activity', source: a, hasUsage: true, machine: 'm1' }, { ...base, kind: 'activity', source: b, hasUsage: true, machine: 'm2' })
    assert.equal(u.source, want, `usage ${a} then ${b}`)
    assert.equal(act.source, want, `activity ${a} then ${b}`)
    assert.equal(u.machine, act.machine, `reporter ${a} then ${b}`)
    assert.equal(u.machine, want === a ? 'm1' : 'm2')
    assert.equal(u.in, 900)
  }
})
