// Sign in with GitHub on the Pages dashboard, on any device: GitHub's device flow for the tokenmaxor App (the
// collector's), through a small relay (d0m1.com's /api/github/device/*, api/src/lib/github-device.js), because
// GitHub's device-flow endpoints send no CORS headers. The relay pins the App's client id; no secret exists.
//
// 1. The relay asks GitHub for a device code; the page shows the user code, and the owner enters it at
//    https://github.com/login/device (any device: a phone signs in on its own browser).
// 2. The page polls the relay for the token, at GitHub's interval (slower after slow_down), until GitHub answers
//    with a token, or the code expires, or the owner cancels on GitHub.
// 3. With the token (only here, in memory, for these few calls), the page reads the repository's Actions variable
//    TOKENMAXR_FLEET_KEY from api.github.com (which allows CORS), derives the owner key exactly as the collector
//    does (HMAC-SHA256(fleet key, "tokenmaxr dashboard owner v1"): collector/internal/ghpub OwnerKey; the variable
//    is the 32-byte fleet key in standard base64), checks that it opens the published owner.json files, and keeps it
//    as owner.ts keeps every key: non-extractable, in IndexedDB. The token and the fleet key are then dropped.
//
// Pure apart from the fetch, clock and timers passed in, so signin.test.ts runs it in Node.
import { OWNER_CONTEXT } from './owner.ts'

export const GITHUB_API = 'https://api.github.com'
export const GITHUB_DEVICE_PAGE = 'https://github.com/login/device'
export const FLEET_VARIABLE = 'TOKENMAXR_FLEET_KEY'
export const APP_INSTALL_URL = 'https://github.com/apps/tokenmaxor/installations/new'
const FLEET_KEY_BYTES = 32
/** Polls that fail on the network in a row before the page gives up (a phone between networks). */
const NETWORK_RETRIES = 4

type Fetch = (input: string, init?: RequestInit) => Promise<Response>

export type SignInErrorKind =
  | 'expired' | 'denied' | 'cant-read' | 'not-installed' | 'no-key' | 'wrong-key' | 'no-repo' | 'network' | 'rate-limited' | 'github' | 'failed'

export class SignInError extends Error {
  readonly kind: SignInErrorKind
  readonly repo: string | null
  constructor(kind: SignInErrorKind, repo: string | null = null, detail = '') {
    super(detail || kind)
    this.name = 'SignInError'
    this.kind = kind
    this.repo = repo
  }
}

/** What the owner reads for each failure. */
export function signInMessage(error: SignInError): string {
  const repo = error.repo ?? 'this repository'
  switch (error.kind) {
    case 'expired': return 'The code expired before GitHub got it. Get a new code to try again.'
    case 'denied': return 'Sign-in was cancelled on GitHub.'
    case 'cant-read': return `This GitHub account can't read ${repo}.`
    case 'not-installed': return `tokenmaxor isn't installed with access to ${repo}.`
    case 'no-key': return `${repo} has no fleet key yet. Publish from a tokenmaxr collector first.`
    case 'wrong-key': return `The key in ${repo} doesn't open this dashboard's owner files.`
    case 'no-repo': return 'No repository of this GitHub account holds this dashboard\'s key. Set "repository" in its tokenmaxr.json.'
    case 'network': return 'Couldn\'t reach GitHub. Check the connection and try again.'
    case 'rate-limited': return 'Too many sign-in attempts from this network. Try again in a few minutes.'
    case 'github': return error.message && error.message !== 'github' ? `GitHub refused the sign-in: ${error.message}` : 'GitHub refused the sign-in.'
    case 'failed': return error.message && error.message !== 'failed' ? `Sign-in failed: ${error.message}` : 'Sign-in failed.'
  }
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

const REPO_RE = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})\/[A-Za-z0-9._-]{1,100}$/

/** "owner/name", or null. */
export function parseRepository(value: unknown): string | null {
  return typeof value === 'string' && REPO_RE.test(value.trim()) && !value.trim().endsWith('/..') ? value.trim() : null
}

