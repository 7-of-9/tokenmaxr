// Provider account totals are snapshots, not additional token events. They
// cover every machine signed into an account and sometimes have no token
// breakdown at all. Store them separately and add only the portion not already
// represented by local events. Comparison always uses the provider's UTC day,
// never the local calendar day used by the activity view.
import { isStatus } from './tables.js'
import { computeRollup, UNKNOWN_COUNTRY, UNKNOWN_MACHINE, UNKNOWN_MODEL } from './rollup.js'
import { acctRank } from './merge.js'

const PARTITION = 'usage'
const TOKEN_BUCKETS = ['in', 'cacheW', 'cacheR', 'out']
export const ACCOUNT_LEDGER_VERSION = 3
const keyOf = (item) => `${item.date}|${item.source}|${item.acct}`
export const surroundingDates = (date) => [-1, 0, 1].map((offset) =>
  new Date(Date.parse(`${date}T00:00:00Z`) + offset * 86400000).toISOString().slice(0, 10))
export function datesBetween(from, to) {
  const dates = []
  for (let ms = Date.parse(`${from}T00:00:00Z`), end = Date.parse(`${to}T00:00:00Z`); ms <= end; ms += 86400000) {
    dates.push(new Date(ms).toISOString().slice(0, 10))
  }
  return dates
}

// Storage-only ledger in its own Table property, outside public rollup JSON.
// Older API versions spread rollup.data onto public responses; keeping the
// ledger separate is essential for safe rolling deployment and rollback.
// This remains small:
// one entry per UTC day, provider, source and account, not one per event.
export function accountLedger(events) {
  const sums = new Map()
  for (const ev of events) {
    if (ev.kind !== 'usage' || !Number.isFinite(Date.parse(ev.ts))) continue
    // A weak candidate account is not evidence that an event belongs to a
    // different account. Reserve those tokens as uncertain rather than add
    // the provider's total on top of potentially overlapping local records.
    const entry = { date: new Date(ev.ts).toISOString().slice(0, 10), provider: ev.provider, source: ev.source,
      acct: acctRank(ev.acctQ) >= 3 ? ev.acct || '' : '' }
    const key = keyOf(entry)
    const sum = sums.get(key) || { ...entry, totalTokens: 0 }
    sum.totalTokens += TOKEN_BUCKETS.reduce((n, field) => n + (ev[field] || 0), 0)
    sums.set(key, sum)
  }
  return { v: ACCOUNT_LEDGER_VERSION, entries: [...sums.values()] }
}

// Attach the separate private column only inside the new API. The public
// serializer removes it. Never trust an embedded ledger from the old layout.
export function privateRollupData(row) {
  const data = JSON.parse(row.data)
  delete data.accountLedger
  if (typeof row.accountLedger === 'string') {
    try { data.accountLedger = JSON.parse(row.accountLedger) } catch { /* Old/malformed coverage stays pending. */ }
  }
  return data
}

// Canonical key ignores the reporter and client id. An ETag loop means older
// concurrent observations cannot overwrite a newer snapshot, including a
// provider correction which reduces a previously reported total.
export async function upsertAccountUsage(store, lim, item) {
  const table = store.table('accounts')
  const rowKey = keyOf(item)
  for (let attempt = 0; attempt < 8; attempt++) {
    const row = await lim(() => table.get(PARTITION, rowKey))
    if (row?.observedAt > item.observedAt) return
    if (row?.observedAt === item.observedAt && row.totalTokens >= item.totalTokens) return
    const entity = { partitionKey: PARTITION, rowKey, ...item }
    try {
      if (row) await lim(() => table.replace(entity, row.etag))
      else await lim(() => table.create(entity))
      return
    } catch (err) {
      if (!isStatus(err, 404, 409, 412)) throw err
    }
  }
  throw new Error('account usage snapshot contention')
}

export const readAccountUsage = (store, lim, from = null) =>
  lim(() => store.table('accounts').list(PARTITION, { rkGte: from }))

function addUnattributed(group, tokens) {
  group.exact.unattributed = (group.exact.unattributed || 0) + tokens
  const model = group.models.exact[UNKNOWN_MODEL] ||= { in: 0, cacheW: 0, cacheW1h: 0, cacheR: 0, out: 0, reasoning: 0, calls: 0 }
  model.unattributed = (model.unattributed || 0) + tokens
  for (const [splits, key] of [[group.byMachine, UNKNOWN_MACHINE], [group.byCountry, UNKNOWN_COUNTRY]]) {
    const split = splits[key] ||= { in: 0, cacheW: 0, cacheR: 0, out: 0, prompts: 0 }
    split.unattributed = (split.unattributed || 0) + tokens
  }
}

