import test from 'node:test'
import assert from 'node:assert/strict'
import { tree, id } from './fixtures.mjs'

// The repository is an ES module package, so estimate.js runs as a module
// here and hands itself over through the same global the page uses.
await import('../estimate.js')
const est = globalThis.__tokenmaxrEstimate

test('tokens: ASCII by the calibrated ratio, CJK one each, other non-ASCII half', () => {
  assert.equal(est.ALGO, 'chars-v1')
  assert.ok(est.RHO > 2 && est.RHO < 5)
  assert.equal(est.tokens(''), 0)
  assert.equal(est.tokens(undefined), 0)
  assert.ok(Math.abs(est.tokens('a'.repeat(297)) - 297 / est.RHO) < 1e-9)
  assert.equal(est.tokens('漢字かな한국'), 6)
  assert.equal(est.tokens('éàü'), 1.5)
  assert.equal(est.tokens('😀'), 0.5) // one astral code point, not two halves
})

test('estimateTurn walks only the branch above the prompt', () => {
  const t = tree()
  const O = est.MESSAGE_OVERHEAD
  const r = est.RHO
  // Turn 2 (reply 4): context = turn 1 (prompt 1 + reply 2), fresh = prompt 3.
  const t2 = est.estimateTurn(t, id(4))
  const ctx = (O + 19 / r) + (O + 2 / r)
  assert.equal(t2.calls, 1)
  assert.equal(t2.in, Math.round(ctx + O + 297 / r + O))
  assert.equal(t2.out, Math.round(594 / r))
  assert.equal(t2.human.uuid, id(3))
  // The branch (prompt 5 edits prompt 3) never sees turn 2.
  const t3 = est.estimateTurn(t, id(6))
  const fresh = O + 13 / r + 297 / r + 1000 // text + pasted text + a 1000x750 image
  const base = ctx + fresh + O
  const toolUse = 10 / r + JSON.stringify({ query: 'q' }).length / r
  // title, url, text and the site name; not display_content or any "type"
  const toolResult = (1 + 'https://e.x'.length + 297 + 'e.x'.length) / r
  assert.equal(t3.calls, 2, 'one tool loop is a second model call')
  assert.equal(t3.in, Math.round(base + base + toolUse + toolResult))
  assert.equal(t3.out, Math.round(toolUse + 6 / r))
})

test('estimateTurn needs the reply and its prompt', () => {
  const t = tree()
  assert.equal(est.estimateTurn(t, id(99)), null)
  assert.equal(est.estimateTurn(t, id(3)), null, 'a human message is not a turn')
  const orphan = { chat_messages: [{ uuid: id(8), sender: 'assistant', parent_message_uuid: id(7), content: [] }] }
  assert.equal(est.estimateTurn(orphan, id(8)), null)
})

test('images without a size count a full image; text fallbacks work', () => {
  const m = { sender: 'human', content: [], text: 'hi', files: [{ file_kind: 'image', file_uuid: 'a' }, { file_kind: 'image', file_uuid: 'a' }] }
  assert.equal(est.messageTokens(m), est.MESSAGE_OVERHEAD + 2 / est.RHO + est.IMAGE_TOKENS)
  assert.equal(est.humanText({ content: [{ type: 'text', text: 'a' }, { type: 'tool_result' }, { type: 'text', text: 'b' }] }), 'a\nb')
})

test('a cyclic or deep tree terminates', () => {
  const t = { chat_messages: [
    { uuid: id(1), sender: 'human', parent_message_uuid: id(2), content: [{ type: 'text', text: 'x' }] },
    { uuid: id(2), sender: 'assistant', parent_message_uuid: id(1), content: [{ type: 'text', text: 'y' }] },
    { uuid: id(3), sender: 'human', parent_message_uuid: id(2), content: [{ type: 'text', text: 'z' }] },
    { uuid: id(4), sender: 'assistant', parent_message_uuid: id(3), content: [{ type: 'text', text: 'w' }] },
  ] }
  assert.ok(est.estimateTurn(t, id(4)).in > 0)
})

test('toolTokens and contextTokens: the fixed context of one model call', () => {
  const tools = [{ name: 'a', description: 'x'.repeat(297), input_schema: { type: 'object' } }, null, 'junk']
  const n = est.toolTokens(tools)
  assert.ok(n >= 100 && n < 120, String(n))
  assert.equal(est.toolTokens(undefined), 0)
  assert.equal(est.contextTokens(n), est.SYSTEM_FLOOR + n)
  assert.equal(est.contextTokens(undefined), est.SYSTEM_FLOOR)
  assert.equal(est.contextTokens(-5), est.SYSTEM_FLOOR)
})
