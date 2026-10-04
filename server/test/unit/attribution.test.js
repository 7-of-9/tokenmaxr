// Account-attribution qualities (SPEC "Accounts"): the merge rank order, the
// new acctQ values on the wire, labels only from rank 3 up, prompt rows
// upgraded in place by a better attribution, and the archive's account facets.
import test from 'node:test'
import assert from 'node:assert/strict'
import { memoryStore } from '../../src/lib/tables.js'
import { decryptJson, parseKey } from '../../src/lib/crypto.js'
import { ACCT_RANK, acctRank, labelled, mergeEvent } from '../../src/lib/merge.js'
import { ACCT_Q, validateActivity, validatePrompt, validateUsage } from '../../src/lib/validate.js'
import { storedPromptQ, upsertPrompt } from '../../src/lib/ingest.js'
import { acctKeyOf, handlePrompts } from '../../src/lib/prompts.js'
import { addMonths } from '../../src/lib/months.js'
import { limiter } from '../../src/lib/pool.js'
import { ACCT, activity, bigText, principal, prompt, testKey, usage } from '../fixtures.js'
import { OWNER_IDS } from '../scenario.js'

const NOW = new Date('2026-09-29T12:00:00Z')
const ORDER = ['recorded', 'session', 'timeline', 'bounded', 'lineage', 'inferred', 'unknown']
const OTHER = 'a_' + 'f'.repeat(16)

const norm = (x) => {
  const r = validateUsage(x, NOW)
  assert.ok(r.ok, r.error)
  const { tsMs, ...ev } = r.value
  return ev
}

test('rank order: recorded > session > timeline > bounded > lineage > inferred > unknown', () => {
  assert.deepEqual(ACCT_Q, ORDER)
  assert.deepEqual(ORDER.map(acctRank), [6, 5, 4, 3, 2, 1, 0])
  assert.deepEqual(Object.keys(ACCT_RANK).sort(), [...ORDER].sort())
  assert.equal(acctRank('guess'), 0)
  assert.deepEqual(ORDER.filter(labelled), ['recorded', 'session', 'timeline', 'bounded'])
})

test('merge keeps the higher-ranked account in either order, ties by hash', () => {
  for (let i = 0; i < ORDER.length; i++) {
    for (let j = i + 1; j < ORDER.length - 1; j++) {
      const hi = norm(usage('r1', { acct: ACCT.anthropic, acctQ: ORDER[i] }))
      const lo = norm(usage('r1', { acct: OTHER, acctQ: ORDER[j] }))
      for (const m of [mergeEvent(hi, lo), mergeEvent(lo, hi)]) {
        assert.equal(m.acct, ACCT.anthropic, `${ORDER[i]} beats ${ORDER[j]}`)
        assert.equal(m.acctQ, ORDER[i])
      }
    }
  }
  // Recorded upgrades an earlier timeline row (the heimdall window case).
  const old = norm(usage('r2', { acct: OTHER, acctQ: 'timeline' }))
  const now = norm(usage('r2', { acct: ACCT.anthropic, acctQ: 'recorded' }))
  assert.equal(mergeEvent(old, now).acct, ACCT.anthropic)
  // Equal quality: the smaller hash wins regardless of arrival order.
  const a = norm(usage('r3', { acct: ACCT.anthropic, acctQ: 'bounded' }))
  const b = norm(usage('r3', { acct: OTHER, acctQ: 'bounded' }))
  const win = ACCT.anthropic < OTHER ? ACCT.anthropic : OTHER
  assert.equal(mergeEvent(a, b).acct, win)
  assert.equal(mergeEvent(b, a).acct, win)
  // Activity follows the same order.
  const r = (q, acct) => {
    const v = validateActivity(activity('r4', { acct, acctQ: q }), NOW).value
    delete v.tsMs
    return v
  }
  assert.equal(mergeEvent(r('lineage', OTHER), r('bounded', ACCT.anthropic)).acctQ, 'bounded')
  assert.equal(mergeEvent(r('recorded', OTHER), r('session', ACCT.anthropic)).acctQ, 'recorded')
})

