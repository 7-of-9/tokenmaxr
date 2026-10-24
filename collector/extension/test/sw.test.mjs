import test from 'node:test'
import assert from 'node:assert/strict'
import { ORG, CONV, id } from './fixtures.mjs'

let n = 0

// worker loads a fresh sw.js with a fake chrome.*: host answers each native
// message, and failSet (optional) makes a storage write throw.
async function worker(host, { failSet } = {}) {
  const store = {}
  const sent = []
  let listener = null
  globalThis.fetch = async () => { throw new Error('no config.json') }
  globalThis.chrome = {
    runtime: {
      id: 'ext',
      lastError: undefined,
      getManifest: () => ({ version: '0.1.0' }),
      getURL: (p) => 'chrome-extension://ext/' + p,
      sendNativeMessage(name, msg, cb) {
        sent.push(msg)
        setTimeout(() => cb(host(msg)), 1)
      },
      onMessage: { addListener: (fn) => { listener = fn } },
      onInstalled: { addListener() {} },
      onStartup: { addListener() {} },
    },
    storage: {
      local: {
        async get(k) { return { [k]: structuredClone(store[k]) } },
        async set(o) {
          if (failSet && failSet(o)) throw new Error('QUOTA_BYTES quota exceeded')
          Object.assign(store, structuredClone(o))
        },
      },
    },
    alarms: { create() {}, onAlarm: { addListener() {} } },
  }
  await import('../sw.js?instance=' + n++)
  const send = (msg) => new Promise((resolve) => {
    listener(msg, { id: 'ext', url: 'https://claude.ai/chat/x' }, resolve)
  })
  return { store, sent, send }
}

const settings = { ok: true, version: 'dev', prompts: true, sweepPrompts: false, sweeper: false, inferApps: true, settleMinutes: 0, t0: '' }

const live = (k, text) => ({
  v: 1, kind: 'live', platform: 'web_claude_ai', org: ORG, conv: CONV, human: id(2 * k), assistant: id(2 * k + 1),
  ts: '2026-10-23T07:39:09Z', calls: 1, est: { in: 10, out: 2, algo: 'chars-v1' }, ...(text ? { text } : {}),
})

async function until(fn, ms = 3000) {
  const end = Date.now() + ms
  while (!fn()) {
    if (Date.now() > end) throw new Error('timed out')
    await new Promise((r) => setTimeout(r, 5))
  }
}

test('a batch the host finds too large is split, and every record arrives', async () => {
  const got = []
  const w = await worker((msg) => {
    if (msg.type === 'hello') return settings
    if (msg.records.length > 3) return { ok: false, tooLarge: true }
    got.push(...msg.records)
    return { ...settings, accepted: msg.records.length }
  })
  await w.send({ type: 'records', records: Array.from({ length: 10 }, (_, k) => live(k)) })
  await until(() => got.length === 10 && w.store.queue && w.store.queue.length === 0)
  assert.equal(new Set(got.map((r) => r.assistant)).size, 10)
  assert.ok(got.every((r) => !('queuedAt' in r)), 'queue fields never reach the host')
})

test('one record still too large loses its text, then is sent', async () => {
  const got = []
  const w = await worker((msg) => {
    if (msg.type === 'hello') return settings
    if (msg.records.some((r) => 'text' in r)) return { ok: false, tooLarge: true }
    got.push(...msg.records)
    return settings
  })
  await w.send({ type: 'records', records: [live(1, 'hello')] })
  await until(() => got.length === 1)
  assert.equal('text' in got[0], false)
  assert.equal(got[0].est.in, 10)
})

test('a full storage keeps the records without their text', async () => {
  const w = await worker((msg) => (msg.type === 'hello' ? settings : { ok: false, transport: true, error: 'no host' }), {
    failSet: (o) => Array.isArray(o.queue) && o.queue.some((r) => 'text' in r),
  })
  const reply = await w.send({ type: 'records', records: [live(1, 'hello'), live(2)] })
  assert.equal(reply.ok, true)
  assert.equal(w.store.queue.length, 2)
  assert.ok(w.store.queue.every((r) => !('text' in r)))
})

test('claims are queued and sent like records', async () => {
  const got = []
  const w = await worker((msg) => {
    if (msg.type === 'hello') return settings
    got.push(...msg.records)
    return settings
  })
  await w.send({ type: 'records', records: [{ v: 1, kind: 'claim', platform: 'desktop_app', org: ORG, conv: CONV, human: '', assistant: id(9), ts: '2026-10-23T07:39:09Z' }] })
  await until(() => got.length === 1)
  assert.equal(got[0].kind, 'claim')
  assert.equal(got[0].platform, 'desktop_app')
})
