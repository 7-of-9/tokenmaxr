// The owner's plan limits on a public dashboard. GitHub Pages has no server to sign in to, so what only the owner may
// read on d0m1.com/tokens/agents (account emails, organisation names, plans) is published encrypted, one file per
// machine, and decrypted here, in the owner's browser, with the owner key: derived from the fleet key, which only
// the repository's collaborators can read (signin.ts signs in with GitHub to read it).
//
// Contract (the collector writes exactly this; testdata/owner-vector.mjs builds a test vector with node:crypto):
// - data/machines/<id>/owner.json = {"schema":1,"machine":"<id>","alg":"A256GCM","nonce":"<base64, 12 bytes>",
//   "data":"<base64 of ciphertext || 16-byte tag>"}. A schema 3 data/index.json lists it; under an older index
//   an unlocked page looks for it itself (a 404 is none).
// - Owner key (32 bytes) = HMAC-SHA256(key: the fleet key K, message: "tokenmaxr dashboard owner v1"). The page
//   never sees K, only this key.
// - AAD = UTF-8 "tokenmaxr dashboard owner v1|" + machine id, so a file only opens as its own machine's.
// - Plaintext = {"v":1,"items":[...]}: exactly the rows GET /api/limits returns on d0m1.com for this machine.
//
// Signing in, three ways: with GitHub (signin.ts, the device flow, on any device); the collector's settings page
// ("Open dashboard") opens <dashboard>#unlock and hands the key over with postMessage, so it is never in a URL (see
// "The collector's handoff" below; the fragment and message names are the collector's); or a copied link, <dashboard>#unlock=<base64url(owner key)>,
// whose key the page takes out of the address bar at once (history.replaceState; the browser's own history still
// records the link). Either way the page keeps it as a non-extractable CryptoKey in IndexedDB for this dashboard's
// path (see "Keeping the key"), and from then on decrypts every owner.json with WebCrypto. lock() forgets it. The key
// never goes into a request, a URL the page loads, or a log. Pure apart from the vault, storage and history passed in, so owner.test.ts runs it in Node.
import type { LimitRow } from '../../src/components/agents/limits.ts'

export const OWNER_CONTEXT = 'tokenmaxr dashboard owner v1'
export const OWNER_FILE = 'owner.json'
const KEY_BYTES = 32
const NONCE_BYTES = 12
const TAG_BYTES = 16

