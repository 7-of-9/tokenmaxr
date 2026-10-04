// node --test scripts/sync-pages-site.test.mjs
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { check, listFiles, siteHash, stampAndMirror } from './sync-pages-site.mjs'

function dirs(t) {
  const root = mkdtempSync(join(tmpdir(), 'pages-site-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const from = join(root, 'pages', 'site')
  const to = join(root, 'embed', 'site')
  mkdirSync(join(from, 'assets'), { recursive: true })
  writeFileSync(join(from, 'index.html'), '<!doctype html><script src="./assets/index-a.js"></script>\n')
  writeFileSync(join(from, 'assets', 'index-a.js'), 'console.log(1)\n')
  return { from, to }
}

const T0 = Date.parse('2026-10-04T10:00:00Z')

test('stamps the build and mirrors it exactly', (t) => {
  const { from, to } = dirs(t)
  const v = stampAndMirror(from, to, T0)
  assert.equal(v.builtAt, '2026-10-04T10:00:00Z')
  assert.equal(v.hash, siteHash(from))
  assert.deepEqual(listFiles(to), ['assets/index-a.js', 'index.html', 'version.json'])
  assert.deepEqual(JSON.parse(readFileSync(join(to, 'version.json'), 'utf8')), v)
  assert.deepEqual(check(from, to), [])
})

test('an unchanged rebuild keeps builtAt; a change moves it forward and drops stale files', (t) => {
  const { from, to } = dirs(t)
  const v1 = stampAndMirror(from, to, T0)
  // vite empties its output: version.json is gone until the script runs again.
  rmSync(join(from, 'version.json'))
  assert.deepEqual(stampAndMirror(from, to, T0 + 3600e3), v1, 'same files, same version')

  rmSync(join(from, 'assets', 'index-a.js'))
  writeFileSync(join(from, 'assets', 'index-b.js'), 'console.log(2)\n')
  const v2 = stampAndMirror(from, to, T0 + 7200e3)
  assert.notEqual(v2.hash, v1.hash)
  assert.equal(v2.builtAt, '2026-10-04T12:00:00Z')
  assert.deepEqual(listFiles(to), ['assets/index-b.js', 'index.html', 'version.json'])

  // A clock behind the last build still moves builtAt forward.
  writeFileSync(join(from, 'index.html'), 'changed\n')
  const v3 = stampAndMirror(from, to, T0)
  assert.equal(v3.builtAt, '2026-10-04T12:00:01Z')
})

test('check finds hand edits, missing stamps and differing copies', (t) => {
  const { from, to } = dirs(t)
  assert.ok(check(from, to).length, 'nothing stamped yet')
  stampAndMirror(from, to, T0)
  writeFileSync(join(to, 'assets', 'index-a.js'), 'edited\n')
  const problems = check(from, to)
  assert.ok(problems.some((p) => p.includes('does not match')), problems.join('; '))
  assert.ok(problems.some((p) => p.includes('index-a.js differs')), problems.join('; '))
  stampAndMirror(from, to, T0)
  writeFileSync(join(to, 'extra.js'), '1')
  assert.ok(check(from, to).some((p) => p.includes('different files')))
})

test('refuses to stamp an empty build', (t) => {
  const { from, to } = dirs(t)
  rmSync(join(from, 'index.html'))
  assert.throws(() => stampAndMirror(from, to, T0), /no index.html/)
  assert.equal(existsSync(join(from, 'version.json')), false)
})
