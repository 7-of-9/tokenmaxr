// The GitHub Pages data source: reads the static files collectors publish to the user's repository
// (data/index.json lists each machine's files) and builds the same UsageResponse d0m1.com's API serves, so the
// shared /tokens page (TokensView) shows the same nuance.
//
// Where it reads them: a public repository's files straight from the repository (raw.githubusercontent.com, its
// newest commit, cached there for up to 5 minutes), so collectors' commits show without a Pages deploy; the
// index there is the one collectors commit with their data, joined with the deployed copy's index (a machine
// whose collector commits no index yet adds files the repository's index does not list until another machine
// commits; the deployed index lists them after the next hourly deploy, and they are then read live). A private repository (raw.githubusercontent.com
// answers 404 to a page), a repository whose collectors do not commit an index yet, and any failure before the
// first live read of a visit read the deployed copy beside the page instead (its index written by
// scripts/build-index.mjs), which the Pages workflow refreshes hourly. Once a live read has worked, a failed one
// (rate-limited, offline) fails the poll, so the page keeps its newer data and tries again, rather than step
// back to the older deployed copy. Both indexes give each file's git object id, so a file is read again only when it
// changed, and a copy is kept only when its bytes have that id (raw.githubusercontent.com caches each file on its
// own: a fresh index can list a change whose older copy is still served, which is then read again next time).
//
// Every machine publishes its own daily rows; the fleet view sums them here exactly as the server's rollup
// (api/src/lib/rollup.js) sums events: tokens split into exact and estimated buckets by event quality, prompts
// and prompts without token records per provider, tokens per model, machine and country. Rows are read by
// their "cols" names, never by position, so schema 1 files (no quality, cache-1h, reasoning or prompt
// columns) keep working. Account hashes never leave this module: they become per-page aliases (acct1..N).
//
// Account history (account-usage.json: provider account totals per UTC day, and each machine's local tokens per
// UTC day) is reconciled exactly as the API reconciles it (accountUsage.ts, a port of api/src/lib/account-usage.js).
//
// The owner's plan limits (the Agents page) come from each machine's owner.json, encrypted for the owner and
// decrypted in the owner's browser (owner.ts); a page that is not signed in reads none of them.
import {
  ACCOUNT_LEDGER_VERSION, datesBetween, reconcileAccountUsage, surroundingDates,
  type AccountSnapshot, type LedgerDay, type LedgerEntry,
} from './accountUsage.ts'
import { LimitsDenied, type LimitRow } from '../../src/components/agents/limits.ts'
import type { UsageSource } from '../../src/components/agents/source.ts'
import type { ModelBucket, PlaceBucket, Provider, ProviderDay, PublicMachine, RollupDay, TokenBucket, UsageResponse, WorkspaceDay } from '../../src/components/agents/types.ts'
import type { UsageTransfer } from '../../src/components/agents/usageResponse.ts'
import { decryptOwnerFile, mergeLimitRows, OWNER_FILE } from './owner.ts'

const PROVIDERS: Provider[] = ['anthropic', 'openai', 'xai', 'cursor', 'google']
/** Column order of schema 1 files, which some early collectors wrote without "cols". */
export const SCHEMA1_COLS = ['date', 'provider', 'source', 'model', 'acct', 'in', 'cacheW', 'cacheR', 'out', 'events', 'prompts']
/**
 * A machine whose newest event is this recent is live: collectors publish hourly by default, and
 * raw.githubusercontent.com keeps a copy for up to 5 minutes.
 */
export const LIVE_MS = 75 * 60 * 1000
/**
 * The same read from the deployed copy, which lags the repository by up to another hour (the hourly deploy at
 * 17 minutes past, plus its queue): two publish intervals and the deploy's delay.
 */
export const DEPLOYED_LIVE_MS = 135 * 60 * 1000
const UNKNOWN_COUNTRY = 'ZZ'
const UNKNOWN_MODEL = 'unknown'
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/
const USAGE_FILE_RE = /^usage-\d{4}-\d{2}\.json$/

// ---- What the repository holds ----

export interface IndexFile {
  schema?: number
  title?: string
  generatedAt?: string
  /** Schema 4: each listed file's git object id (blobs), by file name. */
  machines?: Array<{ id: string; files?: string[]; blobs?: Record<string, string> }>
}

export interface MetaFile {
  id?: string
  label?: string
  os?: string
  collector?: string
  updatedAt?: string
  /** Schema 2, opt-in: the machine's country (ISO 3166-1 alpha-2). */
  cc?: string
  /** Schema 2: the machine-local date of its first data. */
  firstSeenAt?: string
  /** Schema 2: the newest usage or activity event, RFC 3339 UTC to the minute. */
  lastEventAt?: string
}

export interface UsageFile {
  schema?: number
  month?: string
  cols?: string[]
  rows?: unknown[][]
}

