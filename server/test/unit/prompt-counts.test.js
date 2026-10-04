import test from 'node:test'
import assert from 'node:assert/strict'
import { memoryStore } from '../../src/lib/tables.js'
import { encryptJson, parseKey } from '../../src/lib/crypto.js'
import { handlePromptCounts } from '../../src/lib/prompt-counts.js'
import { handlePrompts, INDEX_TTL_MS } from '../../src/lib/prompts.js'
import { PROMPT_INDEX } from '../../src/lib/months.js'
import { createInvite, handleEnroll } from '../../src/lib/enroll.js'
import { handleIngest } from '../../src/lib/ingest.js'
import { KFP, batch, principal, prompt, testKey } from '../fixtures.js'
import { eventId } from '../../src/lib/ids.js'
import { createHarness } from '../harness.js'

const M1 = 'm_' + '1'.repeat(16), M2 = 'm_' + '2'.repeat(16), M3 = 'm_' + '3'.repeat(16)
const privateValues = ['synthetic confidential prompt', '/synthetic/private/workspace', 'synthetic@example.test', 'synthetic-session', 'a_' + 'c'.repeat(16)]
const request = over => ({ method: 'GET', headers: {}, query: {}, params: {}, now: new Date(), ...over })
const owner = { 'x-ms-client-principal': principal('github', 'test-owner') }
const sum = map => Object.values(map).reduce((n, v) => n + v, 0)

async function fixture(items = []) {
  const store = memoryStore(), env = { PROMPT_ENC_KEY: testKey(), AGENTS_OWNER_IDS: 'github:test-owner' }
  const key = parseKey(env.PROMPT_ENC_KEY)
  for (const [id, label] of [[M1, 'Alpha'], [M2, 'Twin'], [M3, 'Twin']]) {
    await store.table('machines').create({ partitionKey: 'm', rowKey: id, label })
  }
  await store.table('accounts').create({ partitionKey: 'meta', rowKey: 'months', months: JSON.stringify(['2026-09']) })
  for (const [i, item] of items.entries()) {
    const id = eventId('prompt', 'anthropic', 'claude-code', `public-count-${i}`)
    await store.table('prompts').create({ partitionKey: '2026-09', rowKey: id, provider: 'anthropic', source: 'claude-code',
      ts: '2026-09-20T23:30:00.000Z', tzOffsetMin: 120, model: 'model-a', acctQ: 'recorded', ...item,
      enc: encryptJson(key, id, { text: privateValues[0], workspace: privateValues[1], acctLabel: privateValues[2],
        session: privateValues[3], acct: privateValues[4], machine: item.machine || '' }),
    })
  }
  return { store, env }
}

test('archive counts conserve providers, models and public machines using local dates', async () => {
  const ctx = await fixture([
    { machine: M1 }, { machine: 'Alpha' }, { machine: 'alpha', model: '' }, { machine: 'Twin', model: 'model-b' },
    { machine: M2, provider: 'openai', source: 'codex', model: 'gpt-test', ts: '2026-09-20T00:30:00.000Z', tzOffsetMin: -120 },
  ])
  const result = await handlePromptCounts(request(), ctx)
  assert.equal(result.status, 200)
  assert.equal(result.headers['Cache-Control'], 'public, max-age=60')
  const body = result.jsonBody
  assert.equal(body.source, 'archive')
  assert.equal(body.total, 5)
  assert.deepEqual(body.days.map(d => d.date), ['2026-09-19', '2026-09-21'])
  const p = body.days[1].providers.anthropic
  assert.deepEqual(p.byModel, { 'model-a': 2, 'model-b': 1, unknown: 1 })
  assert.equal(p.byMachine[M1].prompts, 2)
  assert.equal(p.byMachine.unknown.prompts, 2, 'case mismatch and ambiguous label stay unassigned')
  assert.equal(p.byMachineComplete, false)
  assert.equal(body.days[0].providers.openai.byMachine[M2].prompts, 1, 'a known public ID wins even when its label is ambiguous')
  assert.equal(body.days[0].providers.openai.byMachineComplete, true)
  for (const day of body.days) for (const group of Object.values(day.providers)) {
    assert.equal(sum(group.byModel), group.prompts)
    assert.equal(Object.values(group.byMachine).reduce((n, m) => n + m.prompts, 0), group.prompts)
    for (const m of Object.values(group.byMachine)) assert.equal(sum(m.byModel), m.prompts)
  }
})

