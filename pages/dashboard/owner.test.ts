// The owner files (owner.ts): the contract's test vector (testdata/owner-vector.mjs, node:crypto) opened with
// WebCrypto as the browser opens it, wrong keys, tampering, the machine binding, the unlock link, how the key and
// the Agents page's choices are kept on an origin other sites share, and the adapter's reads. Run: node --experimental-strip-types --no-warnings pages/dashboard/owner.test.ts
import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { LimitsDenied, type LimitRow } from '../../src/components/agents/limits.ts'
import { githubSource } from './githubSource.ts'
import {
  consumeUnlockFragment, createOwnerStore, createUnlockHandoff, decryptOwnerFile, fromBase64, importOwnerKey, memoryVault, mergeLimitRows,
  openPrefs, OWNER_CONTEXT, ownerStorageName, parseOwnerKey, toBase64Url, UNLOCK_KEY, UNLOCK_READY, type HandoffStatus, type OwnerStore,
} from './owner.ts'
// @ts-expect-error -- plain JS module without types (the test vector generator).
import { deriveOwnerKey, encryptOwnerFile, FLEET_KEY, MACHINE, OTHER_MACHINE, ownerVector, VECTOR_PATH } from './testdata/owner-vector.mjs'

interface Vector {
  fleetKey: string
  ownerKey: string
  ownerKeyBase64Url: string
  unlockFragment: string
  machine: string
  aad: string
  plaintext: string
  items: LimitRow[]
  file: { schema: 1; machine: string; alg: string; nonce: string; data: string }
}

const vector = JSON.parse(readFileSync(VECTOR_PATH, 'utf8')) as Vector
const keyBytes = fromBase64(vector.ownerKey)!
const bytes = (b: Uint8Array) => Uint8Array.from(b)
/** As the page holds it: non-extractable. (importOwnerKey wipes nothing; the store does.) */
const key = (await importOwnerKey(Uint8Array.from(keyBytes)))!
const imported = async (b: Uint8Array) => (await importOwnerKey(Uint8Array.from(b)))!

test('the committed vector is what the generator makes', () => {
  assert.deepEqual(vector, JSON.parse(JSON.stringify(ownerVector())), 'run node pages/dashboard/testdata/owner-vector.mjs')
  assert.equal(vector.machine, MACHINE)
  assert.equal(vector.aad, `${OWNER_CONTEXT}|${MACHINE}`)
})