export interface OwnerFile {
  schema: 1
  machine: string
  alg: 'A256GCM'
  nonce: string
  data: string
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

// ---- Encodings ----

/** Standard base64 (padded), or null. */
export function fromBase64(text: string): Uint8Array<ArrayBuffer> | null {
  if (text.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(text)) return null
  try {
    return Uint8Array.from(atob(text), c => c.charCodeAt(0))
  } catch {
    return null
  }
}

/** base64url, padded or not, or null. */
export function fromBase64Url(text: string): Uint8Array<ArrayBuffer> | null {
  if (!/^[A-Za-z0-9_-]*={0,2}$/.test(text)) return null
  const plain = text.replace(/=+$/, '').replace(/-/g, '+').replace(/_/g, '/')
  return fromBase64(plain + '='.repeat((4 - (plain.length % 4)) % 4))
}

/** base64url without padding: the unlock link's form. */
export function toBase64Url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** A 32-byte owner key from its base64url text, or null. */
export function parseOwnerKey(text: string | null | undefined): Uint8Array<ArrayBuffer> | null {
  const bytes = typeof text === 'string' ? fromBase64Url(text.trim()) : null
  return bytes && bytes.length === KEY_BYTES ? bytes : null
}

/** The additional data that binds a file to its machine. */
export function ownerAad(machine: string): Uint8Array<ArrayBuffer> {
  return new TextEncoder().encode(`${OWNER_CONTEXT}|${machine}`)
}

// ---- Decrypting ----

/** A decrypted row is the owner's own authenticated data; only rows the Agents page cannot read are dropped. */
function isLimitRow(value: unknown): value is LimitRow {
  return isRecord(value) && ['id', 'provider', 'source', 'window', 'observedAt'].every(field => typeof value[field] === 'string')
}

/**
 * The limit rows in one machine's owner.json, or null when it does not open for this key and machine (the wrong
 * key, a file altered or moved from another machine, or not an owner file at all). A file that opens but holds
 * an unknown plaintext version gives no rows.
 */
export async function decryptOwnerFile(file: unknown, machine: string, key: CryptoKey,
  subtle: SubtleCrypto = globalThis.crypto.subtle): Promise<LimitRow[] | null> {
  if (!isRecord(file) || file.schema !== 1 || file.alg !== 'A256GCM' || file.machine !== machine ||
    typeof file.nonce !== 'string' || typeof file.data !== 'string' ||
    key.algorithm.name !== 'AES-GCM' || (key.algorithm as AesKeyAlgorithm).length !== KEY_BYTES * 8) return null
  const nonce = fromBase64(file.nonce)
  const data = fromBase64(file.data)
  if (!nonce || nonce.length !== NONCE_BYTES || !data || data.length < TAG_BYTES) return null
  let plain: ArrayBuffer
  try {
    plain = await subtle.decrypt({ name: 'AES-GCM', iv: nonce, additionalData: ownerAad(machine), tagLength: TAG_BYTES * 8 }, key, data)
  } catch {
    return null
  }
  try {
    const body: unknown = JSON.parse(new TextDecoder().decode(plain))
    return isRecord(body) && body.v === 1 && Array.isArray(body.items) ? body.items.filter(isLimitRow) : []
  } catch {
    return []
  }
}

const PROVIDER_ORDER = ['anthropic', 'openai', 'xai', 'cursor', 'google']
const WINDOW_ORDER = ['session', 'week', 'extra', 'plan']

/**
 * Every machine's rows as GET /api/limits serves them: one row per id, the newest reading winning (an equal or
 * older one never replaces it), in the API's order (api/src/lib/limits.js readLimits).
 */
export function mergeLimitRows(lists: LimitRow[][]): LimitRow[] {
  const byId = new Map<string, LimitRow>()
  for (const row of lists.flat()) {
    const held = byId.get(row.id)
    if (!held?.observedAt || held.observedAt < row.observedAt) byId.set(row.id, row)
  }
  const rank = (list: string[], value: string) => {
    const i = list.indexOf(value)
    return i < 0 ? list.length : i
  }
  return [...byId.values()].sort((a, b) =>
    rank(PROVIDER_ORDER, a.provider) - rank(PROVIDER_ORDER, b.provider) ||
    String(a.name || '').localeCompare(String(b.name || '')) ||
    String(a.plan || '').localeCompare(String(b.plan || '')) ||
    rank(WINDOW_ORDER, a.window) - rank(WINDOW_ORDER, b.window) ||
    String(a.scope || '').localeCompare(String(b.scope || '')) ||
    String(a.resetsAt || '9999').localeCompare(String(b.resetsAt || '9999')))
}

// ---- Keeping the key ----
//
// Every Pages site of a GitHub user or organisation shares one origin (<user>.github.io), so any script on any of
// them can read this origin's storage. The key is therefore never kept as bytes: the unlock link's bytes become a
// non-extractable WebCrypto key at once and are wiped, and that CryptoKey (which IndexedDB stores as it is, still
// non-extractable) is what stays. Another page on the origin could still use it while this browser is unlocked,
// but cannot copy it out to decrypt later. The Agents page's choices (which accounts are tracked: their emails)
// are kept encrypted with it too, and Lock removes both.

/** Where the key stays between visits: IndexedDB in the browser (indexedDbVault), a Map in the tests. */
export interface KeyVault {
  load(name: string): Promise<CryptoKey | null>
  save(name: string, key: CryptoKey): Promise<void>
  remove(name: string): Promise<void>
}

/** The owner key as WebCrypto holds it: AES-256-GCM, non-extractable. Null unless the bytes are 32. */
export async function importOwnerKey(bytes: Uint8Array<ArrayBuffer>, subtle: SubtleCrypto = globalThis.crypto.subtle): Promise<CryptoKey | null> {
  if (bytes.length !== KEY_BYTES) return null
  return subtle.importKey('raw', bytes, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt'])
}

const isOwnerKey = (value: unknown): value is CryptoKey =>
  typeof CryptoKey !== 'undefined' && value instanceof CryptoKey && value.algorithm.name === 'AES-GCM' && !value.extractable

/** The browser's vault: one IndexedDB store of CryptoKeys by dashboard. */
export function indexedDbVault(idb: IDBFactory): KeyVault {
  let opened: Promise<IDBDatabase> | null = null
  const open = () => opened ??= new Promise<IDBDatabase>((resolve, reject) => {
    const request = idb.open('tokenmaxr-owner', 1)
    request.onupgradeneeded = () => { request.result.createObjectStore('keys') }
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error ?? new Error('IndexedDB refused'))
    request.onblocked = () => reject(new Error('IndexedDB blocked'))
  }).catch((err: unknown) => {
    opened = null
    throw err
  })
  const run = async <T>(mode: IDBTransactionMode, op: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> => {
    const db = await open()
    return new Promise<T>((resolve, reject) => {
      const tx = db.transaction('keys', mode)
      const request = op(tx.objectStore('keys'))
      tx.oncomplete = () => resolve(request.result)
      tx.onerror = tx.onabort = () => reject(tx.error ?? request.error ?? new Error('IndexedDB failed'))
    })
  }
  return {
    load: async name => {
      const value: unknown = await run('readonly', store => store.get(name))
      return isOwnerKey(value) ? value : null
    },
    save: async (name, key) => { await run('readwrite', store => store.put(key, name)) },
    remove: async name => { await run('readwrite', store => store.delete(name)) },
  }
}

/** A Map: the tests' vault, and the page's when IndexedDB is missing (the key then lasts for this tab). */
export function memoryVault(): KeyVault & { map: Map<string, CryptoKey> } {
  const map = new Map<string, CryptoKey>()
  return {
    map,
    load: async name => map.get(name) ?? null,
    save: async (name, key) => { map.set(name, key) },
    remove: async name => { map.delete(name) },
  }
}

const PREFS_CONTEXT = 'tokenmaxr dashboard prefs v1'
const prefsAad = (name: string) => new TextEncoder().encode(`${PREFS_CONTEXT}|${name}`)

/** The choices, sealed with the owner key for this dashboard only: {"v":1,"nonce":"…","data":"…"}. */
export async function sealPrefs(values: Record<string, string>, key: CryptoKey, name: string,
  subtle: SubtleCrypto = globalThis.crypto.subtle): Promise<string> {
  const nonce = globalThis.crypto.getRandomValues(new Uint8Array(NONCE_BYTES))
  const data = await subtle.encrypt({ name: 'AES-GCM', iv: nonce, additionalData: prefsAad(name) }, key,
    new TextEncoder().encode(JSON.stringify(values)))
  const b64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))
  return JSON.stringify({ v: 1, nonce: b64(nonce), data: b64(new Uint8Array(data)) })
}