/** account-usage.json: provider account totals and the local ledger they reconcile against (UTC days). */
export interface AccountUsageFile {
  schema?: number
  snapshots?: Array<{ provider: string; source: string; acct: string; date: string; totalTokens: number; observedAt: string }>
  /** The ledger's column names (schema 2: date, provider, source, acct, tokens); rows are read by name. */
  ledgerCols?: string[]
  ledger?: unknown[][]
  /** Local dates whose usage rows hold tokens the ledger does not (an older collector's totals): never covered. */
  unledgered?: string[]
}

/** One machine's published files, parsed. */
export interface MachineFiles {
  id: string
  meta: MetaFile | null
  usage: UsageFile[]
  accountUsage?: AccountUsageFile | null
}

export interface Published {
  title?: string
  generatedAt?: string
  /** False: read from the deployed copy, so a machine counts as live over DEPLOYED_LIVE_MS (else LIVE_MS). */
  live?: boolean
  machines: MachineFiles[]
}

/** One usage row, read by column name. Null: the file has no such column (an older schema). */
export interface UsageRow {
  machine: string
  date: string
  provider: Provider
  source: string
  model: string
  acct: string
  estimated: boolean
  in: number
  cacheW: number
  cacheW1h: number
  cacheR: number
  out: number
  reasoning: number
  events: number
  prompts: number
  promptsNoUsage: number | null
  modelPrompts: number | null
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)
const count = (value: unknown) => typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0
const text = (value: unknown) => typeof value === 'string' ? value : ''
const isProvider = (value: string): value is Provider => (PROVIDERS as string[]).includes(value)
const country = (value: unknown) => typeof value === 'string' && /^[A-Z]{2}$/.test(value) && value !== UNKNOWN_COUNTRY ? value : ''
const validTime = (value: unknown) => typeof value === 'string' && Number.isFinite(Date.parse(value)) ? value : null

/** A file's rows by column name; rows with an unknown provider or a malformed date are skipped. */
export function readUsageRows(machine: string, file: UsageFile): UsageRow[] {
  const cols = Array.isArray(file.cols) && file.cols.every(c => typeof c === 'string') ? file.cols : SCHEMA1_COLS
  const at = new Map(cols.map((name, i) => [name, i]))
  const has = (name: string) => at.has(name)
  const out: UsageRow[] = []
  for (const raw of Array.isArray(file.rows) ? file.rows : []) {
    if (!Array.isArray(raw)) continue
    const get = (name: string) => raw[at.get(name) ?? -1]
    const date = text(get('date'))
    const provider = text(get('provider'))
    if (!DATE_RE.test(date) || !isProvider(provider)) continue
    out.push({
      machine, date, provider,
      source: text(get('source')),
      model: text(get('model')),
      acct: text(get('acct')),
      // The server's rollup: only estimated events are estimated; anything else counts as exact.
      estimated: get('q') === 'e',
      in: count(get('in')),
      cacheW: count(get('cacheW')),
      cacheW1h: count(get('cacheW1h')),
      cacheR: count(get('cacheR')),
      out: count(get('out')),
      reasoning: count(get('reasoning')),
      events: count(get('events')),
      prompts: count(get('prompts')),
      promptsNoUsage: has('promptsNoUsage') ? count(get('promptsNoUsage')) : null,
      modelPrompts: has('modelPrompts') ? count(get('modelPrompts')) : null,
    })
  }
  return out
}

// ---- Building the response ----

const emptyBucket = (): TokenBucket => ({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 })
const emptyModel = (): ModelBucket => ({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0 })
const emptyPlace = (): PlaceBucket => ({ in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: 0 })
const slot = <T>(obj: Record<string, T>, key: string, make: () => T) => obj[key] ?? (obj[key] = make())
const sorted = <T>(obj: Record<string, T>) => Object.fromEntries(Object.keys(obj).sort().map(key => [key, obj[key]]))

const TOKEN_FIELDS = ['in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning'] as const
const PLACE_FIELDS = ['in', 'cacheW', 'cacheR', 'out'] as const

const emptySource = (): WorkspaceDay => ({ exact: emptyBucket(), estimated: emptyBucket(), prompts: 0, promptsNoUsage: 0,
  models: { exact: {}, estimated: {} }, byMachine: {}, byCountry: {} })

function addSourceRow(p: WorkspaceDay, row: UsageRow, cc: string) {
  const machine = slot(p.byMachine, row.machine, emptyPlace)
  const place = slot(p.byCountry, cc || UNKNOWN_COUNTRY, emptyPlace)
  if (row.events > 0 || TOKEN_FIELDS.some(f => row[f] > 0)) {
    const tier = row.estimated ? 'estimated' : 'exact'
    const model = slot(p.models[tier], row.model || UNKNOWN_MODEL, emptyModel)
    for (const f of TOKEN_FIELDS) { p[tier][f] += row[f]; model[f] += row[f] }
    p[tier].events += row.events
    for (const f of PLACE_FIELDS) { machine[f] += row[f]; place[f] += row[f] }
  }
  p.prompts += row.prompts
  p.promptsNoUsage += row.promptsNoUsage ?? 0
  for (const split of [machine, place]) {
    split.prompts += row.prompts
    split.promptsNoUsage = (split.promptsNoUsage ?? 0) + (row.promptsNoUsage ?? 0)
  }
}