test('the owner key is HMAC-SHA256(fleet key, context), the same under WebCrypto', async () => {
  const hmac = await crypto.subtle.importKey('raw', fromBase64(vector.fleetKey)!, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
  const derived = new Uint8Array(await crypto.subtle.sign('HMAC', hmac, new TextEncoder().encode(OWNER_CONTEXT)))
  assert.deepEqual(derived, bytes(keyBytes))
  assert.deepEqual(bytes(deriveOwnerKey(FLEET_KEY)), bytes(keyBytes))
  assert.equal(toBase64Url(keyBytes), vector.ownerKeyBase64Url)
  assert.deepEqual(parseOwnerKey(vector.ownerKeyBase64Url), keyBytes)
})

test('WebCrypto opens the vector to exactly the API rows', async () => {
  const items = await decryptOwnerFile(vector.file, MACHINE, key)
  assert.deepEqual(items, vector.items)
  assert.deepEqual(JSON.parse(vector.plaintext), { v: 1, items: vector.items })
  // The rows carry what only the owner may read.
  assert.ok(items!.some(r => r.label === 'owner@example.com') && items!.some(r => r.name === 'Example Org') && items!.some(r => r.plan === 'Max (20x)'))
})

test('a round trip with a fresh nonce and another machine', async () => {
  const items = vector.items.slice(0, 2)
  const file = encryptOwnerFile({ ownerKey: Buffer.from(keyBytes), machine: OTHER_MACHINE, plaintext: JSON.stringify({ v: 1, items }), nonce: randomBytes(12) })
  assert.deepEqual(await decryptOwnerFile(file, OTHER_MACHINE, key), items)
})

test('the wrong key, or any tampering, does not open (and never throws)', async () => {
  assert.equal(await decryptOwnerFile(vector.file, MACHINE, await imported(Uint8Array.from(keyBytes, b => b ^ 1))), null)
  assert.equal(await decryptOwnerFile(vector.file, MACHINE, await imported(deriveOwnerKey(Buffer.alloc(32)))), null)
  // An AES-128 key made from half of it is not an owner key.
  const half = await crypto.subtle.importKey('raw', keyBytes.slice(0, 16), { name: 'AES-GCM' }, false, ['decrypt'])
  assert.equal(await decryptOwnerFile(vector.file, MACHINE, half), null)

  const flip = (b64: string, at: number) => {
    const data = fromBase64(b64)!
    data[at < 0 ? data.length + at : at] ^= 0x80
    return Buffer.from(data).toString('base64')
  }
  for (const altered of [
    { ...vector.file, data: flip(vector.file.data, 0) }, // ciphertext
    { ...vector.file, data: flip(vector.file.data, -1) }, // tag
    { ...vector.file, nonce: flip(vector.file.nonce, 0) },
    { ...vector.file, data: vector.file.data.slice(0, 8) },
    { ...vector.file, alg: 'A128GCM' },
    { ...vector.file, schema: 2 },
    { ...vector.file, nonce: '!!!!' },
    null, 'owner', [],
  ]) assert.equal(await decryptOwnerFile(altered, MACHINE, key), null)
})

test('the additional data binds a file to its machine', async () => {
  // Moved to another machine's folder: the folder's id does not match the file.
  assert.equal(await decryptOwnerFile(vector.file, OTHER_MACHINE, key), null)
  // Relabelled to match that folder: the AAD (the original machine) no longer matches.
  assert.equal(await decryptOwnerFile({ ...vector.file, machine: OTHER_MACHINE }, OTHER_MACHINE, key), null)
})

test('an opened file with an unknown version gives no rows (it is not the wrong key)', async () => {
  const file = encryptOwnerFile({ ownerKey: Buffer.from(keyBytes), machine: MACHINE, plaintext: JSON.stringify({ v: 2, rows: [] }), nonce: randomBytes(12) })
  assert.deepEqual(await decryptOwnerFile(file, MACHINE, key), [])
})

test('merged as the API merges: one row per id, the newest reading wins, the API order', () => {
  const row = (over: Partial<LimitRow>): LimitRow => ({ id: 'a'.repeat(32), provider: 'anthropic', source: 'claude-code', acct: '', acctQ: 'unknown', window: 'week', observedAt: '2026-10-01T10:00:00.000Z', ...over })
  const older = row({ usedPercent: 10, observedAt: '2026-09-30T10:00:00.000Z' })
  const newer = row({ usedPercent: 60 })
  const same = row({ usedPercent: 99 })
  const codex = row({ id: 'b'.repeat(32), provider: 'openai', source: 'codex' })
  const session = row({ id: 'c'.repeat(32), window: 'session' })
  const merged = mergeLimitRows([[codex, older], [newer, session], [same]])
  assert.deepEqual(merged.map(r => [r.id[0], r.usedPercent]), [['c', undefined], ['a', 60], ['b', undefined]])
})

// ---- Unlocking ----

function memoryStorage(fail = false) {
  const map = new Map<string, string>()
  return {
    map,
    getItem: (k: string) => { if (fail) throw new Error('blocked'); return map.get(k) ?? null },
    setItem: (k: string, v: string) => { if (fail) throw new Error('blocked'); map.set(k, v) },
    removeItem: (k: string) => { if (fail) throw new Error('blocked'); map.delete(k) },
  }
}

function fakeHistory() {
  const urls: string[] = []
  return { urls, replaceState: (_state: unknown, _unused: string, url?: string | URL | null) => { urls.push(String(url)) } }
}

/** One BroadcastChannel name across tabs: a message reaches every other tab. */
function fakeBus() {
  const tabs: Array<(event: MessageEvent) => void> = []
  return () => {
    let mine: ((event: MessageEvent) => void) | undefined
    return {
      postMessage: (data: unknown) => tabs.filter(l => l !== mine).forEach(l => l({ data } as MessageEvent)),
      addEventListener: (_type: 'message', listener: (event: MessageEvent) => void) => { mine = listener; tabs.push(listener) },
    }
  }
}

/** Until the store's background saves have landed. */
async function until(check: () => boolean) {
  for (let i = 0; i < 200 && !check(); i++) await new Promise(resolve => setTimeout(resolve, 1))
  assert.ok(check(), 'timed out')
}

/** Everything a page on the same origin could read: no form of the key, no owner data. */
function assertNothingReadable(storage: { map: Map<string, string> }, ...secrets: string[]) {
  const all = JSON.stringify([...storage.map])
  for (const secret of [...keyForms(), ...secrets]) assert.ok(!all.includes(secret), `storage holds ${secret}`)
}

test('the unlock link: the key leaves the address bar and stays only as a non-extractable CryptoKey', async () => {
  const storage = memoryStorage()
  const vault = memoryVault()
  const name = ownerStorageName('/tokenmaxr-usage/')
  assert.equal(name, ownerStorageName('/tokenmaxr-usage/index.html'))
  assert.notEqual(name, ownerStorageName('/another-repository/'))
  storage.setItem(name, vector.ownerKeyBase64Url) // the first build kept the key's text here
  const store = createOwnerStore({ name, vault, storage })
  assert.equal(store.state().status, 'loading')
  assert.equal(await store.ready(), null)
  assert.equal(store.state().status, 'locked')
  assert.equal(storage.map.size, 0, 'the old text key is removed')

  const history = fakeHistory()
  const location = { pathname: '/tokenmaxr-usage/', search: '', hash: vector.unlockFragment }
  assert.equal(consumeUnlockFragment(location, history, store), 'unlocked')
  assert.deepEqual(history.urls, ['/tokenmaxr-usage/#/agents'], 'lands on the Agents page')
  const held = await store.ready()
  assert.ok(held instanceof CryptoKey)
  assert.equal(held.extractable, false)
  await assert.rejects(crypto.subtle.exportKey('raw', held), 'the key cannot be copied out')
  assert.equal(store.state().status, 'unlocked')
  assert.equal(vault.map.get(name), held)
  assert.deepEqual(await decryptOwnerFile(vector.file, MACHINE, held), vector.items)
  assertNothingReadable(storage)

  // A later visit (another store over the same vault) is unlocked; another repository's dashboard is not.
  assert.equal(await createOwnerStore({ name, vault, storage }).ready(), held)
  assert.equal(await createOwnerStore({ name: ownerStorageName('/another-repository/'), vault, storage }).ready(), null)

  // Lock forgets it, everywhere.
  const seen: string[] = []
  store.subscribe(() => seen.push(store.state().status))
  store.lock()
  assert.equal(store.get(), null)
  await store.ready()
  assert.equal(vault.map.size, 0)
  assert.deepEqual(seen, ['locked'])
})

test('the unlock link’s bytes are wiped once the key is held', async () => {
  const bytes = Uint8Array.from(keyBytes)
  const store = createOwnerStore({ name: 'k', vault: memoryVault() })
  await store.unlock(bytes)
  assert.ok(store.get())
  assert.ok(bytes.every(b => b === 0))
  // Too short for AES-256: nothing is held.
  assert.equal(await importOwnerKey(keyBytes.slice(0, 16)), null)
})

test('an unlock link with a bad key is removed too, and unlocks nothing', async () => {
  for (const hash of ['#unlock=', '#unlock=abc', '#unlock=%E0%A4%A', `#unlock=${vector.ownerKey}`]) {
    const store = createOwnerStore({ name: 'k', vault: memoryVault(), storage: memoryStorage() })
    const history = fakeHistory()
    assert.equal(consumeUnlockFragment({ pathname: '/r/', search: '', hash }, history, store), 'invalid', hash)
    assert.deepEqual(history.urls, ['/r/#/agents'])
    assert.equal(await store.ready(), null)
  }
  const history = fakeHistory()
  const store = createOwnerStore({ name: 'k', vault: memoryVault() })
  assert.equal(consumeUnlockFragment({ pathname: '/r/', search: '', hash: '#/agents?period=90d' }, history, store), 'none')
  assert.deepEqual(history.urls, [])
})

test('a vault or storage that refuses keeps the key for this tab', async () => {
  const refuses = { load: () => Promise.reject(new Error('no')), save: () => Promise.reject(new Error('no')), remove: () => Promise.reject(new Error('no')) }
  const store = createOwnerStore({ name: 'k', vault: refuses, storage: memoryStorage(true) })
  assert.equal(await store.ready(), null)
  await store.unlock(Uint8Array.from(keyBytes))
  assert.ok(store.get())
  store.prefs.set('d0m1.agents.tracking.v1', '{}')
  assert.equal(store.prefs.get('d0m1.agents.tracking.v1'), '{}')
  store.lock()
  assert.equal(store.get(), null)
})

test('this dashboard’s other tabs: unlocks and locks arrive', async () => {
  const vault = memoryVault()
  const tab = fakeBus()
  const a = createOwnerStore({ name: 'k', vault, channel: tab() })
  const b = createOwnerStore({ name: 'k', vault, channel: tab() })
  await Promise.all([a.ready(), b.ready()])
  await a.unlock(Uint8Array.from(keyBytes))
  assert.equal(await b.ready(), a.get())
  a.lock()
  assert.equal(b.get(), null)
  assert.equal(b.state().status, 'locked')
})

test('the Agents page’s choices are sealed with the key, for this dashboard only, and Lock removes them', async () => {
  const storage = memoryStorage()
  const vault = memoryVault()
  const name = ownerStorageName('/tokenmaxr-usage/')
  const TRACKING = 'd0m1.agents.tracking.v1'
  const choices = JSON.stringify({ '["anthropic","email","owner@example.com"]': true, '["anthropic","email","work@example.org"]': false })
  let storageEvent: ((event: StorageEvent) => void) | undefined
  const store = createOwnerStore({ name, vault, storage, events: { addEventListener: (_type, listener) => { storageEvent = listener } } })
  await store.ready()

  // Locked (?mock=1): kept for this tab only.
  store.prefs.set(`${TRACKING}.mock`, '{"x":true}')
  assert.equal(store.prefs.get(`${TRACKING}.mock`), '{"x":true}')
  assert.equal(storage.map.size, 0)

  await store.unlock(Uint8Array.from(keyBytes))
  const heard: Array<string | null> = []
  store.prefs.subscribe(TRACKING, value => heard.push(value))
  store.prefs.set(TRACKING, choices)
  assert.deepEqual(heard, [], 'the page’s own change is not echoed back to it')
  assert.equal(store.prefs.get(TRACKING), choices)
  await until(() => storage.map.has(`${name}:prefs`))
  assertNothingReadable(storage, 'owner@example.com', 'work@example.org', 'anthropic')

  // A later visit reads them back; another dashboard on the origin, or another key, cannot.
  const later = createOwnerStore({ name, vault, storage })
  await later.ready()
  assert.equal(later.prefs.get(TRACKING), choices)
  const sealed = storage.map.get(`${name}:prefs`)!
  assert.equal(await openPrefs(sealed, later.get()!, ownerStorageName('/another-repository/')), null)
  assert.equal(await openPrefs(sealed, (await importOwnerKey(Uint8Array.from(keyBytes, b => b ^ 1)))!, name), null)

  // Another tab's change arrives through the storage event.
  later.prefs.set(TRACKING, '{}')
  await until(() => storage.map.get(`${name}:prefs`) !== sealed)
  storageEvent!({ key: `${name}:prefs` } as StorageEvent)
  await until(() => heard.length === 1)
  assert.deepEqual(heard, ['{}'])

  store.lock()
  assert.equal(store.prefs.get(TRACKING), null)
  assert.deepEqual(heard, ['{}', null])
  assert.equal(storage.map.size, 0)
})

// ---- The adapter ----

const A = MACHINE
const B = OTHER_MACHINE

function fixtures(schema: number, list = true) {
  const bRows = [
    { ...vector.items[1], usedPercent: 10, observedAt: '2026-09-30T00:00:00.000Z' }, // an older reading of A's row
    { ...vector.items[5], id: 'b'.repeat(32), label: 'side@example.net', plan: 'Plus' },
  ]
  const bFile = encryptOwnerFile({ ownerKey: Buffer.from(keyBytes), machine: B, plaintext: JSON.stringify({ v: 1, items: bRows }), nonce: randomBytes(12) })
  const files = (owner: boolean) => ['meta.json', ...(owner && list ? ['owner.json'] : []), 'usage-2026-09.json']
  return {
    'data/index.json': { schema, machines: [{ id: A, files: files(true) }, { id: B, files: files(true) }, { id: 'm_cccccccccccc', files: files(false) }] },
    [`data/machines/${A}/owner.json`]: vector.file,
    [`data/machines/${B}/owner.json`]: bFile,
  } as Record<string, unknown>
}

interface Request { url: string; init?: RequestInit }

function fakeFetch(files: Record<string, unknown>, requests: Request[]): typeof fetch {
  return async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input)
    requests.push({ url, init })
    if (!(url in files)) return new Response('not found', { status: 404 })
    return new Response(JSON.stringify(files[url]), { headers: { 'content-type': 'application/json' } })
  }
}