/** The choices sealed by sealPrefs, or null when they do not open (another key, another dashboard, altered). */
export async function openPrefs(sealed: string, key: CryptoKey, name: string,
  subtle: SubtleCrypto = globalThis.crypto.subtle): Promise<Record<string, string> | null> {
  try {
    const box: unknown = JSON.parse(sealed)
    if (!isRecord(box) || box.v !== 1 || typeof box.nonce !== 'string' || typeof box.data !== 'string') return null
    const nonce = fromBase64(box.nonce)
    const data = fromBase64(box.data)
    if (!nonce || nonce.length !== NONCE_BYTES || !data) return null
    const plain = await subtle.decrypt({ name: 'AES-GCM', iv: nonce, additionalData: prefsAad(name) }, key, data)
    const values: unknown = JSON.parse(new TextDecoder().decode(plain))
    if (!isRecord(values)) return null
    return Object.fromEntries(Object.entries(values).filter((entry): entry is [string, string] => typeof entry[1] === 'string'))
  } catch {
    return null
  }
}

type KeyStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

/** One key per dashboard: every repository of a GitHub user shares the <user>.github.io origin. */
export function ownerStorageName(pathname: string): string {
  return `tokenmaxr.owner.v1:${pathname.replace(/[^/]*$/, '') || '/'}`
}

