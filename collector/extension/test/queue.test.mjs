import test from 'node:test'
import assert from 'node:assert/strict'
import {
  sanitize, mergeQueue, settleQueue, decideSweep, finishSweep, SWEEP_EVERY, SWEEP_AGAIN, MAX_QUEUE, SWEEP_ROOM, MAX_SENT,
  expireText, pickBatch, shrinkRecord, cutText, byteLength, TEXT_TTL, MAX_QUEUE_TEXT, MAX_TEXT_BYTES, MAX_BATCH_BYTES,
} from '../queue.js'
import { ORG, CONV, id } from './fixtures.mjs'

const rec = (n, extra = {}) => ({
  v: 1, kind: 'live', platform: 'web_claude_ai', org: ORG, conv: CONV, human: id(n), assistant: id(n + 1),
  ts: '2026-10-23T07:39:09Z', promptTs: '2026-10-23T07:39:07Z', model: 'claude-fable-5-1', chatPlatform: 'CLAUDE_AI',
  stop: 'end_turn', calls: 1, thinkingMs: 256, est: { in: 10, out: 2, ctx: 2500, algo: 'chars-v1' }, text: 'hello', ...extra,
})
const sweep = (n) => ({ ...rec(n), kind: 'sweep' })

test('sanitize keeps the schema, stamps the version and keeps text only as allowed per kind', () => {
  const [r] = sanitize([rec(1, { cookie: 'sessionKey=x', headers: { authorization: 'x' } })], { prompts: false, ext: '0.1.0' })
  assert.equal(r.ext, '0.1.0')
  assert.equal('text' in r, false)
  assert.equal('cookie' in r, false)
  assert.equal('headers' in r, false)
  assert.equal(r.est.ctx, 2500)
  assert.equal(sanitize([rec(1)], { prompts: true, ext: '0.1.0' })[0].text, 'hello')
  assert.equal('text' in sanitize([sweep(1)], { prompts: true })[0], false, 'live prompts do not cover history')
  assert.equal(sanitize([sweep(1)], { prompts: true, sweepPrompts: true })[0].text, 'hello')
  assert.equal(sanitize([rec(1, { org: 'x' }), rec(1, { kind: 'import' }), null, 'x', rec(1, { ts: 5 })], { prompts: true }).length, 0)
  assert.equal(sanitize([rec(1, { model: '<b>' })], {})[0].model, '')
  assert.equal('ctx' in sanitize([rec(1, { est: { in: 1, out: 1, ctx: -1 } })], {})[0].est, false)
})

test('mergeQueue skips swept turns already sent and duplicates in the queue', () => {
  let m = mergeQueue([], [rec(1), rec(1), sweep(3)], [])
  assert.equal(m.added, 2)
  // Queued but not yet delivered: the same swept turn is not queued twice.
  m = mergeQueue(m.queue, [sweep(3)], [])
  assert.equal(m.added, 0)
  // Delivered (sent): skipped; its live record is new.
  m = mergeQueue([], [sweep(3), sweep(5), rec(3)], [id(4)])
  assert.equal(m.added, 2)
  assert.deepEqual(m.queue.map((r) => r.kind + r.assistant.slice(-2)), ['sweep06', 'live04'])
})

test('mergeQueue never drops queued records: it refuses new ones when full', () => {
  const full = Array.from({ length: MAX_QUEUE }, (_, i) => ({ kind: 'live', assistant: 'q' + i }))
  const big = mergeQueue(full, [rec(1)], [])
  assert.equal(big.queue.length, MAX_QUEUE)
  assert.equal(big.queue[0].assistant, 'q0', 'the oldest records stay')
  assert.equal(big.refused, 1)
  // Sweeps stop at SWEEP_ROOM; live records still fit.
  const busy = Array.from({ length: SWEEP_ROOM }, (_, i) => ({ kind: 'live', assistant: 'q' + i }))
  const m = mergeQueue(busy, [sweep(1), rec(3)], [])
  assert.equal(m.added, 1)
  assert.equal(m.refused, 1)
  assert.equal(m.queue.at(-1).kind, 'live')
})

test('settleQueue removes delivered records by key and marks swept turns sent', () => {
  const batch = [rec(1), sweep(3)]
  // A record arrived at the front while the batch was out; another was queued after.
  const queue = [sweep(7), ...batch, rec(5)]
  const done = settleQueue(queue, batch, ['old'])
  assert.deepEqual(done.queue.map((r) => r.kind + r.assistant.slice(-2)), ['sweep08', 'live06'])
  assert.deepEqual(done.sent, ['old', id(4)])
  const many = settleQueue([], [sweep(1)], Array.from({ length: MAX_SENT }, (_, i) => 's' + i))
  assert.equal(many.sent.length, MAX_SENT)
  assert.equal(many.sent.at(-1), id(2))
})

test('decideSweep honours the sweeper switch, the lease, the cadence and the queue', () => {
  const cfg = { sweeper: true, settleMinutes: 360 }
  assert.equal(decideSweep({}, { sweeper: false }, 0, '').due, false)
  assert.equal(decideSweep({}, null, 0, '').due, false)
  const first = decideSweep({}, cfg, 1000, 'x')
  assert.equal(first.due, true)
  assert.deepEqual({ ...first.reply }, { due: true, cursor: '', settleMinutes: 360, budget: 300, only: 'x' })
  assert.equal(decideSweep(first.state, cfg, 2000, '').due, false, 'another tab holds the lease')
  const ran = { cursor: 'c', lastAt: 10_000 }
  assert.equal(decideSweep(ran, cfg, 10_000 + SWEEP_EVERY - 1, '').due, false)
  assert.equal(decideSweep(ran, cfg, 10_000 + SWEEP_EVERY, '').reply.cursor, 'c')
  assert.equal(decideSweep({ ...ran, more: true }, cfg, 10_000 + SWEEP_AGAIN, '').due, true)
  assert.equal(decideSweep({}, cfg, 1000, '', SWEEP_ROOM / 2).due, false, 'no sweep while the queue is busy')
})