/**
 * The repository a Pages site is served from: https://<user>.github.io/<repository>/ (or the user site, the
 * repository <user>.github.io, at the root). Null on a custom domain (tokenmaxr.json "repository" names it).
 */
export function pagesRepository(hostname: string, pathname: string): string | null {
  const host = hostname.toLowerCase()
  if (!host.endsWith('.github.io')) return null
  const user = host.slice(0, -'.github.io'.length)
  if (!user || user.includes('.')) return null
  let first = pathname.split('/').find(Boolean) ?? ''
  try {
    first = decodeURIComponent(first)
  } catch {
    return null
  }
  // A path ending in a file (index.html) at the root is the user site.
  return parseRepository(`${user}/${first && !first.includes('.') ? first : host}`)
}

/**
 * The dashboard's settings in tokenmaxr.json (served beside the page): the repository. ("background", which once
 * turned on d0m1.com's animated background, is ignored: the dashboard looks like a page of GitHub's, plain.)
 */
export interface DashboardConfig {
  /** "owner/name": needed on a custom domain, where the address does not say. */
  repository: string | null
}

export function parseDashboardConfig(value: unknown): DashboardConfig {
  const config = isRecord(value) ? value : {}
  return { repository: parseRepository(config.repository) }
}

// ---- The device flow, through the relay ----

export interface DeviceCode {
  deviceCode: string
  userCode: string
  verificationUri: string
  /** Epoch ms. */
  expiresAt: number
  /** Seconds between polls. */
  interval: number
}

/** POSTs to the relay as a simple request (text/plain): no CORS preflight. */
async function relayPost(fetch: Fetch, url: string, body: unknown, signal?: AbortSignal): Promise<Record<string, unknown>> {
  let response: Response
  try {
    response = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'text/plain;charset=UTF-8', Accept: 'application/json' },
      body: JSON.stringify(body ?? {}),
      credentials: 'omit',
      cache: 'no-store',
      signal,
    })
  } catch (err) {
    if (signal?.aborted) throw err
    throw new SignInError('network')
  }
  if (response.status === 429) throw new SignInError('rate-limited')
  let json: unknown
  try {
    json = await response.json()
  } catch {
    throw new SignInError(response.status >= 500 ? 'network' : 'github', null, `HTTP ${response.status}`)
  }
  if (!isRecord(json)) throw new SignInError('github', null, `HTTP ${response.status}`)
  if (response.status >= 500 && (json.error === 'github_unreachable' || json.error === 'github_bad_answer')) throw new SignInError('network')
  return json
}

export async function requestDeviceCode(relay: string, fetch: Fetch, now: () => number, signal?: AbortSignal): Promise<DeviceCode> {
  const body = await relayPost(fetch, `${relay}/code`, {}, signal)
  const { device_code, user_code, verification_uri, expires_in, interval } = body
  if (typeof device_code !== 'string' || typeof user_code !== 'string') {
    throw new SignInError('github', null, typeof body.error_description === 'string' ? body.error_description : typeof body.error === 'string' ? body.error : '')
  }
  return {
    deviceCode: device_code,
    userCode: user_code,
    // Only GitHub's own page: whatever else an answer says, the owner is sent to github.com.
    verificationUri: typeof verification_uri === 'string' && verification_uri.startsWith('https://github.com/') ? verification_uri : GITHUB_DEVICE_PAGE,
    expiresAt: now() + (typeof expires_in === 'number' && expires_in > 0 ? expires_in : 900) * 1000,
    interval: typeof interval === 'number' && interval > 0 ? interval : 5,
  }
}

export interface PollOptions {
  fetch: Fetch
  now: () => number
  /** Resolves after ms, or rejects once the signal aborts. */
  sleep: (ms: number, signal?: AbortSignal) => Promise<void>
  signal?: AbortSignal
}