interface ProviderAcc {
  day: ProviderDay
  accts: Set<string>
  promptsByModel: Record<string, number>
  /** Every row has a modelPrompts column: per-model prompt counts are complete for this provider and day. */
  modelsKnown: boolean
  sourceModelPrompts: Record<string, Record<string, number> | null>
}

/**
 * acct1..N over every account hash in the published rows and account totals, in hash order (as the d0m1 API
 * aliases them).
 */
export function accountAliases(rows: UsageRow[], snapshots: AccountSnapshot[] = []): Map<string, string> {
  const hashes = [...new Set([...rows.map(r => r.acct), ...snapshots.map(s => s.acct)].filter(Boolean))].sort()
  return new Map(hashes.map((hash, i) => [hash, `acct${i + 1}`]))
}

/**
 * Days from rows, summed per local date and provider. `country` maps a machine id to its published country
 * ('' unknown). A provider's per-model prompt counts are given only when every row behind it carries them.
 */
export function buildDays(rows: UsageRow[], aliases: Map<string, string>, country: (machine: string) => string): RollupDay[] {
  const byDate = new Map<string, Partial<Record<Provider, ProviderAcc>>>()
  for (const row of rows) {
    const providers = byDate.get(row.date) ?? {}
    byDate.set(row.date, providers)
    const acc: ProviderAcc = providers[row.provider] ??= {
      day: { exact: emptyBucket(), estimated: emptyBucket(), prompts: 0, promptsNoUsage: 0, accts: [],
        models: { exact: {}, estimated: {} }, byMachine: {}, byCountry: {} },
      accts: new Set(), promptsByModel: {}, modelsKnown: true, sourceModelPrompts: Object.create(null),
    }
    const p = acc.day
    const channels = p.bySource ??= Object.create(null)
    addSourceRow(slot(channels, row.source || 'unknown', emptySource), row, country(row.machine))
    const source = row.source || 'unknown'
    if (row.modelPrompts === null) acc.sourceModelPrompts[source] = null
    else if (acc.sourceModelPrompts[source] !== null) {
      const counts = acc.sourceModelPrompts[source] ??= {}
      if (row.modelPrompts > 0) counts[row.model || UNKNOWN_MODEL] = (counts[row.model || UNKNOWN_MODEL] ?? 0) + row.modelPrompts
    }
    if (row.acct) acc.accts.add(aliases.get(row.acct) ?? 'unknown')
    const machine = slot(p.byMachine, row.machine, emptyPlace)
    const place = slot(p.byCountry, country(row.machine) || UNKNOWN_COUNTRY, emptyPlace)
    const model = row.model || UNKNOWN_MODEL
    if (row.events > 0 || TOKEN_FIELDS.some(f => row[f] > 0)) {
      const tier = row.estimated ? 'estimated' : 'exact'
      const bucket = slot(p.models[tier], model, emptyModel)
      for (const f of TOKEN_FIELDS) {
        p[tier][f] += row[f]
        bucket[f] += row[f]
      }
      p[tier].events += row.events
      for (const f of PLACE_FIELDS) {
        machine[f] += row[f]
        place[f] += row[f]
      }
    }
    p.prompts += row.prompts
    p.promptsNoUsage += row.promptsNoUsage ?? 0
    machine.prompts += row.prompts
    place.prompts += row.prompts
    if (row.modelPrompts === null) acc.modelsKnown = false
    else if (row.modelPrompts > 0) acc.promptsByModel[model] = (acc.promptsByModel[model] ?? 0) + row.modelPrompts
  }
  return [...byDate.keys()].sort().map(date => {
    const providers: RollupDay['providers'] = {}
    const accs = byDate.get(date)!
    for (const provider of PROVIDERS) {
      const acc = accs[provider]
      if (!acc) continue
      const p = acc.day
      for (const [source, counts] of Object.entries(acc.sourceModelPrompts)) {
        if (counts) p.bySource![source].promptsByModel = sorted(counts)
      }
      providers[provider] = {
        ...p,
        ...(acc.modelsKnown ? { promptsByModel: sorted(acc.promptsByModel) } : {}),
        accts: [...acc.accts].sort(),
        models: { exact: sorted(p.models.exact), estimated: sorted(p.models.estimated) },
        byMachine: sorted(p.byMachine),
        byCountry: sorted(p.byCountry),
      }
    }
    return { v: 4, date, providers }
  })
}