/** loading: the vault is being read (the first moments of a visit); then locked or unlocked. */
export interface OwnerState {
  status: 'loading' | 'locked' | 'unlocked'
  /** Changes on every unlock and lock, here or in another tab. */
  version: number
}

/** The Agents page's per-browser choices (site.ts SitePrefs), kept encrypted for this dashboard. */
export interface OwnerPrefs {
  get(name: string): string | null
  set(name: string, value: string): void
  /** Changes from elsewhere (another tab, an unlock or lock); never this page's own set. */
  subscribe(name: string, listener: (value: string | null) => void): () => void
}

export interface OwnerStore {
  /** The owner key, or null while locked (or still loading). */
  get(): CryptoKey | null
  /** The key once the vault has been read and any unlock under way has finished. */
  ready(): Promise<CryptoKey | null>
  /** An immutable snapshot (for useSyncExternalStore). */
  state(): OwnerState
  subscribe(listener: () => void): () => void
  /** Keeps the key and wipes the bytes. */
  unlock(bytes: Uint8Array<ArrayBuffer>): Promise<void>
  /** Forgets the key and the choices kept with it, here and in this dashboard's other tabs. */
  lock(): void
  prefs: OwnerPrefs
}

/** What one tab tells this dashboard's other tabs. */
interface Channel {
  postMessage(message: unknown): void
  addEventListener(type: 'message', listener: (event: MessageEvent) => void): void
}

export interface OwnerStoreOptions {
  /** The dashboard's name (ownerStorageName). */
  name: string
  vault: KeyVault
  /** Local storage, for the sealed choices; null when the browser refuses it (the choices then last for this tab). */
  storage?: KeyStorage | null
  /** This dashboard's other tabs (a BroadcastChannel named after the dashboard). */
  channel?: Channel | null
  /** The window: other tabs' changes to the sealed choices. */
  events?: { addEventListener(type: 'storage', listener: (event: StorageEvent) => void): void }
  subtle?: SubtleCrypto
}

/**
 * The owner key for one dashboard. A vault that refuses (no IndexedDB) keeps the key for this tab only. The first
 * build of this page kept the key's text in local storage under the dashboard's name: that entry is removed.
 */