/** Polls until GitHub hands over the user's token. Throws SignInError expired, denied, network or github. */
export async function pollDeviceToken(relay: string, code: DeviceCode, { fetch, now, sleep, signal }: PollOptions): Promise<string> {
  let interval = code.interval
  let failures = 0
  for (;;) {
    await sleep(interval * 1000, signal)
    if (now() >= code.expiresAt) throw new SignInError('expired')
    let body: Record<string, unknown>
    try {
      body = await relayPost(fetch, `${relay}/token`, { device_code: code.deviceCode }, signal)
    } catch (err) {
      if (err instanceof SignInError && err.kind === 'network' && ++failures < NETWORK_RETRIES) continue
      if (err instanceof SignInError && err.kind === 'rate-limited' && ++failures < NETWORK_RETRIES) {
        interval += 5
        continue
      }
      throw err
    }
    failures = 0
    if (typeof body.access_token === 'string' && body.access_token) return body.access_token
    switch (body.error) {
      case 'authorization_pending': continue
      case 'slow_down':
        interval = typeof body.interval === 'number' && body.interval > interval ? body.interval : interval + 5
        continue
      case 'expired_token': throw new SignInError('expired')
      case 'access_denied': throw new SignInError('denied')
      default:
        throw new SignInError('github', null, typeof body.error_description === 'string' ? body.error_description : typeof body.error === 'string' ? body.error : '')
    }
  }
}

// ---- The fleet key, read with the user's token ----

/** The fleet key as the collector stores it (Go base64.StdEncoding of 32 bytes, surrounding space trimmed), or null. */
export function parseFleetKey(text: unknown): Uint8Array<ArrayBuffer> | null {
  if (typeof text !== 'string') return null
  const plain = text.trim()
  if (plain.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(plain)) return null
  let bytes: Uint8Array<ArrayBuffer>
  try {
    bytes = Uint8Array.from(atob(plain), c => c.charCodeAt(0))
  } catch {
    return null
  }
  return bytes.length === FLEET_KEY_BYTES ? bytes : null
}