test('finishSweep moves the cursor forward only when nothing was refused', () => {
  const [a, b, c] = ['2026-10-01T00:00:00Z', '2026-10-02T00:00:00Z', '2026-10-03T00:00:00.000000Z']
  const now = Date.parse('2026-10-25T00:00:00Z')
  const s = { cursor: b, lease: 9 }
  assert.equal(finishSweep(s, { ok: true, cursor: c, more: false }, now).cursor, c)
  assert.equal(finishSweep(s, { ok: true, cursor: a }, now).cursor, b, 'never backwards')
  assert.equal(finishSweep(s, { ok: false, cursor: c }, now).cursor, b)
  for (const forged of ['9999-12-31T00:00:00Z', '2026-10-26T00:00:00Z', 'zzz', 'x'.repeat(50)]) {
    assert.equal(finishSweep(s, { ok: true, cursor: forged }, now).cursor, b, 'a cursor past now is ignored: ' + forged)
  }
  const refused = finishSweep({ ...s, refused: true }, { ok: true, cursor: c, more: false }, now)
  assert.equal(refused.cursor, b, 'refused records are read again')
  assert.equal(refused.more, true, 'and soon')
  assert.equal(refused.refused, false)
  assert.equal(refused.lease, 0)
})

test('claims keep ids, the platform and a time only', () => {
  const [c] = sanitize([{ ...rec(1), kind: 'claim', human: '', text: 'x', est: { in: 9 } }], { prompts: true, ext: '0.1.0' })
  assert.deepEqual(c, {
    v: 1, kind: 'claim', platform: 'web_claude_ai', org: ORG, conv: CONV, human: '', assistant: id(2), ts: rec(1).ts, calls: 1,
    est: { in: 0, out: 0, algo: 'chars-v1' }, ext: '0.1.0',
  })
  assert.equal(sanitize([{ ...rec(1), kind: 'claim', assistant: 'x' }], {}).length, 0)
})

test('prompt text is cut to the collector limit in UTF-8 bytes', () => {
  assert.equal(cutText('abc'), 'abc')
  const long = 'é'.repeat(MAX_TEXT_BYTES)
  const cut = cutText(long)
  assert.ok(byteLength(cut) <= MAX_TEXT_BYTES && cut.length === MAX_TEXT_BYTES / 2)
  assert.ok(!cut.includes('\uFFFD'))
  assert.equal(sanitize([rec(1, { text: long })], { prompts: true })[0].text, cut)
})

test('queued prompt text expires; the records stay', () => {
  const t = 1_000_000
  const m = mergeQueue([], [rec(1), rec(3, { text: undefined })], [], t)
  assert.equal(m.queue[0].queuedAt, t)
  assert.equal('queuedAt' in m.queue[1], false, 'no text, no queue time')
  assert.equal(expireText(m.queue, t + TEXT_TTL - 1).changed, false)
  const x = expireText(m.queue, t + TEXT_TTL)
  assert.equal(x.changed, true)
  assert.equal(x.queue.length, 2)
  assert.equal('text' in x.queue[0] || 'queuedAt' in x.queue[0], false)
  assert.equal(x.queue[0].est.in, 10)
  // Older text goes first past the queue's text budget.
  const big = 'y'.repeat(MAX_QUEUE_TEXT / 2 + 1)
  const q = mergeQueue([], [rec(1, { text: big }), rec(3, { text: big }), rec(5, { text: 'new' })], [], t).queue
  const b = expireText(q, t + 1)
  assert.deepEqual(b.queue.map((r) => 'text' in r), [false, true, true])
  // Text without a queue time (an older queue) goes at once.
  assert.equal('text' in expireText([rec(1)], t).queue[0], false)
})

test('pickBatch caps a message by records and bytes and strips queue fields', () => {
  const q = mergeQueue([], [1, 3, 5, 7].map((n) => rec(n, { text: 'z'.repeat(200 * 1024) })), [], 5).queue
  assert.equal(pickBatch(q, 2).length, 2)
  const small = pickBatch(q, 200, 450 * 1024)
  assert.equal(small.length, 2)
  assert.ok(small.every((r) => !('queuedAt' in r)))
  assert.equal(pickBatch(q, 200, 10).length, 1, 'always one record')
  const full = Array.from({ length: 200 }, (_, i) => rec(2 * i + 1, { text: 'w'.repeat(MAX_TEXT_BYTES) }))
  const batch = pickBatch(full, 200)
  assert.ok(batch.length < 200 && byteLength(JSON.stringify(batch)) <= MAX_BATCH_BYTES)
})

test('shrinkRecord drops the text of a record too large to send, then the record', () => {
  const q = [rec(1), rec(3, { text: undefined })]
  const once = shrinkRecord(q, q[0])
  assert.equal(once.length, 2)
  assert.equal('text' in once[0], false)
  assert.equal(shrinkRecord(once, once[0]).length, 1)
})
