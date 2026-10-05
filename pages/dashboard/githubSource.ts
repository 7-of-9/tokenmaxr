// The GitHub Pages data source: reads the static files collectors publish to the user's repository
// (data/index.json, written by scripts/build-index.mjs, lists each machine's files) and builds the same
// UsageResponse d0m1.com's API serves, so the shared /tokens page (TokensView) shows the same nuance.
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
// decrypted in the owner's browser (owner.ts); a page that is not unlocked reads none of them.
import {
  ACCOUNT_LEDGER_VERSION, datesBetween, reconcileAccountUsage, surroundingDates,
  type AccountSnapshot, type LedgerDay, type LedgerEntry,
} from './accountUsage.ts'
import { LimitsDenied, type LimitRow } from '../../src/components/agents/limits.ts'
import type { UsageSource } from '../../src/components/agents/source.ts'
import type { ModelBucket, PlaceBucket, Provider, ProviderDay, PublicMachine, RollupDay, TokenBucket, UsageResponse } from '../../src/components/agents/types.ts'
import type { UsageTransfer } from '../../src/components/agents/usageResponse.ts'
import { decryptOwnerFile, mergeLimitRows, OWNER_FILE } from './owner.ts'

const PROVIDERS: Provider[] = ['anthropic', 'openai', 'xai', 'cursor', 'google']
/** Column order of schema 1 files, which some early collectors wrote without "cols". */
export const SCHEMA1_COLS = ['date', 'provider', 'source', 'model', 'acct', 'in', 'cacheW', 'cacheR', 'out', 'events', 'prompts']
/** A machine whose newest event is this recent is live (the collector publishes about every 15 minutes). */
export const LIVE_MS = 30 * 60 * 1000
const UNKNOWN_COUNTRY = 'ZZ'
const UNKNOWN_MODEL = 'unknown'
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/
const USAGE_FILE_RE = /^usage-\d{4}-\d{2}\.json$/

// ---- What the repository holds ----

export interface IndexFile {
  schema?: number
  title?: string
  generatedAt?: string
  machines?: Array<{ id: string; files?: string[] }>
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

interface ProviderAcc {
  day: ProviderDay
  accts: Set<string>
  promptsByModel: Record<string, number>
  /** Every row has a modelPrompts column: per-model prompt counts are complete for this provider and day. */
  modelsKnown: boolean
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
    const acc = providers[row.provider] ??= {
      day: { exact: emptyBucket(), estimated: emptyBucket(), prompts: 0, promptsNoUsage: 0, accts: [],
        models: { exact: {}, estimated: {} }, byMachine: {}, byCountry: {} },
      accts: new Set(), promptsByModel: {}, modelsKnown: true,
    }
    const p = acc.day
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
      providers[provider] = {
        ...p,
        ...(acc.modelsKnown ? { promptsByModel: sorted(acc.promptsByModel) } : {}),
        accts: [...acc.accts].sort(),
        models: { exact: sorted(p.models.exact), estimated: sorted(p.models.estimated) },
        byMachine: sorted(p.byMachine),
        byCountry: sorted(p.byCountry),
      }
    }
    return { v: 3, date, providers }
  })
}