test('public JSON is explicitly count-only, with no archive identifiers or private payload fields', async () => {
  const ctx = await fixture([{ machine: 'private-unregistered-machine' }, { machine: M1, model: '__proto__' }, { model: 'constructor' }])
  const body = (await handlePromptCounts(request(), ctx)).jsonBody
  assert.deepEqual(Object.keys(body).sort(), ['days', 'generatedAt', 'source', 'total', 'undecryptable'])
  const p = body.days[0].providers.anthropic
  assert.deepEqual(Object.keys(p).sort(), ['byMachine', 'byMachineComplete', 'byModel', 'prompts'])
  assert.equal(p.byModel.__proto__, 1)
  assert.equal(p.byModel.constructor, 1)
  for (const [id, counts] of Object.entries(p.byMachine)) {
    assert.ok([M1, M2, M3, 'unknown'].includes(id))
    assert.deepEqual(Object.keys(counts).sort(), ['byModel', 'prompts'])
  }
  const json = JSON.stringify(body)
  for (const value of [...privateValues, 'private-unregistered-machine', 'Alpha', 'Twin', ctx.env.PROMPT_ENC_KEY,
    eventId('prompt', 'anthropic', 'claude-code', 'public-count-0')]) assert.ok(!json.includes(value), value)
  for (const field of ['text', 'textPreview', 'workspace', 'acct', 'acctLabel', 'session', 'enc', 'penc']) {
    assert.ok(!json.includes(`"${field}":`), field)
  }
})

test('public and owner requests share the index; ingest and TTL refresh the same cache', async () => {
  const ctx = await fixture([{ machine: M1 }])
  const original = ctx.store.table.bind(ctx.store)
  let promptLists = 0
  ctx.store.table = name => {
    const table = original(name)
    return name !== 'prompts' ? table : { ...table, list: (...args) => { promptLists++; return table.list(...args) } }
  }
  const [counts, facets] = await Promise.all([
    handlePromptCounts(request(), ctx), handlePrompts(request({ headers: owner, params: { id: 'facets' } }), ctx),
  ])
  assert.equal(counts.jsonBody.total, 1)
  assert.equal(facets.jsonBody.total, 1)
  assert.equal(promptLists, 1)
  const { invite } = await createInvite(ctx.store, new Date(), 'test')
  const enrolled = await handleEnroll(request({ method: 'POST', body: { invite, machineLabel: 'Alpha', kFingerprint: KFP } }), ctx)
  const ingest = await handleIngest(request({ method: 'POST', headers: { 'x-d0m1-token': enrolled.jsonBody.token },
    body: batch({ prompts: [prompt('new-public-count', { machine: M1 })] }) }), ctx)
  assert.deepEqual(ingest.jsonBody.retry, [])
  assert.equal((await handlePromptCounts(request(), ctx)).jsonBody.total, 2)
  assert.equal(promptLists, 2)
  ctx.store.cache.get(PROMPT_INDEX).index.at -= INDEX_TTL_MS + 1
  assert.equal((await handlePromptCounts(request(), ctx)).jsonBody.total, 2)
  assert.equal(promptLists, 3)
})

test('an empty archive is a successful zero; undecryptable records are explicit missing coverage', async () => {
  const ctx = await fixture()
  assert.deepEqual((await handlePromptCounts(request(), ctx)).jsonBody.days, [])
  assert.equal((await handlePromptCounts(request(), ctx)).jsonBody.total, 0)
  await ctx.store.table('prompts').create({ partitionKey: '2026-09', rowKey: 'f'.repeat(32), enc: 'corrupted', provider: 'anthropic' })
  ctx.store.cache.delete(PROMPT_INDEX)
  const body = (await handlePromptCounts(request(), ctx)).jsonBody
  assert.equal(body.undecryptable, 1)
  assert.equal(body.total, 0)
  assert.equal((await handlePromptCounts(request({ method: 'POST' }), ctx)).status, 405)
  assert.equal((await handlePromptCounts(request(), { store: ctx.store, env: {} })).status, 503)
})

test('anonymous HTTP counts do not authorize owner-only prompt routes', async t => {
  const ctx = await fixture([{ machine: M1 }])
  const server = createHarness({ ...ctx, quiet: true })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  t.after(() => { server.closeAllConnections(); server.close() })
  const base = `http://127.0.0.1:${server.address().port}`
  const counts = await fetch(base + '/api/prompt-counts')
  assert.equal(counts.status, 200)
  assert.match(counts.headers.get('content-type'), /application\/json/)
  assert.equal((await counts.json()).total, 1)
  for (const path of ['/api/prompts', '/api/prompts/facets', '/api/prompts/' + 'a'.repeat(32)]) {
    assert.equal((await fetch(base + path)).status, 401)
    assert.equal((await fetch(base + path, { headers: { 'x-ms-client-principal': principal('github', 'other-person') } })).status, 403)
  }
})