test('validation accepts the new qualities and rejects unknown ones', () => {
  for (const q of ORDER) {
    assert.equal(validateUsage(usage('v1', { acctQ: q }), NOW).value.acctQ, q)
    assert.equal(validateActivity(activity('v1', { acctQ: q }), NOW).value.acctQ, q)
    assert.equal(validatePrompt(prompt('v1', { acctQ: q }), NOW).value.acctQ, q)
  }
  for (const q of ['guess', 'exact', 'Recorded', 'probable']) {
    assert.equal(validateUsage(usage('v1', { acctQ: q }), NOW).ok, false)
    assert.equal(validatePrompt(prompt('v1', { acctQ: q }), NOW).ok, false)
  }
})

test('prompts: labels only from rank 3; legacy prompts derive their acctQ', () => {
  const v = (over) => validatePrompt(prompt('l1', over), NOW).value
  for (const q of ['recorded', 'session', 'timeline', 'bounded']) assert.equal(v({ acctQ: q }).acctLabel, 'synthetic-label')
  for (const q of ['lineage', 'inferred', 'unknown']) assert.equal(v({ acctQ: q }).acctLabel, '', q)
  // No acctQ: a pre-v1.4 collector labelled only timeline/session attributions.
  assert.equal(v({}).acctQ, 'timeline')
  assert.equal(v({ acctLabel: '' }).acctQ, 'inferred')
  assert.equal(v({ acct: '', acctLabel: '' }).acctQ, 'unknown')
  assert.equal(v({ acct: '' }).acctLabel, '', 'no account, no label')
})

function promptsCtx() {
  const store = memoryStore()
  const b64 = testKey()
  return { store, b64, key: parseKey(b64), lim: limiter(4), t: store.table('prompts') }
}

const valid = (over) => {
  const r = validatePrompt(prompt('up', { ts: '2026-09-20T09:59:00.000Z', ...over }), NOW)
  assert.ok(r.ok, r.error)
  return r.value
}

test('upsertPrompt: a higher acctQ re-encrypts acct and label; equal or lower never rewrites', async () => {
  const { store, key, lim, t } = promptsCtx()
  const month = await upsertPrompt(store, lim, key, valid({ acct: OTHER, acctQ: 'inferred', acctLabel: 'ignored', model: '' }))
  let row = await t.get(month, valid({}).id)
  assert.equal(row.acctQ, 'inferred')
  let payload = decryptJson(key, row.rowKey, row.enc)
  assert.equal(payload.acct, OTHER)
  assert.equal(payload.acctLabel, '')
  // Equal rank: untouched except the model fill.
  const enc0 = row.enc
  await upsertPrompt(store, lim, key, valid({ acct: ACCT.anthropic, acctQ: 'inferred', model: 'm1' }))
  row = await t.get(month, row.rowKey)
  assert.equal(row.enc, enc0)
  assert.equal(row.model, 'm1')
  // Higher rank: the account and label change; text, workspace, machine stay.
  await upsertPrompt(store, lim, key, valid({ acct: ACCT.anthropic, acctQ: 'recorded', acctLabel: 'new-label', text: 'other text', workspace: '/other', model: 'm2' }))
  row = await t.get(month, row.rowKey)
  assert.equal(row.acctQ, 'recorded')
  assert.equal(row.model, 'm1', 'a filled model stands')
  payload = decryptJson(key, row.rowKey, row.enc)
  assert.equal(payload.acct, ACCT.anthropic)
  assert.equal(payload.acctLabel, 'new-label')
  assert.equal(payload.text, 'synthetic prompt up')
  assert.equal(payload.workspace, '/synthetic/workspace')
  assert.equal(payload.machine, 'test-machine')
  // Lower rank later: never downgraded.
  const enc1 = row.enc
  await upsertPrompt(store, lim, key, valid({ acct: OTHER, acctQ: 'timeline', acctLabel: 'old-label' }))
  row = await t.get(month, row.rowKey)
  assert.equal(row.enc, enc1)
  assert.equal(row.acctQ, 'recorded')
})