export function createOwnerStore({ name, vault, storage = null, channel = null, events, subtle = globalThis.crypto.subtle }: OwnerStoreOptions): OwnerStore {
  const prefsName = `${name}:prefs`
  let key: CryptoKey | null = null
  let state: OwnerState = { status: 'loading', version: 0 }
  // Bumped by every unlock and lock: a slower read that started before one never overrides it.
  let generation = 0
  let tasks: Promise<void> = Promise.resolve()
  const enqueue = (task: () => Promise<void>) => (tasks = tasks.then(task).catch(() => {}))
  const listeners = new Set<() => void>()
  const publish = (next: CryptoKey | null) => {
    key = next
    state = { status: next ? 'unlocked' : 'locked', version: state.version + 1 }
    listeners.forEach(listener => listener())
  }

  let prefs = new Map<string, string>()
  const prefListeners = new Map<string, Set<(value: string | null) => void>>()
  const replacePrefs = (next: Map<string, string>) => {
    const previous = prefs
    prefs = next
    for (const [prefName, set] of prefListeners) {
      if (previous.get(prefName) !== next.get(prefName)) set.forEach(listener => listener(next.get(prefName) ?? null))
    }
  }
  const readPrefs = async (owner: CryptoKey | null) => {
    let sealed: string | null = null
    try {
      sealed = owner ? storage?.getItem(prefsName) ?? null : null
    } catch {
      // Refused: none kept.
    }
    const values = owner && sealed ? await openPrefs(sealed, owner, name, subtle) : null
    return new Map(Object.entries(values ?? {}))
  }
  let saving: Promise<void> = Promise.resolve()
  const savePrefs = () => {
    saving = saving.then(async () => {
      const owner = key
      if (!owner || !storage) return
      const sealed = await sealPrefs(Object.fromEntries(prefs), owner, name, subtle)
      if (key === owner) storage.setItem(prefsName, sealed)
    }).catch(() => { /* Unsaved: the choices last for this tab. */ })
  }

  /** Reads the vault; `onlyUnlock` (another tab unlocked) never locks this one. */
  const load = (onlyUnlock = false) => {
    const started = generation
    enqueue(async () => {
      let next: CryptoKey | null = key
      try {
        next = await vault.load(name)
      } catch {
        // The vault refused: keep what this tab has.
      }
      if (onlyUnlock && !next) return
      const values = await readPrefs(next)
      if (started !== generation) return
      publish(next)
      replacePrefs(values)
    })
  }

  const forget = (tell: boolean) => {
    generation++
    try {
      storage?.removeItem(prefsName)
    } catch {
      // Forgotten in this tab regardless.
    }
    publish(null)
    replacePrefs(new Map())
    enqueue(() => vault.remove(name).catch(() => {}))
    if (tell) channel?.postMessage('lock')
  }

  try {
    storage?.removeItem(name)
  } catch {
    // Nothing to clear.
  }
  load()
  channel?.addEventListener('message', event => {
    if (event.data === 'lock') forget(false)
    else if (event.data === 'unlock') load(true)
  })
  events?.addEventListener('storage', event => {
    if (event.key !== prefsName && event.key !== null) return
    const started = generation
    void readPrefs(key).then(values => { if (started === generation) replacePrefs(values) })
  })

  return {
    get: () => key,
    ready: () => tasks.then(() => key),
    state: () => state,
    subscribe(listener) {
      listeners.add(listener)
      return () => { listeners.delete(listener) }
    },
    unlock(bytes) {
      generation++
      const started = generation
      return enqueue(async () => {
        let next: CryptoKey | null
        try {
          next = await importOwnerKey(bytes, subtle)
        } finally {
          bytes.fill(0)
        }
        if (!next) return
        await vault.save(name, next).catch(() => { /* This tab only. */ })
        const values = await readPrefs(next)
        if (started !== generation) return
        publish(next)
        replacePrefs(values)
        channel?.postMessage('unlock')
      })
    },
    lock: () => forget(true),
    prefs: {
      get: prefName => prefs.get(prefName) ?? null,
      set(prefName, value) {
        if (prefs.get(prefName) === value) return
        prefs = new Map(prefs).set(prefName, value)
        // Locked (test data only): kept for this tab, never stored.
        if (key) savePrefs()
      },
      subscribe(prefName, listener) {
        const set = prefListeners.get(prefName) ?? new Set()
        prefListeners.set(prefName, set.add(listener))
        return () => { set.delete(listener) }
      },
    },
  }
}

// ---- The collector's handoff, and a copied link ----
//
// Two ways in besides the GitHub sign-in: the handoff from tokenmaxr's settings page, or a copied link. Both land on
// the Agents page (#/agents): the key exists to show the owner's accounts, and that is where they are (without a
// key, its gate offers the GitHub sign-in).

export const UNLOCK_LANDING = '#/agents'

