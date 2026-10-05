// GET / PUT / DELETE /api/fleet/github: an opaque, fleet-key-encrypted share
// of one machine's GitHub sign-in, readable by enrolled machines only.
import test from 'node:test'
import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'
import { createHarness } from '../harness.js'
import { client } from '../scenario.js'
import { CREATED_ON_USE, TABLES, memoryStore } from '../../src/lib/tables.js'
import { createInvite, handleEnroll } from '../../src/lib/enroll.js'
import { runHandler } from '../../src/lib/http.js'
import { FLEET_SECRETS, MAX_BLOB_BYTES, blobProblem, handleFleetGitHub } from '../../src/lib/fleet-github.js'
import { KFP } from '../fixtures.js'

const NOW = new Date('2026-10-05T09:00:00Z')

// What a collector sends: "tmx1" || nonce || ciphertext, base64. The server
// sees only these bytes, so random ones stand in for the AES-GCM output.
const share = (n = 120) => Buffer.concat([Buffer.from('tmx1'), randomBytes(n)]).toString('base64')

async function enroll(store, machineLabel) {
  const { invite } = await createInvite(store, new Date(), 'test')
  const r = await handleEnroll({ method: 'POST', headers: {}, query: {}, body: { invite, machineLabel, kFingerprint: KFP }, now: new Date() }, { store })
  assert.equal(r.status, 200)
  return r.jsonBody
}

const call = (store, method, token, body, now = NOW) =>
  handleFleetGitHub({ method, headers: token ? { 'x-d0m1-token': token } : {}, query: {}, params: {}, body, now }, { store, env: {} })

const isPrivate = (r) => assert.equal(r.headers['Cache-Control'], 'private, no-store')

test('blobProblem accepts a tmx1 share up to 4096 bytes and nothing else', () => {
  assert.equal(blobProblem(share()), null)
  assert.equal(blobProblem(Buffer.from('tmx1').toString('base64')), null)
  const max = Buffer.concat([Buffer.from('tmx1'), randomBytes(MAX_BLOB_BYTES - 4)])
  assert.equal(blobProblem(max.toString('base64')), null)
  const over = Buffer.concat([max, Buffer.alloc(1)])
  assert.match(blobProblem(over.toString('base64')), /over 4096 bytes/)
  assert.match(blobProblem('A'.repeat(100000)), /over 4096 bytes/)
  assert.match(blobProblem(Buffer.from('tmx2' + 'x'.repeat(40)).toString('base64')), /start with "tmx1"/)
  assert.match(blobProblem(Buffer.from('tmx').toString('base64')), /start with "tmx1"/)
  assert.match(blobProblem(Buffer.from('{"token":"ghu_x"}').toString('base64')), /start with "tmx1"/)
  for (const bad of [undefined, null, 42, '', {}, ['dG14MQ=='], 'not base64!', 'dG14MQ', 'dG14MQ==\n', 'dG14MX-_']) {
    assert.match(blobProblem(bad), /base64/, String(bad))
  }
})

test('only enrolled machines reach the share, and every answer is private', async () => {
  const store = memoryStore()
  const { token, machineId } = await enroll(store, 'rabbit')
  for (const method of ['GET', 'PUT', 'DELETE']) {
    const none = await call(store, method, null, { blob: share() })
    assert.equal(none.status, 401, method)
    isPrivate(none)
    assert.equal((await call(store, method, 'x'.repeat(43), { blob: share() })).status, 401, method)
  }
  // A revoked machine is shut out too (the token cache is per store).
  const other = await enroll(store, 'hare')
  const machines = store.table('machines')
  const row = await machines.get('m', other.machineId)
  await machines.replace({ ...row, revokedAt: NOW.toISOString() }, row.etag)
  assert.equal((await call(store, 'GET', other.token)).status, 401)
  assert.equal((await call(store, 'GET', token)).status, 404)
  const post = await call(store, 'POST', token, { blob: share() })
  assert.equal(post.status, 405)
  isPrivate(post)
  assert.equal(store._tables.get(FLEET_SECRETS)?.size ?? 0, 0)
  assert.ok(machineId)
})

test('put, get, replace and delete one share; the server never decodes it', async () => {
  const store = memoryStore()
  const a = await enroll(store, 'rabbit')
  const b = await enroll(store, 'hare')

  const missing = await call(store, 'GET', b.token)
  assert.equal(missing.status, 404)
  isPrivate(missing)

  const blob = share()
  const put = await call(store, 'PUT', a.token, { blob })
  assert.equal(put.status, 200)
  isPrivate(put)
  assert.deepEqual(put.jsonBody, { updatedAt: NOW.toISOString(), escrowed: false })

  // Any enrolled machine reads it back, with who shared it and when.
  const got = await call(store, 'GET', b.token)
  assert.equal(got.status, 200)
  isPrivate(got)
  assert.deepEqual(got.jsonBody, { blob, updatedAt: NOW.toISOString(), by: a.machineId })

  // The row holds the opaque blob, the time and the machine id: nothing else.
  const rows = [...store._tables.get(FLEET_SECRETS).values()].map(({ entity }) => entity)
  assert.deepEqual(rows, [{ partitionKey: 'fleet', rowKey: 'github', blob, updatedAt: NOW.toISOString(), by: a.machineId }])

  // A newer share replaces it.
  const later = new Date(NOW.getTime() + 60 * 60 * 1000)
  const blob2 = share(200)
  assert.equal((await call(store, 'PUT', b.token, { blob: blob2 }, later)).status, 200)
  assert.deepEqual((await call(store, 'GET', a.token)).jsonBody, { blob: blob2, updatedAt: later.toISOString(), by: b.machineId })

  // Invalid bodies change nothing.
  for (const body of [null, [], 'tmx1', {}, { blob: 42 }, { blob: 'nope' }, { blob: Buffer.from('plain').toString('base64') },
    { blob: Buffer.alloc(MAX_BLOB_BYTES + 1, 'tmx1').toString('base64') }]) {
    const r = await call(store, 'PUT', a.token, body)
    assert.equal(r.status, 400, JSON.stringify(body)?.slice(0, 60))
    isPrivate(r)
  }
  assert.equal((await call(store, 'GET', a.token)).jsonBody.blob, blob2)

  // Delete is idempotent and answers 204 with no body.
  for (let i = 0; i < 2; i++) {
    const del = await call(store, 'DELETE', a.token)
    assert.equal(del.status, 204)
    assert.equal(del.jsonBody, undefined)
    isPrivate(del)
  }
  assert.equal((await call(store, 'GET', b.token)).status, 404)
})