/** The public machine list, oldest first, as GET /api/usage orders it. */
export function publicMachines(published: Published, rows: UsageRow[], now: Date): PublicMachine[] {
  const firstRow = new Map<string, string>()
  for (const row of rows) if (!firstRow.has(row.machine) || row.date < firstRow.get(row.machine)!) firstRow.set(row.machine, row.date)
  const liveMs = published.live === false ? DEPLOYED_LIVE_MS : LIVE_MS
  return published.machines.map(({ id, meta }) => {
    const lastEventAt = validTime(meta?.lastEventAt)
    const firstSeenAt = typeof meta?.firstSeenAt === 'string' && DATE_RE.test(meta.firstSeenAt) ? meta.firstSeenAt : firstRow.get(id) ?? null
    return {
      id,
      label: text(meta?.label).slice(0, 64) || id,
      cc: country(meta?.cc),
      os: text(meta?.os).slice(0, 32),
      live: lastEventAt !== null && now.getTime() - Date.parse(lastEventAt) < liveMs,
      lastSeenAt: lastEventAt ?? validTime(meta?.updatedAt),
      firstSeenAt,
    }
  }).sort((a, b) => (a.firstSeenAt || '').localeCompare(b.firstSeenAt || '') || a.id.localeCompare(b.id))
}

// ---- Account history ----

/** A machine's published account totals; malformed ones are skipped (as the API's validation rejects them). */
export function readSnapshots(file: AccountUsageFile | null | undefined): AccountSnapshot[] {
  const out: AccountSnapshot[] = []
  for (const s of Array.isArray(file?.snapshots) ? file.snapshots as unknown[] : []) {
    if (!isRecord(s)) continue
    const provider = text(s.provider)
    const date = text(s.date)
    const observed = Date.parse(text(s.observedAt))
    const total = s.totalTokens
    if (!isProvider(provider) || !text(s.source) || !text(s.acct) || !DATE_RE.test(date) || !Number.isFinite(observed) ||
      typeof total !== 'number' || !Number.isSafeInteger(total) || total < 0) continue
    out.push({ provider, source: text(s.source), acct: text(s.acct), date, totalTokens: total, observedAt: new Date(observed).toISOString() })
  }
  return out
}

/** A machine's account ledger by its "ledgerCols" names, or null when it publishes none (no coverage). */
export function readLedger(file: AccountUsageFile | null | undefined): LedgerEntry[] | null {
  if (!file || !Array.isArray(file.ledger) || !Array.isArray(file.ledgerCols) || !file.ledgerCols.every(c => typeof c === 'string')) return null
  const at = new Map(file.ledgerCols.map((name, i) => [name, i]))
  if (!['date', 'source', 'acct', 'tokens'].every(name => at.has(name))) return null
  const out: LedgerEntry[] = []
  for (const raw of file.ledger) {
    if (!Array.isArray(raw)) continue
    const get = (name: string) => raw[at.get(name) ?? -1]
    const date = text(get('date'))
    if (!DATE_RE.test(date) || !text(get('source'))) continue
    out.push({ date, provider: text(get('provider')), source: text(get('source')), acct: text(get('acct')), totalTokens: count(get('tokens')) })
  }
  return out
}

/**
 * Account history added to the fleet's days as the API adds it: provider totals beyond the local records,
 * reconciled per UTC day against every machine's ledger. A UTC day is covered (its ledger complete) unless a
 * machine that publishes no ledger, or lists that local date as unledgered, has local tokens of a reconciled
 * source on that day or next to it (local dates are within a day of UTC); totals that need such a day stay
 * pending, so nothing is ever counted twice.
 */
export function applyAccountUsage(days: RollupDay[], machines: MachineFiles[], rows: UsageRow[], aliases: Map<string, string>, now: Date):
{ days: RollupDay[]; pending: number; conflicts: number } {
  const snapshots = machines.flatMap(m => readSnapshots(m.accountUsage))
  if (!snapshots.length) return { days, pending: 0, conflicts: 0 }
  const ledger = new Map<string, LedgerEntry[]>()
  const covered = new Set<string>()
  const unledgered = new Map<string, Set<string>>()
  for (const m of machines) {
    const entries = readLedger(m.accountUsage)
    if (!entries) continue
    covered.add(m.id)
    for (const e of entries) ledger.set(e.date, [...(ledger.get(e.date) ?? []), e])
    const gaps = m.accountUsage?.unledgered
    unledgered.set(m.id, new Set(Array.isArray(gaps) ? gaps.filter(d => typeof d === 'string' && DATE_RE.test(d)) : []))
  }
  const sources = new Set(snapshots.map(s => s.source))
  const uncovered = new Set<string>()
  for (const row of rows) {
    if (covered.has(row.machine) && !unledgered.get(row.machine)?.has(row.date)) continue
    if (!sources.has(row.source) || row.in + row.cacheW + row.cacheR + row.out === 0) continue
    for (const date of surroundingDates(row.date)) uncovered.add(date)
  }
  // Every date the totals can reach, as the API's ingest gives each of them a ledger (empty when nothing ran).
  const dates = snapshots.map(s => s.date).sort()
  const byDate = new Map<string, LedgerDay>(days.map(day => [day.date, day]))
  for (const date of datesBetween(surroundingDates(dates[0])[0], surroundingDates(dates.at(-1)!)[2])) {
    if (!byDate.has(date)) byDate.set(date, { v: 3, date, providers: {} })
  }
  const ledgerDays = [...byDate.values()].map(day => uncovered.has(day.date) ? day
    : { ...day, accountLedger: { v: ACCOUNT_LEDGER_VERSION, entries: ledger.get(day.date) ?? [] } })
  const reconciled = reconcileAccountUsage(ledgerDays, snapshots, now, acct => aliases.get(acct) ?? 'unknown')
  return {
    // The ledger is private to the reconciliation, as the API drops it before responding.
    days: reconciled.days.map(day => {
      const out = { ...day }
      delete out.accountLedger
      return out
    }),
    pending: reconciled.pending,
    conflicts: reconciled.conflicts,
  }
}

