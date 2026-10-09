import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { COOKIE, createLockedDocs, hashPassword, SESSION_SECONDS } from '../../src/lib/locked-docs.js'

// Low scrypt cost keeps the tests fast; production uses N = 2^15.
const SCRYPT = { N: 1024, r: 8, p: 1 }
const PASSWORD = 'correct horse'

function fixture(run) {
  const dir = mkdtempSync(join(tmpdir(), 'locked-docs-'))
  const salt = Buffer.alloc(16, 3).toString('base64')
  writeFileSync(join(dir, 'access.json'), JSON.stringify({ scrypt: SCRYPT, salt, hash: hashPassword(PASSWORD, salt, SCRYPT).toString('base64') }))
  writeFileSync(join(dir, 'index.json'), JSON.stringify({ cv: { file: 'cv_test.pdf', downloadName: 'cv_test.pdf' } }))
  writeFileSync(join(dir, 'cv_test.pdf'), '%PDF-1.4 test')
  let clock = Date.UTC(2026, 9, 7)
  const docs = createLockedDocs({ dir, now: () => clock })
  try {
    return run({ docs, dir, advance: (ms) => { clock += ms } })
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

const ip = { 'x-azure-clientip': '203.0.113.9' }
const unlock = (docs, password, headers = ip) => docs.unlock({ headers, body: JSON.stringify({ password }) })
const cookieFrom = (res) => res.headers['Set-Cookie'].split(';')[0]

test('the right password opens a session that serves the document', () => fixture(({ docs }) => {
  const res = unlock(docs, PASSWORD)
  assert.equal(res.status, 204)
  assert.match(res.headers['Set-Cookie'], /HttpOnly; Secure; SameSite=Strict/)
  assert.match(res.headers['Set-Cookie'], /Path=\/api\/locked/)
  const cookie = cookieFrom(res)
  assert.equal(docs.session({ headers: { cookie } }).status, 204)
  const doc = docs.doc({ headers: { cookie }, params: { id: 'cv' } })
  assert.equal(doc.status, 200)
  assert.equal(doc.headers['Content-Type'], 'application/pdf')
  assert.equal(doc.body.toString(), '%PDF-1.4 test')
}))

test('no session, a wrong password or a forged cookie gets nothing', () => fixture(({ docs }) => {
  assert.equal(docs.doc({ headers: {}, params: { id: 'cv' } }).status, 401)
  assert.equal(unlock(docs, 'wrong').status, 401)
  assert.equal(unlock(docs, '').status, 401)
  const forged = `${COOKIE}=${Math.floor(Date.UTC(2030, 0, 1) / 1000)}.AAAA`
  assert.equal(docs.session({ headers: { cookie: forged } }).status, 401)
  assert.equal(docs.doc({ headers: { cookie: forged }, params: { id: 'cv' } }).status, 401)
}))

test('a session expires, and unknown or unsafe ids are not served', () => fixture(({ docs, advance }) => {
  const cookie = cookieFrom(unlock(docs, PASSWORD))
  assert.equal(docs.doc({ headers: { cookie }, params: { id: 'nope' } }).status, 404)
  assert.equal(docs.doc({ headers: { cookie }, params: { id: '__proto__' } }).status, 404)
  advance(SESSION_SECONDS * 1000 + 1)
  assert.equal(docs.session({ headers: { cookie } }).status, 401)
}))

test('changing the password ends open sessions', () => fixture(({ docs, dir }) => {
  const cookie = cookieFrom(unlock(docs, PASSWORD))
  const salt = Buffer.alloc(16, 4).toString('base64')
  writeFileSync(join(dir, 'access.json'), JSON.stringify({ scrypt: SCRYPT, salt, hash: hashPassword('new password', salt, SCRYPT).toString('base64') }))
  assert.equal(docs.session({ headers: { cookie } }).status, 401)
}))

test('an empty password (open) serves the documents with no session', () => fixture(({ docs, dir }) => {
  writeFileSync(join(dir, 'access.json'), JSON.stringify({ open: true }))
  assert.equal(docs.session({ headers: {} }).status, 204)
  assert.equal(docs.doc({ headers: {}, params: { id: 'cv' } }).status, 200)
  assert.equal(docs.doc({ headers: {}, params: { id: 'nope' } }).status, 404)
  assert.equal(unlock(docs, '').status, 204)
}))

test('repeated failures from one client are throttled, others are not', () => fixture(({ docs, advance }) => {
  for (let i = 0; i < 8; i++) assert.equal(unlock(docs, 'guess').status, 401)
  const blocked = unlock(docs, PASSWORD)
  assert.equal(blocked.status, 429)
  assert.ok(Number(blocked.headers['Retry-After']) > 0)
  assert.equal(unlock(docs, PASSWORD, { 'x-azure-clientip': '198.51.100.7' }).status, 204)
  advance(15 * 60 * 1000 + 1)
  assert.equal(unlock(docs, PASSWORD).status, 204)
}))