test('the blob never reaches the logs, even when storage fails', async () => {
  const store = memoryStore()
  const { token } = await enroll(store, 'rabbit')
  const blob = share()
  const table = store.table
  store.table = (name) => name === FLEET_SECRETS
    ? { ...table(name), async upsertMerge() { throw Object.assign(new Error('ServerBusy'), { statusCode: 503 }) } }
    : table(name)
  const lines = []
  const saved = { log: console.log, error: console.error, warn: console.warn }
  for (const k of Object.keys(saved)) console[k] = (...args) => lines.push(args.join(' '))
  let r
  try {
    r = await runHandler(handleFleetGitHub, {
      method: 'PUT',
      headers: { 'x-d0m1-token': token },
      readBody: async () => JSON.stringify({ blob }),
    }, { store, env: {} })
  } finally {
    Object.assign(console, saved)
  }
  assert.equal(r.status, 503)
  isPrivate(r)
  assert.ok(lines.length > 0)
  assert.ok(!lines.some((l) => l.includes(blob) || l.includes(blob.slice(0, 16))))
})

test('PUT says whether the fleet key is escrowed (a fleet linked through /api/link)', async () => {
  const store = memoryStore()
  const { token } = await enroll(store, 'rabbit')
  assert.equal((await call(store, 'PUT', token, { blob: share() })).jsonBody.escrowed, false)
  await store.table('accounts').create({ partitionKey: 'fleet', rowKey: 'kenc', enc: 'opaque', createdAt: NOW.toISOString() })
  assert.equal((await call(store, 'PUT', token, { blob: share() })).jsonBody.escrowed, true)
})

test('the answers runHandler makes itself are private too', async () => {
  const store = memoryStore()
  const { token } = await enroll(store, 'rabbit')
  const run = (headers, raw) => runHandler(handleFleetGitHub, { method: 'PUT', headers: { 'x-d0m1-token': token, ...headers }, readBody: async () => raw }, { store, env: {} })
  const bad = await run({}, '{nope')
  assert.equal(bad.status, 400)
  isPrivate(bad)
  const big = await run({ 'content-length': '9999999' }, '')
  assert.equal(big.status, 413)
  isPrivate(big)
  // Other handlers keep plain no-store.
  assert.equal((await runHandler(async () => ({}), { method: 'PUT', headers: {}, readBody: async () => '{nope' })).headers['Cache-Control'], 'no-store')
})

test('fleetsecrets is a known table, created on first use even without a prefix', () => {
  assert.ok(TABLES.includes(FLEET_SECRETS))
  assert.ok(CREATED_ON_USE.has(FLEET_SECRETS))
})

test('GET, PUT and DELETE /api/fleet/github over HTTP through the harness', async () => {
  const store = memoryStore()
  const server = createHarness({ store, env: {}, quiet: true })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const http = client(`http://127.0.0.1:${server.address().port}`)
  try {
    const { token, machineId } = await enroll(store, 'rabbit')
    const tok = { 'x-d0m1-token': token }
    assert.equal((await http('GET', '/api/fleet/github')).status, 401)
    assert.equal((await http('GET', '/api/fleet/github', { headers: tok })).status, 404)
    const blob = share()
    const put = await http('PUT', '/api/fleet/github', { headers: tok, body: { blob } })
    assert.equal(put.status, 200, put.raw)
    assert.equal(put.headers.get('cache-control'), 'private, no-store')
    const got = await http('GET', '/api/fleet/github', { headers: tok })
    assert.equal(got.status, 200)
    assert.equal(got.headers.get('cache-control'), 'private, no-store')
    assert.deepEqual(got.body, { blob, updatedAt: put.body.updatedAt, by: machineId })
    assert.equal((await http('PUT', '/api/fleet/github', { headers: tok, body: '{nope' })).status, 400)
    assert.equal((await http('POST', '/api/fleet/github', { headers: tok, body: { blob } })).status, 405)
    const del = await http('DELETE', '/api/fleet/github', { headers: tok })
    assert.equal(del.status, 204)
    assert.equal(del.raw, '')
    assert.equal((await http('DELETE', '/api/fleet/github', { headers: tok })).status, 204)
    assert.equal((await http('GET', '/api/fleet/github', { headers: tok })).status, 404)
  } finally {
    server.close()
  }
})