test('upsertPrompt: legacy rows (no acctQ column) rank by their payload; blob rows upgrade too', async () => {
  const { store, key, lim, t } = promptsCtx()
  // A legacy labelled row counts as timeline: bounded does not replace it, recorded does.
  const legacy = valid({ acctLabel: 'legacy-label' })
  delete legacy.acctQ
  const month = await upsertPrompt(store, lim, key, { ...legacy, acctQ: undefined })
  let row = await t.get(month, legacy.id)
  assert.equal(row.acctQ, undefined)
  assert.equal(storedPromptQ(key, row), 'timeline')
  const enc0 = row.enc
  await upsertPrompt(store, lim, key, valid({ acctQ: 'bounded', acctLabel: 'b-label' }))
  assert.equal((await t.get(month, legacy.id)).enc, enc0)
  await upsertPrompt(store, lim, key, valid({ acctQ: 'recorded', acctLabel: 'r-label' }))
  row = await t.get(month, legacy.id)
  assert.equal(row.acctQ, 'recorded')
  assert.equal(decryptJson(key, row.rowKey, row.enc).acctLabel, 'r-label')

  // Blob row: full ciphertext and the row's preview both carry the new label.
  const big = bigText(40000)
  const first = validatePrompt(prompt('bigup', { acct: OTHER, acctQ: 'lineage', text: big }), NOW).value
  const m = await upsertPrompt(store, lim, key, first)
  row = await t.get(m, first.id)
  assert.equal(row.blob, true)
  const better = validatePrompt(prompt('bigup', { acctQ: 'bounded', acctLabel: 'blob-label', text: big }), NOW).value
  await upsertPrompt(store, lim, key, better)
  row = await t.get(m, first.id)
  assert.equal(row.acctQ, 'bounded')
  const preview = decryptJson(key, first.id, row.penc)
  assert.equal(preview.acctLabel, 'blob-label')
  assert.equal(preview.acct, ACCT.anthropic)
  assert.equal(preview.truncated, true)
  const full = decryptJson(key, first.id, await store.blobs.get(first.id))
  assert.equal(full.acctLabel, 'blob-label')
  assert.equal(full.text, big)
})

test('upsertPrompt: a concurrent write (stale ETag) is re-read and the upgrade retried', async () => {
  const { store, key, lim, t } = promptsCtx()
  const month = await upsertPrompt(store, lim, key, valid({ acctQ: 'lineage' }))
  const id = valid({}).id
  // Wrap the table so the first conditional merge loses a race.
  const real = store.table.bind(store)
  let raced = false
  store.table = (name) => {
    const tb = real(name)
    if (name !== 'prompts') return tb
    return {
      ...tb,
      async merge(entity, etag) {
        if (!raced) {
          raced = true
          await tb.merge({ partitionKey: entity.partitionKey, rowKey: entity.rowKey, model: 'racer' }, '*')
        }
        return tb.merge(entity, etag)
      },
    }
  }
  await upsertPrompt(store, lim, key, valid({ acctQ: 'session', acctLabel: 'won' }))
  const row = await t.get(month, id)
  assert.equal(row.acctQ, 'session')
  assert.equal(row.model, 'racer')
  assert.equal(decryptJson(key, id, row.enc).acctLabel, 'won')
})