const UNLOCK_RE = /(?:^#|[#?&/])unlock=([^&]*)/

/**
 * The copied link, <dashboard>#unlock=<key>, for a browser tokenmaxr's settings cannot open (a phone, another
 * computer): takes the key out of the address bar before anything else reads it, and keeps it when it is one.
 */
export function consumeUnlockFragment(location: { pathname: string; search: string; hash: string },
  history: Pick<History, 'replaceState'>, store: OwnerStore): 'unlocked' | 'invalid' | 'none' {
  const match = UNLOCK_RE.exec(location.hash)
  if (!match) return 'none'
  history.replaceState(null, '', `${location.pathname}${location.search}${UNLOCK_LANDING}`)
  let text = match[1]
  try {
    text = decodeURIComponent(text)
  } catch {
    text = ''
  }
  const key = parseOwnerKey(text)
  if (!key) return 'invalid'
  void store.unlock(key)
  return 'unlocked'
}

// The handoff: tokenmaxr's settings page (http://127.0.0.1:<port>, its own window) opens <dashboard>#unlock, which
// carries no key, and waits. The page says it is ready ({type: UNLOCK_READY}, to any origin: it carries nothing)
// and takes one {type: UNLOCK_KEY, key: <base64url>} from the window that opened it, whatever that window's origin.
// So the key is never in a URL, and never in the browser's history. Any page can open the dashboard and hand it a
// key, as any page can link to #unlock=<key>; that only replaces the key this browser holds with one that opens
// nothing (the page then locks itself, as for a stale key). The message is only ever a key to keep: nothing in it
// is decrypted, fetched or shown, and nothing goes back to the opener but the ready message.

export const UNLOCK_READY = 'tokenmaxr-unlock-ready'
export const UNLOCK_KEY = 'tokenmaxr-unlock'
export const HANDOFF_TIMEOUT_MS = 10_000

const HANDOFF_RE = /^#\/?unlock$/

/** idle: no handoff; waiting: asked the opener; done: the key arrived; failed: no opener, or no key in time. */
export type HandoffStatus = 'idle' | 'waiting' | 'done' | 'failed'

/** The window, as the handoff uses it (fakes in owner.test.ts). */
export interface HandoffWindow {
  readonly opener: { postMessage(message: unknown, targetOrigin: string): void } | null
  addEventListener(type: 'message', listener: (event: MessageEvent) => void): void
  removeEventListener(type: 'message', listener: (event: MessageEvent) => void): void
}

export interface UnlockHandoff {
  /** An immutable snapshot (for useSyncExternalStore). */
  status(): HandoffStatus
  subscribe(listener: () => void): () => void
  /** Takes a bare #unlock out of the address bar and asks the opener for the key; false for any other address. */
  consume(location: { pathname: string; search: string; hash: string }, history: Pick<History, 'replaceState'>): boolean
}

export interface UnlockHandoffOptions {
  window: HandoffWindow
  store: Pick<OwnerStore, 'unlock'>
  timeoutMs?: number
}

export function createUnlockHandoff({ window: win, store, timeoutMs = HANDOFF_TIMEOUT_MS }: UnlockHandoffOptions): UnlockHandoff {
  let status: HandoffStatus = 'idle'
  const listeners = new Set<() => void>()
  const set = (next: HandoffStatus) => {
    status = next
    listeners.forEach(listener => listener())
  }
  // Ends the handoff under way: no more messages, no timeout.
  let stop: (() => void) | null = null

  const start = () => {
    const opener = win.opener
    if (!opener) {
      set('failed')
      return
    }
    const onMessage = (event: MessageEvent) => {
      if (event.source !== opener) return
      const data: unknown = event.data
      if (!isRecord(data) || data.type !== UNLOCK_KEY) return
      const key = parseOwnerKey(typeof data.key === 'string' ? data.key : null)
      // Not a key: ignored (the opener may still send one).
      if (!key) return
      stop?.()
      set('done')
      void store.unlock(key)
    }
    const timer = setTimeout(() => {
      stop?.()
      set('failed')
    }, timeoutMs)
    stop = () => {
      stop = null
      clearTimeout(timer)
      win.removeEventListener('message', onMessage)
    }
    win.addEventListener('message', onMessage)
    set('waiting')
    try {
      opener.postMessage({ type: UNLOCK_READY }, '*')
    } catch {
      stop()
      set('failed')
    }
  }

  return {
    status: () => status,
    subscribe(listener) {
      listeners.add(listener)
      return () => { listeners.delete(listener) }
    },
    consume(location, history) {
      if (!HANDOFF_RE.test(location.hash)) return false
      history.replaceState(null, '', `${location.pathname}${location.search}${UNLOCK_LANDING}`)
      // One at a time: a second #unlock while waiting keeps the first.
      if (!stop) start()
      return true
    },
  }
}