function emptyProvider(date, provider, acct) {
  // Reuse the ordinary shape, but do not invent a request, machine or model.
  const p = computeRollup(date, [{ kind: 'usage', provider, acct, q: 'exact' }]).providers[provider]
  p.exact.events = 0
  p.byWorkspace.unknown.exact.events = 0
  return p
}

// Input rollups retain their private ledger until public serialization. Never
// mutate cached storage objects: each request may select a different range.
// Missing old ledgers suppress additions until ingest upgrades the relevant
// dates; treating unknown coverage as zero would inflate the public total.
export function reconcileAccountUsage(days, snapshots, now = new Date()) {
  if (!snapshots.length) return { days, pending: 0, conflicts: 0 }
  const result = new Map(days.map((day) => [day.date, structuredClone(day)]))
  const local = new Map()
  const unknown = new Map()
  for (const day of days) for (const item of day.accountLedger?.entries || []) {
    const key = keyOf(item)
    local.set(key, (local.get(key) || 0) + item.totalTokens)
    if (!item.acct) {
      const sourceKey = `${item.date}|${item.source}`
      unknown.set(sourceKey, (unknown.get(sourceKey) || 0) + item.totalTokens)
    }
  }
  let pending = 0
  const newest = new Map()
  for (const snapshot of snapshots) {
    const key = keyOf(snapshot)
    const previous = newest.get(key)
    if (!previous || snapshot.observedAt > previous.observedAt ||
      (snapshot.observedAt === previous.observedAt && snapshot.totalTokens > previous.totalTokens)) newest.set(key, snapshot)
  }
  const windows = new Map()
  const today = now.toISOString().slice(0, 10)
  // A provider meter can lag today's ongoing local calls. Keep the live day
  // separate so it cannot make verified history disappear. Closed history
  // is one window: month boundaries cannot conceal a shifted day/overage.
  const windowKey = (snapshot) => `${snapshot.source}|${snapshot.acct}|${snapshot.date >= today ? snapshot.date : 'closed'}`
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
      .some((date) => result.get(date)?.accountLedger?.v !== ACCOUNT_LEDGER_VERSION)
    // Include dates between sparse provider buckets, not just listed dates.
    // Missing/nonzero local days may indicate incompatible date boundaries.
    for (const date of datesBetween(window.from, window.to)) {
      window.matched += local.get(`${date}|${source}|${acct}`) || 0
      window.uncertain += unknown.get(`${date}|${source}`) || 0
    }
    window.conflict = window.deficits > Math.max(0, window.reported - window.matched - window.uncertain)
    if (window.conflict && !window.pending) conflicts++
  }
  for (const snapshot of newest.values()) {
    const window = windows.get(windowKey(snapshot))
    if (window.pending) {
      pending++
      continue
    }
    const matchedTokens = local.get(keyOf(snapshot)) || 0
    // Unknown-account local events may overlap this account. Conservatively
    // reserve them too; they are never assigned to the account in the UI.
    const uncertainTokens = unknown.get(`${snapshot.date}|${snapshot.source}`) || 0
    // If clipping daily differences would inflate the overall account window,
    // keep local counts and expose the conflict. Do not redistribute or scale
    // reported counts across days to manufacture a plausible calendar.
    const recoveredTokens = window.conflict ? 0 : Math.max(0, snapshot.totalTokens - matchedTokens - uncertainTokens)
    let day = result.get(snapshot.date)
    if (!day) result.set(snapshot.date, (day = { v: 3, date: snapshot.date, providers: {} }))
    const p = day.providers[snapshot.provider] ||= emptyProvider(snapshot.date, snapshot.provider, snapshot.acct)
    if (!p.accts.includes(snapshot.acct)) p.accts.push(snapshot.acct)
    const records = p.accountUsage ||= []
    records.push({ source: snapshot.source, timezone: 'UTC', timezoneBasis: 'collector-convention', totalTokens: snapshot.totalTokens, matchedTokens,
      uncertainTokens, recoveredTokens, observedAt: snapshot.observedAt, status: window.conflict ? 'conflict' : 'reconciled',
      ...(window.conflict ? { windowFrom: window.from, windowTo: window.to, windowReportedTokens: window.reported,
        windowMatchedTokens: window.matched, windowUncertainTokens: window.uncertain } : {}) })
    if (!recoveredTokens) continue
    addUnattributed(p, recoveredTokens)
    const workspace = p.byWorkspace.unknown ||= emptyProvider(snapshot.date, snapshot.provider, snapshot.acct).byWorkspace.unknown
    addUnattributed(workspace, recoveredTokens)
  }
  return { days: [...result.values()].sort((a, b) => a.date.localeCompare(b.date)), pending, conflicts }
}