/** The owner key's 32 bytes: HMAC-SHA256(key: fleet key, message: "tokenmaxr dashboard owner v1"). */
export async function deriveOwnerKey(fleet: Uint8Array<ArrayBuffer>, subtle: SubtleCrypto = globalThis.crypto.subtle): Promise<Uint8Array<ArrayBuffer>> {
  const hmac = await subtle.importKey('raw', fleet, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
  return new Uint8Array(await subtle.sign('HMAC', hmac, new TextEncoder().encode(OWNER_CONTEXT)))
}

/** api.github.com with the user's token. Network failures are SignInError network. */
function github(fetch: Fetch, token: string, signal?: AbortSignal) {
  return async (path: string): Promise<{ status: number; body: unknown }> => {
    let response: Response
    try {
      response = await fetch(GITHUB_API + path, {
        headers: { Accept: 'application/vnd.github+json', Authorization: `Bearer ${token}`, 'X-GitHub-Api-Version': '2022-11-28' },
        credentials: 'omit',
        cache: 'no-store',
        signal,
      })
    } catch (err) {
      if (signal?.aborted) throw err
      throw new SignInError('network')
    }
    let body: unknown = null
    try {
      body = await response.json()
    } catch {
      // No JSON: the status says enough.
    }
    return { status: response.status, body }
  }
}

const sameName = (a: unknown, b: string) => typeof a === 'string' && a.toLowerCase() === b.toLowerCase()

/**
 * Why the variable could not be read: the App is not installed on the repository (but this account could
 * otherwise reach it), this account cannot read it, or the repository has no fleet key yet.
 */
async function diagnose(get: ReturnType<typeof github>, repo: string, status: number): Promise<SignInError> {
  const [owner] = repo.split('/')
  let installed: boolean | null = null
  try {
    const list = await get('/user/installations?per_page=100')
    const installations = isRecord(list.body) && Array.isArray(list.body.installations) ? list.body.installations.filter(isRecord) : []
    const install = installations.find(i => isRecord(i.account) && sameName(i.account.login, owner))
    if (list.status === 200) installed = false
    if (install && install.repository_selection === 'all') installed = true
    else if (install && typeof install.id === 'number') {
      const repos = await get(`/user/installations/${install.id}/repositories?per_page=100`)
      const names = isRecord(repos.body) && Array.isArray(repos.body.repositories) ? repos.body.repositories.filter(isRecord).map(r => r.full_name) : []
      installed = names.some(name => sameName(name, repo))
    }
  } catch (err) {
    if (!(err instanceof SignInError)) throw err
  }
  let canWrite = false
  try {
    const info = await get(`/repos/${repo}`)
    const permissions = isRecord(info.body) && isRecord(info.body.permissions) ? info.body.permissions : {}
    canWrite = permissions.admin === true || permissions.maintain === true || permissions.push === true
  } catch (err) {
    if (!(err instanceof SignInError)) throw err
  }
  if (installed === true) return new SignInError(status === 404 && canWrite ? 'no-key' : 'cant-read', repo)
  return new SignInError(canWrite ? 'not-installed' : 'cant-read', repo)
}

/** The fleet key in repo's Actions variable. Throws SignInError cant-read, not-installed, no-key or network. */
export async function readFleetKey(token: string, repo: string, fetch: Fetch, signal?: AbortSignal): Promise<Uint8Array<ArrayBuffer>> {
  const get = github(fetch, token, signal)
  const variable = await get(`/repos/${repo}/actions/variables/${FLEET_VARIABLE}`)
  if (variable.status === 200) {
    const key = parseFleetKey(isRecord(variable.body) ? variable.body.value : null)
    if (!key) throw new SignInError('no-key', repo)
    return key
  }
  if (variable.status === 401) throw new SignInError('github', repo, 'the token was not accepted')
  if (variable.status === 403 || variable.status === 404) throw await diagnose(get, repo, variable.status)
  throw new SignInError(variable.status >= 500 ? 'network' : 'github', repo, `HTTP ${variable.status}`)
}

/** On a custom domain with no "repository": the repositories the App can reach for this account, likeliest first. */
export async function installedRepositories(token: string, fetch: Fetch, signal?: AbortSignal, max = 30): Promise<string[]> {
  const get = github(fetch, token, signal)
  const list = await get('/user/installations?per_page=100')
  const installations = isRecord(list.body) && Array.isArray(list.body.installations) ? list.body.installations.filter(isRecord) : []
  const names: string[] = []
  for (const install of installations) {
    if (typeof install.id !== 'number') continue
    const repos = await get(`/user/installations/${install.id}/repositories?per_page=100`)
    const list = isRecord(repos.body) && Array.isArray(repos.body.repositories) ? repos.body.repositories.filter(isRecord) : []
    for (const repo of list) {
      const name = parseRepository(repo.full_name)
      if (name) names.push(name)
    }
  }
  // The collector's default name first.
  return names.sort((a, b) => Number(b.endsWith('/tokenmaxr-usage')) - Number(a.endsWith('/tokenmaxr-usage'))).slice(0, max)
}

// ---- The whole sign-in, as the panel drives it ----

export type SignInState =
  | { step: 'idle' }
  | { step: 'requesting' }
  /** opened: the owner has gone to GitHub (Open GitHub), so the panel says it is waiting. */
  | { step: 'code'; code: DeviceCode; opened: boolean }
  | { step: 'checking' }
  | { step: 'done' }
  | { step: 'error'; error: SignInError }

/** What the published owner files say of a key: opens them, none to check, or opens none. */
export type KeyCheck = 'ok' | 'none' | 'wrong'

export interface DeviceSignInOptions {
  /** The relay's base, e.g. https://d0m1.com/api/github/device. */
  relay: string
  /** The dashboard's repository ("owner/name"), or null when only discovery can tell (a custom domain). */
  repository: () => Promise<string | null>
  /** Tries a key on the published owner.json files. */
  check: (key: CryptoKey, signal: AbortSignal) => Promise<KeyCheck>
  /** Keeps the owner key (owner.ts store.unlock: non-extractable, wipes the bytes). */
  accept: (ownerKey: Uint8Array<ArrayBuffer>) => Promise<void>
  fetch?: Fetch
  now?: () => number
  sleep?: PollOptions['sleep']
  subtle?: SubtleCrypto
}

export interface DeviceSignIn {
  state(): SignInState
  subscribe(listener: () => void): () => void
  /** Starts (or restarts) the sign-in: a new code. */
  start(): void
  /** The owner went to GitHub. */
  opened(): void
  /** Stops polling (the panel went away); back to idle. */
  cancel(): void
}

export function abortableSleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(signal.reason)
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      reject(signal?.reason)
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

export function createDeviceSignIn({
  relay, repository, check, accept, fetch = (...args) => globalThis.fetch(...args), now = () => Date.now(), sleep = abortableSleep,
  subtle = globalThis.crypto.subtle,
}: DeviceSignInOptions): DeviceSignIn {
  let state: SignInState = { step: 'idle' }
  let controller: AbortController | null = null
  const listeners = new Set<() => void>()
  const set = (next: SignInState) => {
    state = next
    listeners.forEach(listener => listener())
  }

  /** The owner key of repo, checked against the owner files, as an importable 32 bytes. */
  const keyOf = async (token: string, repo: string, signal: AbortSignal): Promise<{ bytes: Uint8Array<ArrayBuffer>; check: KeyCheck }> => {
    const fleet = await readFleetKey(token, repo, fetch, signal)
    let bytes: Uint8Array<ArrayBuffer>
    try {
      bytes = await deriveOwnerKey(fleet, subtle)
    } finally {
      fleet.fill(0)
    }
    const key = await subtle.importKey('raw', bytes, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt'])
    return { bytes, check: await check(key, signal) }
  }

  const run = async (signal: AbortSignal) => {
    set({ step: 'requesting' })
    const code = await requestDeviceCode(relay, fetch, now, signal)
    set({ step: 'code', code, opened: false })
    // The token lives in this function only, for the few calls below.
    const token = await pollDeviceToken(relay, code, { fetch, now, sleep, signal })
    set({ step: 'checking' })
    const repo = await repository()
    let found: Uint8Array<ArrayBuffer> | null = null
    if (repo) {
      const { bytes, check: result } = await keyOf(token, repo, signal)
      if (result === 'wrong') {
        bytes.fill(0)
        throw new SignInError('wrong-key', repo)
      }
      found = bytes
    } else {
      // A custom domain that names no repository: the first of this account's repositories whose key opens the
      // owner files (or, with none published yet, the first that has a key).
      let fallback: Uint8Array<ArrayBuffer> | null = null
      for (const candidate of await installedRepositories(token, fetch, signal)) {
        let result: { bytes: Uint8Array<ArrayBuffer>; check: KeyCheck }
        try {
          result = await keyOf(token, candidate, signal)
        } catch (err) {
          if (err instanceof SignInError && err.kind !== 'network') continue
          throw err
        }
        if (result.check === 'ok') {
          found = result.bytes
          break
        }
        if (result.check === 'none' && !fallback) fallback = result.bytes
        else result.bytes.fill(0)
      }
      if (found) fallback?.fill(0)
      found ??= fallback
      if (!found) throw new SignInError('no-repo')
    }
    if (signal.aborted) {
      found.fill(0)
      return
    }
    await accept(found)
    set({ step: 'done' })
  }

  return {
    state: () => state,
    subscribe(listener) {
      listeners.add(listener)
      return () => { listeners.delete(listener) }
    },
    start() {
      controller?.abort()
      const mine = new AbortController()
      controller = mine
      run(mine.signal).catch((err: unknown) => {
        if (mine.signal.aborted) return
        set({ step: 'error', error: err instanceof SignInError ? err : new SignInError('failed', null, err instanceof Error ? err.message : '') })
      }).finally(() => {
        if (controller === mine) controller = null
      })
    },
    opened() {
      if (state.step === 'code' && !state.opened) set({ ...state, opened: true })
    },
    cancel() {
      controller?.abort()
      controller = null
      if (state.step !== 'done') set({ step: 'idle' })
    },
  }
}
