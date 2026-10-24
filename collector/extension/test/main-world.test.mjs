import test from 'node:test'
import assert from 'node:assert/strict'
import vm from 'node:vm'
import { readFileSync } from 'node:fs'
import { ORG, CONV, id, sse, tree } from './fixtures.mjs'

const src = (f) => readFileSync(new URL('../' + f, import.meta.url), 'utf8')
const ESTIMATE = src('estimate.js')
const SHIPPED = src('main-world.js')
// The tests add the pure pieces as hooks; the shipped file exposes nothing.
const MAIN = SHIPPED.replace('/* test-hooks */',
  'W.__tokenmaxrTest = { sseParser: sseParser, onEvent: onEvent, newStreamState: newStreamState, requestTools: requestTools }')
const COMPLETION = `/api/organizations/${ORG}/chat_conversations/${CONV}/completion`

// page builds a fake claude.ai window: the two content scripts run in it
// exactly as Chrome runs them in the page's world.
function page({ stream, treeBody = tree(), failTree = 0, list = null, org = { raven_type: null, capabilities: ['chat', 'claude_max'] }, main = MAIN } = {}) {
  const et = new EventTarget()
  const calls = []
  const records = []
  const claims = []
  const events = []
  const done = []
  let treeFails = failTree
  const g = {
    CustomEvent, TextDecoder, TextEncoder, Response, Headers, ReadableStream, Request, Blob, DecompressionStream, ArrayBuffer, Uint8Array,
    setTimeout: (fn) => setTimeout(fn, 0), // the capture's waits, without the wait
    addEventListener: et.addEventListener.bind(et),
    dispatchEvent: et.dispatchEvent.bind(et),
    async fetch(input, init) {
      const url = typeof input === 'string' ? input : input.url
      calls.push({ url, init })
      if (url.includes('/completion')) {
        if (stream === 'reject') throw new TypeError('network down')
        const chunks = typeof stream === 'object' ? stream.chunks : chunk(stream)
        const fail = typeof stream === 'object' && stream.fail
        let i = 0
        return new Response(new ReadableStream({
          pull(c) {
            if (i < chunks.length) c.enqueue(new TextEncoder().encode(chunks[i++]))
            else if (fail) c.error(new DOMException('aborted', 'AbortError'))
            else c.close()
          },
        }), { status: 200, headers: { 'content-type': 'text/event-stream' } })
      }
      if (url.includes('chat_conversations_v2')) return Response.json(list || { data: [], has_more: false })
      if (url === '/api/organizations/' + ORG) return Response.json(org)
      if (url.includes(`/chat_conversations/${CONV}?`)) {
        if (treeFails-- > 0) return Response.json(tree().chat_messages.slice(0, 2) && { chat_messages: [] })
        return Response.json(treeBody)
      }
      return Response.json({})
    },
  }
  g.window = g
  vm.createContext(g)
  vm.runInContext(ESTIMATE, g)
  vm.runInContext(main, g)
  et.addEventListener('__tokenmaxr:records', (e) => {
    events.push(e.detail.length)
    for (const r of JSON.parse(e.detail)) (r.kind === 'claim' ? claims : records).push(r)
  })
  et.addEventListener('__tokenmaxr:sweep-done', (e) => done.push(JSON.parse(e.detail)))
  const config = (c) => et.dispatchEvent(new CustomEvent('__tokenmaxr:config', { detail: JSON.stringify(c) }))
  return { g, calls, records, claims, events, done, config, details: () => JSON.stringify(records) + JSON.stringify(claims) }
}

// chunk splits a stream at awkward places (mid-line, mid-event).
function chunk(s) {
  const out = []
  for (let i = 0; i < s.length; i += 37) out.push(s.slice(i, i + 37))
  return out
}

async function until(fn, ms = 3000) {
  const end = Date.now() + ms
  while (!fn()) {
    if (Date.now() > end) throw new Error('timed out')
    await new Promise((r) => setTimeout(r, 5))
  }
}

async function readAll(res) {
  return await res.text()
}

