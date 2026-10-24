import assert from 'node:assert/strict'
import { getGalleryCollectionBySlug, loadGalleryCollection } from './galleryCollections.ts'

const originalFetch = globalThis.fetch
const requests: string[] = []
const fixture = (slug: string) => ({
  collection: { slug, publicDir: slug, title: 'Fixture', source: 'manual' },
  structure: [{ name: slug, type: 'directory', children: [] }],
})
try {
  globalThis.fetch = async input => {
    requests.push(String(input))
    return Response.json(fixture('fixture-gallery'))
  }
  assert.equal(getGalleryCollectionBySlug('fixture-gallery'), null)
  assert.equal(await loadGalleryCollection('../private'), null)
  assert.equal(await loadGalleryCollection('nested/path'), null)
  assert.equal(requests.length, 0)
  const first = loadGalleryCollection('fixture-gallery')
  const duplicate = loadGalleryCollection('fixture-gallery')
  assert.equal(first, duplicate)
  assert.equal((await first)?.collection.slug, 'fixture-gallery')
  await loadGalleryCollection('fixture-gallery')
  assert.deepEqual(requests, ['/fixture-gallery/collection.json'])
  assert.equal(getGalleryCollectionBySlug('another-gallery'), null)

  globalThis.fetch = async () => new Response('<html>SPA fallback</html>', { headers: { 'content-type': 'text/html' } })
  assert.equal(await loadGalleryCollection('missing-gallery'), null)
  globalThis.fetch = async () => Response.json(fixture('other-gallery'))
  assert.equal(await loadGalleryCollection('missing-gallery'), null)
  globalThis.fetch = async () => Response.json({ ...fixture('missing-gallery'), structure: [...fixture('missing-gallery').structure, ...fixture('other-gallery').structure] })
  assert.equal(await loadGalleryCollection('missing-gallery'), null)
  globalThis.fetch = async () => Response.json(fixture('missing-gallery'))
  assert.equal((await loadGalleryCollection('missing-gallery'))?.collection.slug, 'missing-gallery')
} finally {
  globalThis.fetch = originalFetch
}
console.log('Collection isolation, lazy loading, invalid routes and retry checks passed')