export interface BuildOptions {
  /** Only this machine's rows (aliases and the machine list stay fleet-wide). */
  machine?: string
}

/** The UsageResponse GET /api/usage?days=all would serve for these files. */
export function buildUsage(published: Published, now: Date, options: BuildOptions = {}): UsageResponse {
  const all = published.machines.flatMap(m => m.usage.flatMap(file => readUsageRows(m.id, file)))
  const snapshots = published.machines.flatMap(m => readSnapshots(m.accountUsage))
  const aliases = accountAliases(all, snapshots)
  const countries = new Map(published.machines.map(m => [m.id, country(m.meta?.cc)]))
  const rows = options.machine === undefined ? all : all.filter(r => r.machine === options.machine)
  const local = buildDays(rows, aliases, id => countries.get(id) ?? '')
  // Account history belongs to no machine (the API files it under machine "unknown"), so one machine's own rows have
  // none: the page adds that machine's estimated share of it (attribution.ts, addAssignedHistory), as for d0m1.com.
  const account = options.machine === undefined ? applyAccountUsage(local, published.machines, all, aliases, now) : { days: local, pending: 0, conflicts: 0 }
  const days = account.days.filter(day => Object.keys(day.providers).length > 0)
  const machines = publicMachines(published, all, now)
  const accountsByProvider: Partial<Record<Provider, number>> = {}
  for (const provider of PROVIDERS) {
    accountsByProvider[provider] = new Set([...all, ...snapshots].filter(r => r.provider === provider && r.acct).map(r => r.acct)).size
  }
  let lastIngestAt: string | null = null
  for (const m of published.machines) {
    const at = validTime(m.meta?.updatedAt)
    if (at && (!lastIngestAt || Date.parse(at) > Date.parse(lastIngestAt))) lastIngestAt = at
  }
  return {
    generatedAt: validTime(published.generatedAt) ?? now.toISOString(),
    lastIngestAt,
    firstDate: days[0]?.date ?? null,
    days,
    accountUsagePending: account.pending,
    accountUsageConflicts: account.conflicts,
    machines,
    totals: { accounts: aliases.size, accountsByProvider, machines: machines.length, machinesLive: machines.filter(m => m.live).length },
  }
}

// ---- Loading ----

type Fetch = typeof fetch

/** The machines listed in the index, each with its listed files and (schema 4) their git object ids. */
function listed(index: IndexFile) {
  return (Array.isArray(index.machines) ? index.machines : [])
    .filter(m => isRecord(m) && typeof m.id === 'string' && /^m_[0-9a-f]{12}$/.test(m.id))
    .map(m => ({
      id: m.id,
      files: Array.isArray(m.files) ? m.files.filter((f): f is string => typeof f === 'string') : [],
      blobs: new Map(isRecord(m.blobs) ? Object.entries(m.blobs).filter((e): e is [string, string] => typeof e[1] === 'string') : []),
    }))
}

/** git's object id for a file with these bytes (`git hash-object`), or null where WebCrypto is unavailable. */
export async function blobId(bytes: Uint8Array): Promise<string | null> {
  const subtle = globalThis.crypto?.subtle
  if (!subtle) return null
  const head = new TextEncoder().encode(`blob ${bytes.length}\0`)
  const all = new Uint8Array(head.length + bytes.length)
  all.set(head)
  all.set(bytes, head.length)
  return [...new Uint8Array(await subtle.digest('SHA-1', all))].map(b => b.toString(16).padStart(2, '0')).join('')
}

/** Where a public repository's files are read from as they are on its default branch. */
export const RAW_ORIGIN = 'https://raw.githubusercontent.com/'
const REPOSITORY_RE = /^[A-Za-z0-9-]+\/[A-Za-z0-9._-]+$/

/** Where data/ is read from: the repository itself, or the deployed copy. */
interface Place {
  base: string
  live: boolean
}