test('a web turn becomes one record of ids and numbers, never text', async () => {
  const body = sse({ assistant: id(6).toUpperCase(), human: id(5), tool: true, first: true })
  const p = page({ stream: body })
  p.config({ enabled: true, prompts: false })
  const res = await p.g.fetch(COMPLETION, { method: 'POST', headers: { 'anthropic-client-platform': 'web_claude_ai' }, body: new Uint8Array([0x1f, 0x8b]) })
  assert.equal(await readAll(res), body, 'the app reads the stream unchanged')
  await until(() => p.records.length === 1)
  const r = p.records[0]
  assert.equal(r.kind, 'live')
  assert.equal(r.platform, 'web_claude_ai')
  assert.equal(r.org, ORG)
  assert.equal(r.conv, CONV)
  assert.equal(r.assistant, id(6), 'uuids are lower-cased')
  assert.equal(r.human, id(5))
  assert.equal(r.model, 'claude-fable-5-1')
  assert.equal(r.chatPlatform, 'CLAUDE_AI')
  assert.equal(r.stop, 'end_turn')
  assert.equal(r.calls, 2)
  assert.equal(r.thinkingMs, 256)
  assert.equal(r.est.algo, 'chars-v1')
  assert.ok(r.est.in > r.est.out && r.est.out > 0)
  assert.equal(r.ts, '2026-10-01T07:36:00.000000Z')
  assert.equal(r.promptTs, '2026-10-01T07:35:00.000000Z')
  assert.equal('text' in r, false)
  assert.doesNotMatch(p.details(), /SECRET|edited prompt|https:\/\/e\.x/)
  // One read of the conversation, made like the app's own (same origin, GET).
  const reads = p.calls.filter((c) => c.url.includes('tree=True'))
  assert.equal(reads.length, 1)
  assert.equal(reads[0].init.method, 'GET')
  assert.equal(reads[0].init.credentials, 'same-origin')
})

test('the prompt text is added only while prompt capture is on', async () => {
  const p = page({ stream: sse({ assistant: id(4), human: id(3) }) })
  p.config({ enabled: true, prompts: true })
  await readAll(await p.g.fetch(COMPLETION, { headers: new Headers({ 'Anthropic-Client-Platform': 'desktop_app' }) }))
  await until(() => p.records.length === 1)
  assert.equal(p.records[0].text, 'a'.repeat(297))
  assert.equal(p.records[0].platform, 'desktop_app')
})

test('records wait for the settings and are dropped when capture is off', async () => {
  const p = page({ stream: sse({ assistant: id(4), human: id(3) }) })
  await readAll(await p.g.fetch(COMPLETION, {}))
  await new Promise((r) => setTimeout(r, 100))
  assert.equal(p.records.length, 0)
  p.config({ enabled: true, prompts: false })
  await until(() => p.records.length === 1)
  assert.equal(p.records[0].platform, '', 'no header: client unknown')

  const off = page({ stream: sse({ assistant: id(4), human: id(3) }) })
  off.config({ enabled: false, prompts: true })
  await readAll(await off.g.fetch(COMPLETION, {}))
  await new Promise((r) => setTimeout(r, 100))
  assert.equal(off.records.length, 0)
})

test('a stopped reply is still recorded from the saved conversation', async () => {
  const p = page({ stream: { chunks: chunk(sse({ assistant: id(4), human: id(3), cut: true })), fail: true } })
  p.config({ enabled: true, prompts: false })
  const res = await p.g.fetch(COMPLETION, {})
  await assert.rejects(readAll(res))
  await until(() => p.records.length === 1)
  assert.equal(p.records[0].assistant, id(4))
  assert.equal(p.records[0].stop, 'end_turn', 'the saved stop reason when the stream had none')
})

test('the reply is looked for again when the conversation lags', async () => {
  const p = page({ stream: sse({ assistant: id(4), human: id(3) }), failTree: 2 })
  p.config({ enabled: true, prompts: false })
  await readAll(await p.g.fetch(COMPLETION, {}))
  await until(() => p.records.length === 1)
  assert.equal(p.calls.filter((c) => c.url.includes('tree=True')).length, 3)
})

test('other requests and failures pass through untouched', async () => {
  const p = page({ stream: 'reject' })
  p.config({ enabled: true, prompts: false })
  const other = await p.g.fetch('/api/organizations/' + ORG + '/chat_conversations/' + CONV + '/title', { method: 'POST' })
  assert.deepEqual(await other.json(), {})
  await assert.rejects(p.g.fetch(COMPLETION, {}), /network down/)
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(p.records.length, 0)
  assert.equal(p.g.__tokenmaxrEstimate, undefined, 'the estimator handle is removed from the page')
})

test('the SSE parser keeps no text and survives padding and split chunks', () => {
  const p = page({ stream: '' })
  const t = p.g.__tokenmaxrTest
  const st = t.newStreamState()
  const parser = t.sseParser((d) => t.onEvent(st, d))
  for (const c of chunk(sse({ assistant: id(2), human: id(1), tool: true }))) parser.push(c)
  parser.end()
  assert.equal(st.assistant, id(2))
  assert.equal(st.done, true)
  assert.equal(JSON.stringify(st.blocks), JSON.stringify(['thinking', 'tool_use', 'tool_result', 'text']))
  assert.doesNotMatch(JSON.stringify(st), /SECRET/)
})