function keyForms() {
  return [vector.ownerKey, vector.ownerKeyBase64Url, Buffer.from(keyBytes).toString('hex'), vector.fleetKey]
}

function assertKeyNeverSent(requests: Request[]) {
  for (const r of requests) {
    const sent = JSON.stringify({ url: r.url, init: r.init, headers: r.init?.headers instanceof Headers ? [...r.init.headers] : r.init?.headers })
    for (const form of keyForms()) assert.ok(!sent.includes(form), `request ${r.url} carries the key`)
  }
}

const signal = () => new AbortController().signal

test('unlocked: every machine’s owner file, merged; the key is in no request', async () => {
  const requests: Request[] = []
  const source = githubSource({ fetch: fakeFetch(fixtures(3), requests), ownerKey: () => key })
  const rows = await source.fetchLimits(signal())
  assert.equal(rows.length, vector.items.length + 1)
  assert.equal(rows.find(r => r.id === vector.items[1].id)!.usedPercent, 64, 'the newer reading wins')
  assert.ok(rows.some(r => r.label === 'side@example.net'))
  assert.deepEqual(requests.map(r => r.url), ['data/index.json', `data/machines/${A}/owner.json`, `data/machines/${B}/owner.json`])
  assert.ok(requests.every(r => !r.url.startsWith('/') && !r.url.includes('#') && !r.url.includes('?')))
  assertKeyNeverSent(requests)
})

