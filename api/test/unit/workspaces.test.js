// Workspace canonicalisation (SPEC "Workspaces"). The vectors are shared
// with the collector's Go mirror, so both sides apply the same rules.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { canonicalize, decodeSlug, isGeneric, normalize, workspaceGroups } from '../../src/lib/workspaces.js'

const vectors = JSON.parse(readFileSync(fileURLToPath(new URL('../../../collector/internal/sources/workspace/testdata/vectors.json', import.meta.url)), 'utf8'))

test('normalize (shared vectors)', () => {
  for (const [raw, want] of vectors.normalize) assert.equal(normalize(raw), want, raw)
})

test('decodeSlug (shared vectors)', () => {
  for (const [slug, want] of vectors.decodeSlug) assert.equal(decodeSlug(slug), want, slug)
})

test('isGeneric (shared vectors)', () => {
  for (const [key, want] of vectors.generic) assert.equal(isGeneric(key), want, key)
})

test('canonicalize (shared vectors)', () => {
  for (const c of vectors.canonicalize) {
    const got = canonicalize(c.inputs)
    assert.deepEqual(Object.fromEntries(got), c.expect, c.name)
  }
})

test('workspace groups: one row per root, original case, largest first', () => {
  const c = vectors.canonicalize[0]
  const groups = workspaceGroups(c.inputs)
  const byLabel = Object.fromEntries(groups.map((g) => [g.label, g]))
  assert.equal(byLabel['alpha-app'].count, 145 + 23 + 4)
  assert.equal(byLabel['alpha-app'].path, 'C:\\Users\\User\\src\\alpha-app')
  assert.deepEqual(byLabel['alpha-app'].paths, ['C:\\Users\\User\\src\\alpha-app', 'C:\\Users\\User\\SRC\\alpha-app', 'C--Users-User-src-alpha-app'])
  assert.equal(byLabel['example.com'].count, 200 + 30 + 3 + 5)
  assert.equal(byLabel.gamma.count, 194 + 22)
  assert.equal(byLabel.gamma.path, 'C:\\Users\\User\\src\\gamma')
  // Decoded only from a slug: the key is the path, labelled by its basename.
  assert.equal(byLabel['beta-wallet'].path, '/home/alex/src/beta-wallet')
  assert.equal(byLabel['(no workspace)'].key, 'none')
  assert.equal(groups[0].label, 'example.com')
  assert.equal(new Set(groups.map((g) => g.key)).size, groups.length)
  assert.equal(groups.length, 11)
})