class LoadError extends Error {
  readonly status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

export interface GithubSourceOptions {
  /** Where the deployed data/ lives, relative to the page (default: beside it). */
  base?: string
  /**
   * The repository ("owner/name") whose data/ to read live, or null: the deployed copy only. Read from
   * raw.githubusercontent.com (`raw`, its default branch); a private repository is read from the deployed copy.
   */
  repository?: () => Promise<string | null>
  raw?: string
  fetch?: Fetch
  now?: () => Date
  /** The owner key while the owner is signed in (owner.ts), else null. It only ever decrypts: no request carries it. */
  ownerKey?: () => CryptoKey | null | Promise<CryptoKey | null>
}

export type GithubSource = UsageSource & {
  fetchIndex(signal?: AbortSignal): Promise<IndexFile>
  fetchLimits(signal: AbortSignal): Promise<LimitRow[]>
  checkOwnerKey(key: CryptoKey, signal?: AbortSignal): Promise<'ok' | 'none' | 'wrong'>
}


export function githubSource({
  base = '', repository, raw = RAW_ORIGIN, fetch: get = (...args) => fetch(...args), now = () => new Date(), ownerKey = () => null,
}: GithubSourceOptions = {}): GithubSource {
  // The files behind each response, so one machine can be rebuilt exactly from its own rows.
  const behind = new WeakMap<UsageResponse, { published: Published; at: Date }>()

  const deployed: Place = { base, live: false }
  // The repository's own files (null: no repository named, or not a name).
  let repo: Promise<Place | null> | undefined
  const liveRepo = () => repo ??= Promise.resolve()
    .then(() => repository?.() ?? null)
    .then(name => typeof name === 'string' && REPOSITORY_RE.test(name) ? { base: `${raw}${name}/HEAD/`, live: true } : null, () => null)
  // The repository has no index the collectors wrote (a private repository answers 404, as does one whose
  // collectors write none): the deployed copy for the rest of this visit.
  let liveOff = false
  // Whether the last load read the repository's own files (the footer says so).
  let readLive = false
  // A load has read the repository's own files on this visit: a later failure is an error, not the older copy.
  let liveOk = false
  // The newest generatedAt served, so the footer's time never steps back (a fallback to the deployed copy).
  let lastGeneratedAt: string | undefined

  // Each file as last read, with the git object id its bytes have: a file still listed with that id is not read again.
  const kept = new Map<string, { blob: string; value: unknown }>()
  // Files that may be missing (null): account-usage.json and owner.json behind an index too old to list them.
  const optional = new Set<string>()
  const read = async (place: Place, path: string, signal?: AbortSignal, onBytes?: (n: number) => void, blob?: string): Promise<unknown> => {
    const copy = blob === undefined ? undefined : kept.get(path)
    if (copy && copy.blob === blob) return copy.value
    const response = await get(place.base + path, { cache: 'no-cache', signal, headers: { Accept: 'application/json' } })
    if (response.status === 404 && path === 'data/index.json' && !place.live) {
      throw new LoadError('Nothing has been published yet. Once a collector publishes, this page fills in (the first build takes a few minutes).', 404)
    }
    if (response.status === 404 && (optional.has(path) || (place.live && unlisted.has(path)))) return null
    if (!response.ok) throw new LoadError(`Could not load ${path} (${response.status}). Retrying shortly.`, response.status)
    const bytes = new Uint8Array(await response.arrayBuffer())
    onBytes?.(bytes.length)
    let value: unknown
    try {
      value = JSON.parse(new TextDecoder().decode(bytes))
    } catch {
      throw new LoadError(`${path} is not valid JSON.`, response.status)
    }
    // Kept only when these are the listed bytes: an older copy still cached is read again next time.
    if (blob !== undefined && await blobId(bytes) === blob) kept.set(path, { blob, value })
    return value
  }
  const readIndex = async (place: Place, signal?: AbortSignal) => {
    const index = await read(place, 'data/index.json', signal)
    return isRecord(index) ? index as IndexFile : {}
  }

  // Files only the deployed copy's index lists, read live all the same (null when gone from the repository).
  const unlisted = new Set<string>()
  /**
   * The repository's index with every file the deployed copy's index lists too. Only collectors that write an
   * index rewrite the repository's, so a file committed since by one that does not (an older collector, a new
   * machine) is missing from it: the deployed index (build-index.mjs, from the whole data/ folder) lists it.
   */
  const withDeployed = async (index: IndexFile, signal?: AbortSignal): Promise<{ index: IndexFile; deployedIndex?: IndexFile }> => {
    let other: IndexFile
    try {
      other = await readIndex(deployed, signal)
    } catch (error) {
      if (signal?.aborted) throw error
      return { index }
    }
    const machines = new Map<string, { id: string; files: string[]; blobs: Record<string, string> }>()
    for (const m of listed(index)) {
      machines.set(m.id, { id: m.id, files: [...m.files], blobs: Object.fromEntries(m.blobs) })
      for (const f of m.files) unlisted.delete(`data/machines/${m.id}/${f}`)
    }
    for (const m of listed(other)) {
      const into = machines.get(m.id) ?? { id: m.id, files: [], blobs: {} }
      machines.set(m.id, into)
      for (const f of m.files.filter(f => !into.files.includes(f))) {
        into.files.push(f)
        const blob = m.blobs.get(f)
        if (blob) into.blobs[f] = blob
        unlisted.add(`data/machines/${m.id}/${f}`)
      }
      into.files.sort()
    }
    return { index: { ...index, machines: [...machines.values()].sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0) }, deployedIndex: other }
  }