test('locked: nothing is read; the wrong key: denied, not a crash', async () => {
  const requests: Request[] = []
  await assert.rejects(githubSource({ fetch: fakeFetch(fixtures(3), requests) }).fetchLimits(signal()),
    (err: unknown) => err instanceof LimitsDenied && err.status === 401)
  assert.deepEqual(requests, [])

  const wrong = (await importOwnerKey(Uint8Array.from(keyBytes, b => b ^ 0xff)))!
  await assert.rejects(githubSource({ fetch: fakeFetch(fixtures(3), requests), ownerKey: () => wrong }).fetchLimits(signal()),
    (err: unknown) => err instanceof LimitsDenied && err.status === 403)
  assertKeyNeverSent(requests)

  // Unlocked, but nothing published yet: no accounts, not a denial.
  assert.deepEqual(await githubSource({ fetch: fakeFetch(fixtures(3, false), []), ownerKey: () => key }).fetchLimits(signal()), [])
})

test('an index older than schema 3 cannot list owner.json: an unlocked page looks for it itself (404: none)', async () => {
  for (const schema of [1, 2]) {
    const requests: Request[] = []
    const rows = await githubSource({ fetch: fakeFetch(fixtures(schema, false), requests), ownerKey: () => key }).fetchLimits(signal())
    assert.equal(rows.length, vector.items.length + 1)
    assert.ok(requests.some(r => r.url === 'data/machines/m_cccccccccccc/owner.json'), 'a machine without one is a 404, not an error')
    assertKeyNeverSent(requests)
  }

  // A schema 3 index is trusted: nothing unlisted is requested.
  const asked: Request[] = []
  await githubSource({ fetch: fakeFetch(fixtures(3, false), asked), ownerKey: () => key }).fetchLimits(signal())
  assert.deepEqual(asked.map(r => r.url), ['data/index.json'])
})