test('facet keys: per account and quality when labelled, else unattributed', () => {
  assert.equal(acctKeyOf('anthropic', ACCT.anthropic, 'l', 'recorded'), `${ACCT.anthropic}~recorded`)
  assert.equal(acctKeyOf('anthropic', ACCT.anthropic, 'l', 'bounded'), `${ACCT.anthropic}~bounded`)
  assert.equal(acctKeyOf('anthropic', '', 'l', 'timeline'), 'label:anthropic:l~timeline')
  assert.equal(acctKeyOf('anthropic', ACCT.anthropic, 'l', 'lineage'), 'unknown:anthropic')
  assert.equal(acctKeyOf('anthropic', ACCT.anthropic, '', 'inferred', 'l'), `unknown:anthropic:${ACCT.anthropic}`)
  assert.equal(acctKeyOf('anthropic', ACCT.anthropic, '', 'inferred', ''), 'unknown:anthropic')
  assert.equal(acctKeyOf('openai', '', '', 'unknown'), 'unknown:openai')
  // A strong attribution to an account with no label is its own (unnamed) group.
  assert.equal(acctKeyOf('anthropic', ACCT.anthropic, '', 'bounded'), `${ACCT.anthropic}~bounded`)
  assert.equal(acctKeyOf('cursor', ACCT.anthropic, '', 'recorded', 'l'), `${ACCT.anthropic}~recorded`)
})

test('archive facets: qualities, "probably <label>" and the unknown bucket', async () => {
  const { store, b64, key, lim } = promptsCtx()
  const env = { PROMPT_ENC_KEY: b64, AGENTS_OWNER_IDS: OWNER_IDS }
  const p = (k, over) => validatePrompt(prompt(k, over), NOW).value
  const rows = [
    p('f1', { acctQ: 'recorded', ts: '2026-09-20T10:00:00Z' }),
    p('f2', { acctQ: 'recorded', ts: '2026-09-20T11:00:00Z' }),
    p('f3', { acctQ: 'bounded', ts: '2026-09-19T10:00:00Z' }),
    p('f4', { acctQ: 'lineage', ts: '2026-09-18T10:00:00Z' }),
    p('f5', { acctQ: 'inferred', ts: '2026-09-17T10:00:00Z' }),
    p('f6', { acct: OTHER, acctQ: 'inferred', ts: '2026-09-16T10:00:00Z' }),
    p('f7', { acct: '', acctQ: 'unknown', ts: '2026-09-15T10:00:00Z' }),
  ]
  for (const r of rows) await upsertPrompt(store, lim, key, r)
  await addMonths(store, lim, ['2026-09'])
  const owner = { 'x-ms-client-principal': principal('github', 'test-owner') }
  const get = (query, params = {}) => handlePrompts({ method: 'GET', headers: owner, query, params, now: NOW }, { store, env })
  const f = (await get({}, { id: 'facets' })).jsonBody
  assert.deepEqual(f.accounts.map(({ acct, label, quality, count, unattributed, probable }) => [acct, label, quality, count, unattributed, probable]), [
    [`${ACCT.anthropic}~recorded`, 'synthetic-label', 'recorded', 2, false, undefined],
    [`${ACCT.anthropic}~bounded`, 'synthetic-label', 'bounded', 1, false, undefined],
    [`unknown:anthropic:${ACCT.anthropic}`, '', '', 2, true, 'synthetic-label'],
    ['unknown:anthropic', '', '', 2, true, undefined],
  ])
  const list = (await get({ acct: `unknown:anthropic:${ACCT.anthropic}`, order: 'desc' })).jsonBody.items
  assert.deepEqual(list.map((i) => [i.acctQ, i.acctLabel, i.acctProbable]), [['lineage', '', 'synthetic-label'], ['inferred', '', 'synthetic-label']])
  const unk = (await get({ acct: 'unknown:anthropic', order: 'desc' })).jsonBody.items
  assert.deepEqual(unk.map((i) => [i.acctQ, i.acctProbable]), [['inferred', ''], ['unknown', '']])
  const rec = (await get({ acct: `${ACCT.anthropic}~recorded` })).jsonBody.items
  assert.ok(rec.every((i) => i.acctQ === 'recorded' && i.acctLabel === 'synthetic-label'))
})