  interface IndexLoad {
    index: IndexFile
    place: Place
    /** Read live: the deployed copy's own index, when it loaded (a fallback needs no second read). */
    deployedIndex?: IndexFile
    /** The repository's index failed this time (not a 404): why. The index is then the deployed copy's. */
    liveError?: unknown
  }

  /** The index to read by: the repository's own, when its collectors commit one (schema 4), else the deployed copy's. */
  const readIndexes = async (signal: AbortSignal): Promise<IndexLoad> => {
    const live = liveOff ? null : await liveRepo()
    let liveError: unknown
    if (live) {
      try {
        const index = await readIndex(live, signal)
        if ((index.schema ?? 0) >= 4) return { ...await withDeployed(index, signal), place: live }
        liveOff = true
      } catch (error) {
        if (signal.aborted) throw error
        // A 404: the repository has none (private, or no collector writes one). Anything else (offline,
        // rate-limited): the deployed copy this time only.
        if (error instanceof LoadError && error.status === 404) liveOff = true
        else liveError = error
      }
    }
    return { index: await readIndex(deployed, signal), place: deployed, liveError }
  }

  // The index load under way, shared by every caller that asks meanwhile (the page's title and its first usage
  // load start together). It has its own signal, stopped only when every caller waiting on it has given up, so
  // one caller's abort never fails another's load.
  let indexLoad: { promise: Promise<IndexLoad>; controller: AbortController; waiting: number } | undefined
  const loadIndex = (signal?: AbortSignal): Promise<IndexLoad> => {
    if (signal?.aborted) return Promise.reject(signal.reason)
    let shared = indexLoad
    if (!shared) {
      const controller = new AbortController()
      const started: NonNullable<typeof indexLoad> = { controller, waiting: 0, promise: readIndexes(controller.signal) }
      started.promise.then(() => undefined, () => undefined).finally(() => { if (indexLoad === started) indexLoad = undefined })
      indexLoad = shared = started
    }
    const load = shared
    load.waiting++
    return new Promise<IndexLoad>((resolve, reject) => {
      let gone = false
      const leave = () => {
        if (gone) return false
        gone = true
        load.waiting--
        signal?.removeEventListener('abort', onAbort)
        return true
      }
      const onAbort = () => {
        if (!leave()) return
        if (load.waiting === 0) {
          // Nobody waits any more: stop the read, and let the next caller start a new one.
          if (indexLoad === load) indexLoad = undefined
          load.controller.abort(signal!.reason)
        }
        reject(signal!.reason)
      }
      signal?.addEventListener('abort', onAbort, { once: true })
      load.promise.then(value => { if (leave()) resolve(value) }, error => { if (leave()) reject(error) })
    })
  }

  /**
   * load over the index's files; when one of the repository's own does not load, over the deployed copy this
   * time. keepLive (the usage load): once a live read has worked on this visit, a failed one fails instead, so the
   * page keeps its newer data and retries rather than stepping back to the older deployed copy.
   */
  const fromIndex = async <T>(signal: AbortSignal | undefined, load: (index: IndexFile, place: Place) => Promise<T>, keepLive = false): Promise<T> => {
    const { index, place, deployedIndex, liveError } = await loadIndex(signal)
    if (keepLive && liveOk && liveError !== undefined) throw liveError
    try {
      const out = await load(index, place)
      readLive = place.live
      if (place.live) liveOk = true
      return out
    } catch (error) {
      if (!place.live || signal?.aborted || (keepLive && liveOk)) throw error
    }
    const out = await load(deployedIndex ?? await readIndex(deployed, signal), deployed)
    readLive = false
    return out
  }

  // The collectors' index has no title (it is tokenmaxr.json's): read it there once.
  let title: Promise<string | undefined> | undefined
  const fetchIndex = async (signal?: AbortSignal): Promise<IndexFile> => {
    const { index, place } = await loadIndex(signal)
    if (!place.live || typeof index.title === 'string') return index
    title ??= read(place, 'tokenmaxr.json').then(c => isRecord(c) && typeof c.title === 'string' ? c.title : undefined, () => undefined)
    return { ...index, title: await title }
  }

