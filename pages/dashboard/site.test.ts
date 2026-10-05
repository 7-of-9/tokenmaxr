// site.ts: the header's GitHub user and breadcrumb, the calendar's colours and the Sign out / Sign in toggle.
// Run: node --experimental-strip-types pages/dashboard/site.test.ts
import assert from 'node:assert/strict'
import { githubUser, headerCrumbs, PAGES_HEAT_LEVELS, storedViewToggle } from './site.ts'

assert.equal(githubUser('7-of-9.github.io'), '7-of-9')
assert.equal(githubUser('Someone.GitHub.io'), 'someone')
assert.equal(githubUser('tokens.example.com'), null)
assert.equal(githubUser('github.io'), null)

// "<owner> / <repository>" as GitHub writes it, each linking to it on GitHub; without a repository, the title.
assert.deepEqual(headerCrumbs('7-of-9/tokenmaxr-usage', 'AI token usage'), {
  owner: { label: '7-of-9', href: 'https://github.com/7-of-9' },
  repository: { label: 'tokenmaxr-usage', href: 'https://github.com/7-of-9/tokenmaxr-usage' },
})
assert.deepEqual(headerCrumbs('someone/someone.github.io', 'x').repository, { label: 'someone.github.io', href: 'https://github.com/someone/someone.github.io' })
assert.deepEqual(headerCrumbs(null, 'AI token usage'), { owner: null, repository: { label: 'AI token usage' } })
for (const odd of ['', 'octo', '/usage', 'octo/']) assert.deepEqual(headerCrumbs(odd, 't'), { owner: null, repository: { label: 't' } }, odd)

// The Detail calendar follows github.css's light and dark greens.
assert.deepEqual(PAGES_HEAT_LEVELS, ['var(--pages-heat-0)', 'var(--pages-heat-1)', 'var(--pages-heat-2)', 'var(--pages-heat-3)', 'var(--pages-heat-4)'])

// Signed out is remembered per dashboard; other tabs (storage events) follow.
const store = new Map<string, string>()
const storage = {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => { store.set(k, v) },
  removeItem: (k: string) => { store.delete(k) },
} as unknown as Storage
const handlers = new Set<(e: StorageEvent) => void>()
const events = {
  addEventListener: (_: string, h: EventListener) => { handlers.add(h as unknown as (e: StorageEvent) => void) },
  removeEventListener: (_: string, h: EventListener) => { handlers.delete(h as unknown as (e: StorageEvent) => void) },
} as unknown as Window
const view = storedViewToggle(storage, 'dash:signed-out', events)
let changes = 0
const stop = view.subscribe(() => { changes++ })
assert.equal(view.signedOut(), false)
view.setSignedOut(true)
assert.equal(view.signedOut(), true)
assert.equal(store.get('dash:signed-out'), '1')
view.setSignedOut(false)
assert.equal(store.has('dash:signed-out'), false)
assert.equal(changes, 2)
// Another tab signs out.
store.set('dash:signed-out', '1')
for (const h of handlers) h({ key: 'dash:signed-out' } as StorageEvent)
assert.equal(view.signedOut(), true)
assert.equal(changes, 3)
stop()
assert.equal(handlers.size, 0)
// A new page in this browser starts signed out.
assert.equal(storedViewToggle(storage, 'dash:signed-out', null).signedOut(), true)
console.log('site.ts: GitHub user, header breadcrumb, calendar colours and Sign out / Sign in toggle')
