// The Functions v4 adapter maps HttpRequest to the pure handler contract.
import test from 'node:test'
import assert from 'node:assert/strict'
import functions from '@azure/functions'
import { adapt } from '../../src/lib/adapter.js'
import { MAX_BODY_BYTES } from '../../src/lib/http.js'

const { HttpRequest } = functions

const echo = async (req) => ({ status: 200, headers: { 'X-Test': '1' }, jsonBody: req })

test('adapter passes method, lowercased headers, query, params and parsed body', async () => {
  const res = await adapt(echo)(new HttpRequest({
    method: 'POST',
    url: 'http://localhost/api/prompts/abc?month=2026-09',
    headers: { 'X-D0M1-Token': 't0k' },
    params: { id: 'abc' },
    body: { string: '{"v":1}' },
  }))
  assert.equal(res.status, 200)
  assert.equal(res.headers['X-Test'], '1')
  const req = res.jsonBody
  assert.equal(req.method, 'POST')
  assert.equal(req.headers['x-d0m1-token'], 't0k')
  assert.deepEqual(req.query, { month: '2026-09' })
  assert.deepEqual(req.params, { id: 'abc' })
  assert.deepEqual(req.body, { v: 1 })
  assert.ok(req.now instanceof Date)
})

test('adapter rejects bad JSON and oversized bodies before the handler runs', async () => {
  let called = false
  const h = adapt(async () => {
    called = true
    return { status: 200, headers: {}, jsonBody: {} }
  })
  const bad = await h(new HttpRequest({ method: 'POST', url: 'http://localhost/api/ingest', body: { string: '{' } }))
  assert.equal(bad.status, 400)
  const big = await h(new HttpRequest({ method: 'POST', url: 'http://localhost/api/ingest', body: { string: 'x'.repeat(MAX_BODY_BYTES + 1) } }))
  assert.equal(big.status, 413)
  assert.equal(called, false)
})

test('handler exceptions become a 503 without leaking details', async () => {
  const res = await adapt(async () => {
    throw new Error('boom secret')
  })(new HttpRequest({ method: 'GET', url: 'http://localhost/api/usage' }))
  assert.equal(res.status, 503)
  assert.ok(!JSON.stringify(res.jsonBody).includes('secret'))
})
