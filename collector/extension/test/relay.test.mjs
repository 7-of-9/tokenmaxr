import test from 'node:test'
import assert from 'node:assert/strict'
import vm from 'node:vm'
import { readFileSync } from 'node:fs'
import { ORG, CONV, id } from './fixtures.mjs'

const RELAY = readFileSync(new URL('../relay.js', import.meta.url), 'utf8')

// relay runs relay.js in a fake isolated world. sweepDue is the service
// worker's answer to sweep-due.
function relay({ sweepDue = false } = {}) {
  const et = new EventTarget()
  const msgs = []
  const timers = []
  const g = {
    CustomEvent, JSON, Date, Promise, Array, Number,
    addEventListener: et.addEventListener.bind(et),
    dispatchEvent: et.dispatchEvent.bind(et),
    setTimeout: (fn) => timers.push(fn),
    setInterval: () => {},
    chrome: {
      runtime: {
        lastError: undefined,
        sendMessage(msg, cb) {
          msgs.push(msg)
          if (msg.type === 'sweep-due') cb(sweepDue ? { due: true, cursor: '' } : { due: false })
          else cb({ ok: true })
        },
      },
    },
  }
  g.window = g
  vm.createContext(g)
  vm.runInContext(RELAY, g)
  const post = (type, v) => et.dispatchEvent(new CustomEvent(type, { detail: JSON.stringify(v) }))
  const records = () => msgs.filter((m) => m.type === 'records').flatMap((m) => m.records)
  return { msgs, post, records, startSweep: async () => { timers.forEach((f) => f()); await new Promise((r) => setTimeout(r, 5)) } }
}

const rec = (kind, n) => ({ v: 1, kind, org: ORG, conv: CONV, human: id(n), assistant: id(n + 1), ts: '2026-10-23T07:39:09Z' })

test('live records and claims pass at a human rate; malformed ones never', () => {
  const r = relay()
  r.post('__tokenmaxr:records', [rec('live', 1), rec('claim', 1), null, 'x', [], { kind: 'live' }, rec('import', 3)])
  assert.deepEqual(r.records().map((x) => x.kind), ['live', 'claim'])
  r.post('__tokenmaxr:records', Array.from({ length: 100 }, (_, i) => rec('live', 10 + 2 * i)))
  assert.equal(r.records().length, 60, 'at most 60 a minute')
  r.post('__tokenmaxr:records', { not: 'an array' })
  assert.equal(r.records().length, 60)
})

test('swept records and a sweep result pass only while our sweep runs', async () => {
  const idle = relay()
  idle.post('__tokenmaxr:records', [rec('sweep', 1)])
  idle.post('__tokenmaxr:sweep-done', { ok: true, cursor: '9999-12-31T00:00:00Z' })
  assert.equal(idle.msgs.filter((m) => m.type !== 'config').length, 0, 'a page cannot inject history or end a sweep')

  const r = relay({ sweepDue: true })
  await r.startSweep()
  r.post('__tokenmaxr:records', [rec('sweep', 1)])
  assert.equal(r.records().length, 1)
  r.post('__tokenmaxr:sweep-done', { ok: true, cursor: '2026-10-23T07:40:00Z', more: false, conversations: 1, records: 1, extra: 'x' })
  const done = r.msgs.filter((m) => m.type === 'sweep-done')
  assert.equal(done.length, 1)
  assert.deepEqual(Object.keys(done[0].result).sort(), ['conversations', 'cursor', 'more', 'ok', 'records'])
  // The run is over: a second result or more swept records are ignored.
  r.post('__tokenmaxr:sweep-done', { ok: true, cursor: '2026-10-24T00:00:00Z' })
  r.post('__tokenmaxr:records', [rec('sweep', 3)])
  assert.equal(r.msgs.filter((m) => m.type === 'sweep-done').length, 1)
  assert.equal(r.records().length, 1)
})