test('the sweep reads settled history and reports a cursor', async () => {
  const list = { data: [{ uuid: CONV, updated_at: '2026-10-23T07:40:00.000000Z', model: 'claude-fable-5-1' }], has_more: false }
  const p = page({ stream: '', list })
  p.config({ enabled: true, prompts: false })
  await p.g.fetch('/api/organizations/' + ORG, {}) // the app's own call names the organisation
  p.g.dispatchEvent(new CustomEvent('__tokenmaxr:sweep', { detail: JSON.stringify({ cursor: '', settleMinutes: 0, budget: 5 }) }))
  await until(() => p.done.length === 1)
  const d = p.done[0]
  assert.equal(d.ok, true)
  assert.equal(d.cursor, '2026-10-23T07:40:00.000000Z')
  assert.equal(d.records, 3)
  assert.deepEqual(p.records.map((r) => r.assistant).sort(), [id(2), id(4), id(6)])
  for (const r of p.records) {
    assert.equal(r.kind, 'sweep')
    assert.equal(r.platform, '')
  }
  // A second sweep from that cursor finds nothing new.
  p.g.dispatchEvent(new CustomEvent('__tokenmaxr:sweep', { detail: JSON.stringify({ cursor: d.cursor, settleMinutes: 0, budget: 5 }) }))
  await until(() => p.done.length === 2)
  assert.equal(p.done[1].records, 0)
})

test('the sweep waits until turns have settled', async () => {
  const list = { data: [{ uuid: CONV, updated_at: new Date().toISOString() }], has_more: false }
  const fresh = tree()
  fresh.chat_messages[5].created_at = new Date().toISOString() // the branch reply is minutes old
  const p = page({ stream: '', list, treeBody: fresh })
  p.config({ enabled: true, prompts: false })
  await p.g.fetch('/api/organizations/' + ORG + '/usage', {})
  p.g.dispatchEvent(new CustomEvent('__tokenmaxr:sweep', { detail: JSON.stringify({ cursor: '', settleMinutes: 360, budget: 5 }) }))
  await until(() => p.done.length === 1)
  assert.equal(p.done[0].records, 2, 'the unsettled reply waits')
  assert.ok(!p.records.some((r) => r.assistant === id(6)))
  assert.ok(p.done[0].cursor < new Date(Date.now() - 359 * 60000).toISOString(), 'the cursor stops at the settle line')
})

// tools is a completion request's tool definitions, as the app sends them.
const TOOLS = [
  { name: 'search_mail', description: 'Search the mailbox. '.repeat(20), input_schema: { type: 'object', properties: { q: { type: 'string' } } } },
  { name: 'calendar', description: 'Read the calendar.', input_schema: { type: 'object', properties: {} }, integration_name: 'x' },
]

async function gzip(text) {
  const s = new Blob([text]).stream().pipeThrough(new CompressionStream('gzip'))
  return new Uint8Array(await new Response(s).arrayBuffer())
}

test('the fixed context per call: the system floor plus the request\'s tool definitions', async () => {
  await import('../estimate.js')
  const E = globalThis.__tokenmaxrEstimate
  const tools = E.toolTokens(TOOLS)
  assert.ok(tools > 100, 'tool descriptions and schemas count')
  for (const [name, send] of [
    ['gzip bytes', async (p) => p.g.fetch(COMPLETION, { method: 'POST', body: await gzip(JSON.stringify({ prompt: 'SECRET', tools: TOOLS })) })],
    ['a string', async (p) => p.g.fetch(COMPLETION, { method: 'POST', body: JSON.stringify({ prompt: 'SECRET', tools: TOOLS }) })],
    ['a Request', async (p) => p.g.fetch(new Request('https://claude.ai' + COMPLETION, { method: 'POST', body: JSON.stringify({ prompt: 'SECRET', tools: TOOLS }) }))],
  ]) {
    const p = page({ stream: sse({ assistant: id(4), human: id(3) }) })
    p.config({ enabled: true, prompts: false })
    await readAll(await send(p))
    await until(() => p.records.length === 1)
    assert.equal(p.records[0].est.ctx, E.SYSTEM_FLOOR + tools, name)
    assert.doesNotMatch(p.details(), /SECRET|search_mail|mailbox/, name + ': only the count leaves the page')
  }
  // An unreadable body counts the floor only, and the request is untouched.
  const p = page({ stream: sse({ assistant: id(4), human: id(3) }) })
  p.config({ enabled: true, prompts: false })
  await readAll(await p.g.fetch(COMPLETION, { method: 'POST', body: new Uint8Array([0x1f, 0x8b, 1, 2]) }))
  await until(() => p.records.length === 1)
  assert.equal(p.records[0].est.ctx, E.SYSTEM_FLOOR)
})

