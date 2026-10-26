// Account history on the GitHub dashboard: a port of the pure parts of api/src/lib/account-usage.js, so the page
// adds exactly what d0m1.com would. (That module also talks to table storage, which a static page cannot load.)
//
// Provider account totals are snapshots, not additional token events. They cover every machine signed into an
// account and have no token breakdown, so only the part not already represented by local tokens is added, as
// "unattributed" exact tokens. Comparison always uses the provider's UTC day, never the local calendar day of the
// activity view: each collector publishes its local tokens per UTC day (the account ledger) beside the totals.
//
// The page differs from the server in one respect only: its days carry no workspace split, so the recovered
// tokens are not added to a byWorkspace.unknown group (the server adds them there as well as to the provider).
import type { ModelBucket, PlaceBucket, Provider, ProviderDay, RollupDay, TokenBucket } from '../../src/components/agents/types.ts'

/** The server's ledger version: a day whose ledger has another version (or none) is not covered yet. */
export const ACCOUNT_LEDGER_VERSION = 3
const UNKNOWN_MACHINE = 'unknown'
const UNKNOWN_COUNTRY = 'ZZ'
const UNKNOWN_MODEL = 'unknown'

/** Local tokens (in + cacheW + cacheR + out) on one UTC day; acct is '' unless the attribution is labelled. */
export interface LedgerEntry {
  date: string
  provider: string
  source: string
  acct: string
  totalTokens: number
}

/** One provider account total for a UTC day. */
export interface AccountSnapshot {
  provider: Provider
  source: string
  acct: string
  date: string
  totalTokens: number
  /** ISO 8601 (toISOString), so readings compare as strings exactly as the server compares them. */
  observedAt: string
}

/** A day as reconciliation reads it: its ledger rides along until the public response drops it. */
export type LedgerDay = RollupDay & { accountLedger?: { v: number; entries: LedgerEntry[] } }

export interface Reconciled {
  days: LedgerDay[]
  pending: number
  conflicts: number
}

const keyOf = (item: { date: string; source: string; acct: string }) => `${item.date}|${item.source}|${item.acct}`

export const surroundingDates = (date: string) => [-1, 0, 1].map(offset =>
  new Date(Date.parse(`${date}T00:00:00Z`) + offset * 86400000).toISOString().slice(0, 10))

export function datesBetween(from: string, to: string) {
  const dates: string[] = []
  for (let ms = Date.parse(`${from}T00:00:00Z`), end = Date.parse(`${to}T00:00:00Z`); ms <= end; ms += 86400000) {
    dates.push(new Date(ms).toISOString().slice(0, 10))
  }
  return dates
}

const emptyBucket = (): TokenBucket => ({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0, events: 0 })
const emptyModel = (): ModelBucket => ({ in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0 })
const emptyPlace = (): PlaceBucket => ({ in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: 0 })

function addUnattributed(group: ProviderDay, tokens: number) {
  group.exact.unattributed = (group.exact.unattributed || 0) + tokens
  const model = group.models.exact[UNKNOWN_MODEL] ||= emptyModel()
  model.unattributed = (model.unattributed || 0) + tokens
  for (const [splits, key] of [[group.byMachine, UNKNOWN_MACHINE], [group.byCountry, UNKNOWN_COUNTRY]] as const) {
    const split = splits[key] ||= emptyPlace()
    split.unattributed = (split.unattributed || 0) + tokens
  }
}

/** The ordinary shape with no request, machine or model invented (the server's computeRollup of one empty event). */
const emptyProvider = (acct: string): ProviderDay => ({
  exact: emptyBucket(), estimated: emptyBucket(), prompts: 0, promptsNoUsage: 0, accts: [acct],
  models: { exact: { [UNKNOWN_MODEL]: emptyModel() }, estimated: {} },
  byMachine: { [UNKNOWN_MACHINE]: emptyPlace() }, byCountry: { [UNKNOWN_COUNTRY]: emptyPlace() },
})

/**
 * reconcileAccountUsage of api/src/lib/account-usage.js, line for line. `alias` turns an account hash into the
 * name the days already use for it (the page aliases while it builds days; the server aliases afterwards).
 * Never mutates its input. Missing ledgers make their snapshots pending: unknown coverage is never treated as zero.
 */
