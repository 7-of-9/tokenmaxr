import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { base32, handleLink, NOT_ESCROWED } from '../../src/lib/link.js'
import { handleEnroll } from '../../src/lib/enroll.js'
import { memoryStore } from '../../src/lib/tables.js'
import { principal } from '../fixtures.js'

const ENV = { PROMPT_ENC_KEY: Buffer.alloc(32, 7).toString('base64') }
const OWNER = { 'x-ms-client-principal': principal('github', 'owner', ['anonymous', 'authenticated', 'owner']) }
const link = (store, headers = OWNER, env = ENV) => handleLink({ method: 'POST', headers, now: new Date() }, { store, env })

// The code as the collector's joincode.Parse splits it.
function parse(join) {
  const m = /^D0M1-([A-Z2-7]{24})-([A-Z2-7]{52})$/.exec(join)
  assert.ok(m, join)
  return { invite: m[1], k: m[2] }
}

test('base32 matches RFC 4648 without padding', () => {
  assert.equal(base32(Buffer.from('foobar')), 'MZXW6YTBOI')
  assert.equal(base32(Buffer.alloc(32)).length, 52)
})

test('link makes one fleet key, reuses it, and its code enrolls', async () => {
  const store = memoryStore()
  const a = await link(store)
  const b = await link(store)
  assert.equal(a.status, 200)
  const pa = parse(a.jsonBody.join)
  const pb = parse(b.jsonBody.join)
  assert.equal(pa.k, pb.k)
  assert.notEqual(pa.invite, pb.invite)

  // Decode K back to bytes for the fingerprint the collector sends.
  const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = 0
  let value = 0
  const bytes = []
  for (const c of pa.k) {
    value = (value << 5) | B32.indexOf(c)
    bits += 5
    if (bits >= 8) {
      bytes.push((value >>> (bits - 8)) & 255)
      bits -= 8
    }
  }
  const kFingerprint = createHash('sha256').update(Buffer.from(bytes)).digest('hex').slice(0, 16)
  const enroll = (invite) => handleEnroll({ method: 'POST', headers: {}, now: new Date(), body: { invite, machineLabel: 'box', os: 'darwin', arch: 'arm64', version: '0.2.0', kFingerprint } }, { store, env: ENV })
  assert.equal((await enroll(pa.invite)).status, 200)
  assert.equal((await enroll(pb.invite)).status, 200)
})

test('link is owner only and needs PROMPT_ENC_KEY', async () => {
  const store = memoryStore()
  assert.equal((await link(store, {})).status, 401)
  assert.equal((await link(store, { 'x-ms-client-principal': principal('github', 'someone') })).status, 403)
  assert.equal((await link(store, OWNER, {})).status, 503)
})

test('a machine-made fleet key that was never escrowed is not replaced', async () => {
  const store = memoryStore()
  await store.table('accounts').create({ partitionKey: 'fleet', rowKey: 'k', fingerprint: 'ab'.repeat(8) })
  const res = await link(store)
  assert.equal(res.status, 409)
  assert.equal(res.jsonBody.error, NOT_ESCROWED)
})