test('swept turns carry text only when the sweep prompt setting is on', async () => {
  const list = { data: [{ uuid: CONV, updated_at: '2026-10-23T07:40:00.000000Z' }], has_more: false }
  for (const [cfg, want] of [[{ prompts: true }, false], [{ prompts: true, sweepPrompts: true }, true]]) {
    const p = page({ stream: '', list })
    p.config({ enabled: true, ...cfg })
    await p.g.fetch('/api/organizations/' + ORG, {})
    p.g.dispatchEvent(new CustomEvent('__tokenmaxr:sweep', { detail: JSON.stringify({ cursor: '', settleMinutes: 0, budget: 5 }) }))
    await until(() => p.done.length === 1)
    assert.equal(p.records.length, 3)
    for (const r of p.records) assert.equal('text' in r, want, JSON.stringify(cfg))
    for (const r of p.records) assert.equal(r.est.ctx, 2000, 'the sweep cannot see tool definitions')
  }
})

test('the sweep reads personal organisations only', async () => {
  const list = { data: [{ uuid: CONV, updated_at: '2026-10-23T07:40:00.000000Z' }], has_more: false }
  for (const org of [{ raven_type: 'team', capabilities: ['chat'] }, { capabilities: ['chat'] }, { raven_type: null, capabilities: ['chat', 'raven'] }, null]) {
    const p = page({ stream: '', list, org })
    p.config({ enabled: true, prompts: false })
    await p.g.fetch('/api/organizations/' + ORG + '/usage', {})
    p.g.dispatchEvent(new CustomEvent('__tokenmaxr:sweep', { detail: JSON.stringify({ cursor: 'c', settleMinutes: 0, budget: 5 }) }))
    await until(() => p.done.length === 1)
    assert.equal(p.done[0].ok, false, JSON.stringify(org))
    assert.equal(p.done[0].cursor, 'c')
    assert.equal(p.records.length, 0)
    assert.equal(p.calls.filter((c) => c.url.includes('chat_conversations')).length, 0, 'no history read')
  }
})

test('the shipped script leaves nothing on the page', async () => {
  const p = page({ stream: sse({ assistant: id(4), human: id(3) }), main: SHIPPED })
  p.config({ enabled: true, prompts: false })
  await readAll(await p.g.fetch(COMPLETION, {}))
  await until(() => p.records.length === 1)
  assert.deepEqual(Object.keys(p.g).filter((k) => k.startsWith('__tokenmaxr')), [])
  // A page that defines the old global first does not switch capture off.
  const q = page({ stream: sse({ assistant: id(4), human: id(3) }), main: 'window.__tokenmaxrCapture = {};' + SHIPPED })
  q.config({ enabled: true, prompts: false })
  await readAll(await q.g.fetch(COMPLETION, {}))
  await until(() => q.records.length === 1)
})

test('a claim leaves the page as soon as the stream ends, before the read-back', async () => {
  const p = page({ stream: sse({ assistant: id(6).toUpperCase(), human: id(5) }), failTree: 99 })
  p.config({ enabled: true, prompts: true })
  await readAll(await p.g.fetch(COMPLETION, { headers: { 'anthropic-client-platform': 'desktop_app' } }))
  await until(() => p.claims.length === 1)
  const c = p.claims[0]
  assert.deepEqual(Object.keys(c).sort(), ['assistant', 'conv', 'human', 'kind', 'org', 'platform', 'ts', 'v'])
  assert.equal(c.assistant, id(6))
  assert.equal(c.human, id(5))
  assert.equal(c.platform, 'desktop_app')
  // The conversation never shows the reply (the tab closed): no live record,
  // but the claim already left.
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(p.records.length, 0)
})

test('large prompts are cut and events split below the relay limit', async () => {
  const big = tree()
  const many = []
  for (let i = 0; i < 40; i++) {
    const h = { ...big.chat_messages[2], uuid: id(100 + 2 * i), parent_message_uuid: i ? id(99 + 2 * i) : id(2), content: [{ type: 'text', text: 'x'.repeat(300 * 1024) }] }
    const a = { ...big.chat_messages[3], uuid: id(101 + 2 * i), parent_message_uuid: id(100 + 2 * i) }
    many.push(h, a)
  }
  big.chat_messages = big.chat_messages.slice(0, 2).concat(many)
  const list = { data: [{ uuid: CONV, updated_at: '2026-10-23T07:40:00.000000Z' }], has_more: false }
  const p = page({ stream: '', list, treeBody: big })
  p.config({ enabled: true, prompts: true, sweepPrompts: true })
  await p.g.fetch('/api/organizations/' + ORG, {})
  p.g.dispatchEvent(new CustomEvent('__tokenmaxr:sweep', { detail: JSON.stringify({ cursor: '', settleMinutes: 0, budget: 5 }) }))
  await until(() => p.done.length === 1)
  assert.equal(p.records.length, 41)
  assert.ok(p.records.every((r) => !r.text || r.text.length <= 256 * 1024))
  assert.ok(p.events.length > 1 && p.events.every((n) => n <= 4 << 20), 'every event is under 4 MB')
})