export function reconcileAccountUsage(days: LedgerDay[], snapshots: AccountSnapshot[], now: Date, alias: (acct: string) => string = a => a): Reconciled {
  if (!snapshots.length) return { days, pending: 0, conflicts: 0 }
  const result = new Map(days.map(day => [day.date, structuredClone(day)]))
  const local = new Map<string, number>()
  const unknown = new Map<string, number>()
  for (const day of days) for (const item of day.accountLedger?.entries || []) {
    const key = keyOf(item)
    local.set(key, (local.get(key) || 0) + item.totalTokens)
    if (!item.acct) {
      const sourceKey = `${item.date}|${item.source}`
      unknown.set(sourceKey, (unknown.get(sourceKey) || 0) + item.totalTokens)
    }
  }
  let pending = 0
  const newest = new Map<string, AccountSnapshot>()
  for (const snapshot of snapshots) {
    const key = keyOf(snapshot)
    const previous = newest.get(key)
    if (!previous || snapshot.observedAt > previous.observedAt ||
      (snapshot.observedAt === previous.observedAt && snapshot.totalTokens > previous.totalTokens)) newest.set(key, snapshot)
  }
  interface Window { from: string; to: string; reported: number; matched: number; uncertain: number; deficits: number; pending?: boolean; conflict?: boolean }
  const windows = new Map<string, Window>()
  const today = now.toISOString().slice(0, 10)
  // A provider meter can lag today's ongoing local calls: the live day is its own window. Closed history is one.
  const windowKey = (snapshot: AccountSnapshot) => `${snapshot.source}|${snapshot.acct}|${snapshot.date >= today ? snapshot.date : 'closed'}`
  for (const snapshot of newest.values()) {
    const key = windowKey(snapshot)
    const window = windows.get(key) || { from: snapshot.date, to: snapshot.date, reported: 0, matched: 0, uncertain: 0, deficits: 0 }
    if (snapshot.date < window.from) window.from = snapshot.date
    if (snapshot.date > window.to) window.to = snapshot.date
    window.reported += snapshot.totalTokens
    window.deficits += Math.max(0, snapshot.totalTokens - (local.get(keyOf(snapshot)) || 0) - (unknown.get(`${snapshot.date}|${snapshot.source}`) || 0))
    windows.set(key, window)
  }
  let conflicts = 0
  for (const [key, window] of windows) {
    const [source, acct] = key.split('|')
    window.pending = datesBetween(surroundingDates(window.from)[0], surroundingDates(window.to)[2])
      .some(date => result.get(date)?.accountLedger?.v !== ACCOUNT_LEDGER_VERSION)
    // Include dates between sparse provider buckets, not just listed dates.
    for (const date of datesBetween(window.from, window.to)) {
      window.matched += local.get(`${date}|${source}|${acct}`) || 0
      window.uncertain += unknown.get(`${date}|${source}`) || 0
    }
    window.conflict = window.deficits > Math.max(0, window.reported - window.matched - window.uncertain)
    if (window.conflict && !window.pending) conflicts++
  }
  for (const snapshot of newest.values()) {
    const window = windows.get(windowKey(snapshot))!
    if (window.pending) {
      pending++
      continue
    }
    const matchedTokens = local.get(keyOf(snapshot)) || 0
    // Unknown-account local tokens may overlap this account: reserve them too, never assign them to it.
    const uncertainTokens = unknown.get(`${snapshot.date}|${snapshot.source}`) || 0
    // A window whose daily differences would inflate its total keeps the local counts and shows the conflict.
    const recoveredTokens = window.conflict ? 0 : Math.max(0, snapshot.totalTokens - matchedTokens - uncertainTokens)
    let day = result.get(snapshot.date)
    if (!day) result.set(snapshot.date, (day = { v: 3, date: snapshot.date, providers: {} }))
    const name = alias(snapshot.acct)
    const p = day.providers[snapshot.provider] ||= emptyProvider(name)
    if (!p.accts.includes(name)) p.accts.push(name)
    const records = p.accountUsage ||= []
    records.push({ source: snapshot.source, timezone: 'UTC', timezoneBasis: 'collector-convention', totalTokens: snapshot.totalTokens, matchedTokens,
      uncertainTokens, recoveredTokens, observedAt: snapshot.observedAt, status: window.conflict ? 'conflict' : 'reconciled',
      ...(window.conflict ? { windowFrom: window.from, windowTo: window.to, windowReportedTokens: window.reported,
        windowMatchedTokens: window.matched, windowUncertainTokens: window.uncertain } : {}) } as NonNullable<ProviderDay['accountUsage']>[number])
    if (!recoveredTokens) continue
    addUnattributed(p, recoveredTokens)
  }
  return { days: [...result.values()].sort((a, b) => a.date.localeCompare(b.date)), pending, conflicts }
}