test('the store’s key is read at each load: an unlock under way is waited for, a lock takes effect at once', async () => {
  const store: OwnerStore = createOwnerStore({ name: 'k', vault: memoryVault() })
  const source = githubSource({ fetch: fakeFetch(fixtures(3), []), ownerKey: store.ready })
  await assert.rejects(source.fetchLimits(signal()), LimitsDenied)
  void store.unlock(Uint8Array.from(keyBytes))
  assert.equal((await source.fetchLimits(signal())).length, vector.items.length + 1)
  store.lock()
  await assert.rejects(source.fetchLimits(signal()), LimitsDenied)
})

// The handoff from tokenmaxr's settings: the settings page opens <dashboard>#unlock and hands the key over by
// postMessage, so the key is never in an address (or the history).
function handoffWindow(opener: { postMessage(message: unknown, targetOrigin: string): void } | null) {
  const listeners = new Set<(event: MessageEvent) => void>()
  return {
    opener,
    addEventListener: (_: 'message', l: (event: MessageEvent) => void) => { listeners.add(l) },
    removeEventListener: (_: 'message', l: (event: MessageEvent) => void) => { listeners.delete(l) },
    send: (source: unknown, data: unknown) => listeners.forEach(l => l({ source, data } as unknown as MessageEvent)),
    listeners,
  }
}
const at = (hash: string) => ({ pathname: '/tokenmaxr-usage/', search: '', hash })
function recordHistory() {
  const urls: string[] = []
  return { urls, replaceState: (_: unknown, __: string, url?: string | URL | null) => { urls.push(String(url)) } }
}