/** The public machine list, oldest first, as GET /api/usage orders it. */
export function publicMachines(published: Published, rows: UsageRow[], now: Date): PublicMachine[] {
  const firstRow = new Map<string, string>()
  for (const row of rows) if (!firstRow.has(row.machine) || row.date < firstRow.get(row.machine)!) firstRow.set(row.machine, row.date)
  return published.machines.map(({ id, meta }) => {
    const lastEventAt = validTime(meta?.lastEventAt)
    const firstSeenAt = typeof meta?.firstSeenAt === 'string' && DATE_RE.test(meta.firstSeenAt) ? meta.firstSeenAt : firstRow.get(id) ?? null
    return {
      id,
      label: text(meta?.label).slice(0, 64) || id,
      cc: country(meta?.cc),
      os: text(meta?.os).slice(0, 32),
      live: lastEventAt !== null && now.getTime() - Date.parse(lastEventAt) < LIVE_MS,
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
  // Account history belongs to no machine (the API files it under machine "unknown"), so one machine's view has none.
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

/** The machines listed in the index, each with its listed files. */
function listed(index: IndexFile) {
  return (Array.isArray(index.machines) ? index.machines : [])
    .filter(m => isRecord(m) && typeof m.id === 'string' && /^m_[0-9a-f]{12}$/.test(m.id))
    .map(m => ({ id: m.id, files: Array.isArray(m.files) ? m.files.filter((f): f is string => typeof f === 'string') : [] }))
}

export interface GithubSourceOptions {
  /** Where data/ lives, relative to the page (default: beside it). */
  base?: string
  fetch?: Fetch
  now?: () => Date
  /** The owner key while the page is unlocked (owner.ts), else null. It only ever decrypts: no request carries it. */
  ownerKey?: () => CryptoKey | null | Promise<CryptoKey | null>
}

export type GithubSource = UsageSource & {
  fetchIndex(signal?: AbortSignal): Promise<IndexFile>
  fetchLimits(signal: AbortSignal): Promise<LimitRow[]>
}

export function githubSource({ base = '', fetch: get = (...args) => fetch(...args), now = () => new Date(), ownerKey = () => null }: GithubSourceOptions = {}): GithubSource {
  // The files behind each response, so one machine can be rebuilt exactly from its own rows.
  const behind = new WeakMap<UsageResponse, { published: Published; at: Date }>()

  // Files that may be missing (null): account-usage.json and owner.json behind an index too old to list them.
  const optional = new Set<string>()
  const read = async (path: string, signal?: AbortSignal, onBytes?: (n: number) => void): Promise<unknown> => {
    const response = await get(base + path, { cache: 'no-cache', signal, headers: { Accept: 'application/json' } })
    if (response.status === 404 && path === 'data/index.json') {
      throw new Error('Nothing has been published yet. Once a collector publishes, this page fills in (the first build takes a few minutes).')
    }
    if (response.status === 404 && optional.has(path)) return null
    if (!response.ok) throw new Error(`Could not load ${path} (${response.status}). Retrying shortly.`)
    const body = await response.text()
    onBytes?.(body.length)
    try {
      return JSON.parse(body)
    } catch {
      throw new Error(`${path} is not valid JSON.`)
    }
  }
  const fetchIndex = async (signal?: AbortSignal) => {
    const index = await read('data/index.json', signal)
    return isRecord(index) ? index as IndexFile : {}
  }

  return {
    cacheKey: `github:${base || './'}`,
    label: 'Data: daily totals published to this repository',
    loadingLabel: 'published data',
    unavailableText: 'No usage to show yet.',
    collectorHref: 'https://github.com/7-of-9/tokenmaxr',
    // Pages redeploys a few minutes after each collector push.
    pollMs: 120_000,

    fetchIndex,

    async fetchUsage(signal, progress: (transfer: UsageTransfer) => void) {
      let received = 0
      const onBytes = (n: number) => {
        received += n
        progress({ phase: 'downloading', received, total: null })
      }
      progress({ phase: 'downloading', received, total: null })
      const index = await fetchIndex(signal)
      // A schema 1 index (the first template's build-index.mjs) never lists account-usage.json.
      const listsAccountUsage = (index.schema ?? 1) >= 2
      const machines = await Promise.all(listed(index).map(async ({ id, files }): Promise<MachineFiles> => {
        const dir = `data/machines/${id}/`
        const accountPath = dir + 'account-usage.json'
        const lookForAccount = !files.includes('account-usage.json') && !listsAccountUsage && files.includes('meta.json')
        if (lookForAccount) optional.add(accountPath)
        const [meta, usage, accountUsage] = await Promise.all([
          files.includes('meta.json') ? read(dir + 'meta.json', signal, onBytes) : null,
          Promise.all(files.filter(f => USAGE_FILE_RE.test(f)).map(f => read(dir + f, signal, onBytes))),
          files.includes('account-usage.json') || lookForAccount ? read(accountPath, signal, onBytes) : null,
        ])
        return {
          id,
          meta: isRecord(meta) ? meta as MetaFile : null,
          usage: usage.filter(isRecord) as UsageFile[],
          accountUsage: isRecord(accountUsage) ? accountUsage as AccountUsageFile : null,
        }
      }))
      progress({ phase: 'processing', received, total: null })
      const published: Published = { title: index.title, generatedAt: index.generatedAt, machines }
      const at = now()
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

    // Locked: nothing is read (401, the page's "unlock" gate). Unlocked: every machine's owner file, merged as the
    // API merges its rows. Files that are there but none of which open for this key: the wrong key (403).
    async fetchLimits(signal) {
      const key = await ownerKey()
      if (!key) throw new LimitsDenied(401)
      const index = await fetchIndex(signal)
      // Only a schema 3 index (build-index.mjs since owner files) lists owner.json; under an older one, look.
      const listsOwner = (index.schema ?? 1) >= 3
      const files = await Promise.all(listed(index).filter(m => listsOwner ? m.files.includes(OWNER_FILE) : m.files.length > 0).map(async ({ id }) => {
        const path = `data/machines/${id}/${OWNER_FILE}`
        if (!listsOwner) optional.add(path)
        const file = await read(path, signal)
        return file === null ? null : { id, file }
      }))
      const present = files.filter(f => f !== null)
      const rows = await Promise.all(present.map(({ id, file }) => decryptOwnerFile(file, id, key)))
      const opened = rows.filter(r => r !== null)
      if (present.length > 0 && opened.length === 0) throw new LimitsDenied(403)
      return mergeLimitRows(opened)
    },
  }
}
