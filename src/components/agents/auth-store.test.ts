import assert from 'node:assert/strict'
import { authUrls, createAuthStore } from './auth-store.ts'

const principal = { identityProvider: 'github', userId: 'owner-id', userDetails: 'owner', userRoles: ['anonymous', 'authenticated'] }
const signedIn = () => Response.json({ clientPrincipal: principal })

// Header and page share one request and a minute of cache, regardless of rerenders.
{
  let calls = 0
  let now = 1000
  let finish!: (response: Response) => void
  const store = createAuthStore(() => { calls++; return new Promise(resolve => { finish = resolve }) }, () => now)
  const first = store.refresh()
  assert.equal(store.refresh(), first)
  await Promise.resolve()
  assert.equal(calls, 1)
  finish(signedIn())
  await first
  assert.equal(store.getSnapshot().status, 'signed-in')
  now += 59_000
  await store.refresh()
  assert.equal(calls, 1)
  now += 1000
  const refreshed = store.refresh()
  await Promise.resolve()
  assert.equal(calls, 2)
  finish(signedIn())
  await refreshed
}

// A protected API's 401 immediately clears every subscriber and cannot be undone by an older /.auth/me.
{
  let finish!: (response: Response) => void
  const store = createAuthStore(() => new Promise(resolve => { finish = resolve }))
  const states: string[] = []
  const unsubscribe = store.subscribe(() => states.push(store.getSnapshot().status))
  const request = store.refresh()
  await Promise.resolve()
  store.clear()
  assert.equal(store.getSnapshot().status, 'anon')
  finish(signedIn())
  await request
  assert.equal(store.getSnapshot().status, 'anon')
  assert.deepEqual(states, ['anon'])
  unsubscribe()
}

// A network failure is not a logout. Initial failures remain retryable; real 401 is anonymous.
{
  let mode: 'signed-in' | 'error' | 'unauthorized' = 'error'
  const store = createAuthStore(() => {
    if (mode === 'error') throw new Error('offline')
    return Promise.resolve(mode === 'signed-in' ? signedIn() : new Response(null, { status: 401 }))
  })
  await store.refresh()
  assert.equal(store.getSnapshot().status, 'no-auth')
  mode = 'signed-in'
  await store.refresh(true)
  assert.equal(store.getSnapshot().status, 'signed-in')
  mode = 'error'
  await store.refresh(true)
  assert.equal(store.getSnapshot().status, 'signed-in')
  mode = 'unauthorized'
  await store.refresh(true)
  assert.equal(store.getSnapshot().status, 'anon')
}

// An anonymous or malformed principal never produces an authenticated header.
{
  for (const clientPrincipal of [null, {}, { ...principal, userRoles: ['anonymous'] }, { ...principal, userId: '' }]) {
    const store = createAuthStore(() => Promise.resolve(Response.json({ clientPrincipal })))
    await store.refresh()
    assert.equal(store.getSnapshot().status, 'anon')
  }
}

// Login/logout return to the same local route, retaining collector state, filters, and fragments.
{
  const location = { pathname: '/collector/link', search: '?port=51234&state=example_state_1234', hash: '#status' }
  const urls = authUrls(location)
  assert.equal(new URL(urls.signIn, 'http://localhost:5173').origin, 'http://localhost:5173')
  assert.equal(new URL(urls.signIn, 'https://d0m1.com').searchParams.get('post_login_redirect_uri'), location.pathname + location.search + location.hash)
  assert.equal(new URL(urls.signOut, 'http://localhost:5173').searchParams.get('post_logout_redirect_uri'), location.pathname + location.search + location.hash)
  assert.match(authUrls({ pathname: '//evil.example', search: '', hash: '' }).signIn, /%2Ftokens$/)
}

console.log('auth-store.test.ts: ok')