test('handoff: a bare #unlock asks the opener, takes only its key and lands on the Agents page', async () => {
  const posted: unknown[] = []
  const opener = { postMessage: (m: unknown, origin: string) => { posted.push([m, origin]) } }
  const win = handoffWindow(opener)
  const unlocked: Uint8Array[] = []
  const handoff = createUnlockHandoff({ window: win, store: { unlock: async (b) => { unlocked.push(b) } }, timeoutMs: 1000 })
  const history = recordHistory()
  assert.equal(handoff.consume(at('#unlock'), history), true)
  assert.deepEqual(history.urls, ['/tokenmaxr-usage/#/agents'])
  assert.deepEqual(posted, [[{ type: UNLOCK_READY }, '*']])
  const statuses: HandoffStatus[] = [handoff.status()]
  assert.deepEqual(statuses, ['waiting'])
  const key = toBase64Url(randomBytes(32))
  win.send({}, { type: UNLOCK_KEY, key })            // another window: ignored
  win.send(opener, { type: 'other', key })           // another message: ignored
  win.send(opener, { type: UNLOCK_KEY, key: 'nope' }) // not a key: ignored
  assert.equal(unlocked.length, 0)
  assert.equal(handoff.status(), 'waiting')
  win.send(opener, { type: UNLOCK_KEY, key })
  assert.equal(handoff.status(), 'done')
  assert.equal(unlocked.length, 1)
  assert.equal(toBase64Url(unlocked[0]), key)
  assert.equal(win.listeners.size, 0)
  win.send(opener, { type: UNLOCK_KEY, key })        // only once
  assert.equal(unlocked.length, 1)
})

test('handoff: no opener or no key in time fails quietly; other addresses are not a handoff', async () => {
  const lone = createUnlockHandoff({ window: handoffWindow(null), store: { unlock: async () => {} } })
  assert.equal(lone.consume(at('#/unlock'), recordHistory()), true)
  assert.equal(lone.status(), 'failed')

  const win = handoffWindow({ postMessage: () => {} })
  const slow = createUnlockHandoff({ window: win, store: { unlock: async () => { throw new Error('unexpected') } }, timeoutMs: 20 })
  for (const hash of ['', '#/', '#/agents', '#unlock=abc', '#unlocked']) assert.equal(slow.consume(at(hash), recordHistory()), false, hash)
  assert.equal(slow.status(), 'idle')
  assert.equal(slow.consume(at('#unlock'), recordHistory()), true)
  await new Promise(r => setTimeout(r, 60))
  assert.equal(slow.status(), 'failed')
  assert.equal(win.listeners.size, 0)

  const throwing = createUnlockHandoff({ window: handoffWindow({ postMessage: () => { throw new Error('closed') } }), store: { unlock: async () => {} } })
  assert.equal(throwing.consume(at('#unlock'), recordHistory()), true)
  assert.equal(throwing.status(), 'failed')
})