  /** Every machine's owner.json opened with key: how many there are, and the rows of those that open. */
  const ownerRows = (key: CryptoKey, signal?: AbortSignal) => fromIndex(signal, async (index, place) => {
    // Only a schema 3 index (build-index.mjs since owner files) lists owner.json; under an older one, look.
    const listsOwner = (index.schema ?? 1) >= 3
    const files = await Promise.all(listed(index).filter(m => listsOwner ? m.files.includes(OWNER_FILE) : m.files.length > 0).map(async ({ id, blobs }) => {
      const path = `data/machines/${id}/${OWNER_FILE}`
      if (!listsOwner) optional.add(path)
      const file = await read(place, path, signal, undefined, blobs.get(OWNER_FILE))
      return file === null ? null : { id, file }
    }))
    const present = files.filter(f => f !== null)
    const rows = await Promise.all(present.map(({ id, file }) => decryptOwnerFile(file, id, key)))
    return { present: present.length, opened: rows.filter(r => r !== null) }
  })

  return {
    cacheKey: `github:${base || './'}`,
    get label() {
      return readLive ? 'Data: daily totals published to this repository, read live' : 'Data: daily totals published to this repository'
    },
    loadingLabel: 'published data',
    unavailableText: 'No usage to show yet.',
    collectorHref: 'https://github.com/7-of-9/tokenmaxr',
    // Collectors publish hourly by default; the repository's files are cached for up to 5 minutes and the deployed
    // copy is refreshed hourly. Between changes a poll reads the two indexes alone.
    pollMs: 300_000,

    fetchIndex,

    async fetchUsage(signal, progress: (transfer: UsageTransfer) => void) {
      let received = 0
      const onBytes = (n: number) => {
        received += n
        progress({ phase: 'downloading', received, total: null })
      }
      progress({ phase: 'downloading', received, total: null })
      const loaded = await fromIndex(signal, async (index, place): Promise<Published> => {
        // A schema 1 index (the first template's build-index.mjs) never lists account-usage.json.
        const listsAccountUsage = (index.schema ?? 1) >= 2
        const machines = await Promise.all(listed(index).map(async ({ id, files, blobs }): Promise<MachineFiles | null> => {
          const dir = `data/machines/${id}/`
          const accountPath = dir + 'account-usage.json'
          const lookForAccount = !files.includes('account-usage.json') && !listsAccountUsage && files.includes('meta.json')
          if (lookForAccount) optional.add(accountPath)
          const [meta, usage, accountUsage] = await Promise.all([
            files.includes('meta.json') ? read(place, dir + 'meta.json', signal, onBytes, blobs.get('meta.json')) : null,
            Promise.all(files.filter(f => USAGE_FILE_RE.test(f)).map(f => read(place, dir + f, signal, onBytes, blobs.get(f)))),
            files.includes('account-usage.json') || lookForAccount ? read(place, accountPath, signal, onBytes, blobs.get('account-usage.json')) : null,
          ])
          const machine: MachineFiles = {
            id,
            meta: isRecord(meta) ? meta as MetaFile : null,
            usage: usage.filter(isRecord) as UsageFile[],
            accountUsage: isRecord(accountUsage) ? accountUsage as AccountUsageFile : null,
          }
          // Listed only by the deployed index, and every file gone from the repository: the machine was removed
          // since the last deploy, so it is not shown.
          const gone = place.live && files.length > 0 && files.every(f => unlisted.has(dir + f)) &&
            machine.meta === null && machine.usage.length === 0 && machine.accountUsage === null
          return gone ? null : machine
        }))
        return { title: index.title, generatedAt: index.generatedAt, live: place.live, machines: machines.filter(m => m !== null) }
      }, true)
      progress({ phase: 'processing', received, total: null })
      const at = now()
      // The footer's "updated" time never steps back, as when a read falls back to the older deployed copy.
      let generatedAt = validTime(loaded.generatedAt) ?? at.toISOString()
      if (lastGeneratedAt !== undefined && Date.parse(lastGeneratedAt) > Date.parse(generatedAt)) generatedAt = lastGeneratedAt
      lastGeneratedAt = generatedAt
      const published: Published = { ...loaded, generatedAt }
      const data = buildUsage(published, at)
      behind.set(data, { published, at })
      return data
    },

    // Per-model prompt counts travel in the usage rows (modelPrompts) and are already on each day.
    async fetchPromptCounts() {
      return null
    },

    scopeMachine(data, machine) {
      const source = behind.get(data)
      if (!source?.published.machines.some(m => m.id === machine)) return null
      return buildUsage(source.published, source.at, { machine })
    },

    // Signed out: nothing is read (401, the page's sign-in gate). Signed in: every machine's owner file, merged as
    // the API merges its rows. Files that are there but none of which open for this key: the wrong key (403).
    async fetchLimits(signal) {
      const key = await ownerKey()
      if (!key) throw new LimitsDenied(401)
      const { present, opened } = await ownerRows(key, signal)
      if (present > 0 && opened.length === 0) throw new LimitsDenied(403)
      return mergeLimitRows(opened)
    },

    // A key a sign-in derived, before it is kept: does it open the published owner files (none published: none)?
    async checkOwnerKey(key, signal) {
      const { present, opened } = await ownerRows(key, signal)
      return present === 0 ? 'none' : opened.length > 0 ? 'ok' : 'wrong'
    },
  }
}
