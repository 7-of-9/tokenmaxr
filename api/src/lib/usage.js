// GET /api/usage?days=371|all (SPEC "Read API"). Public: day aggregates plus
// the machines list, whose labels and countries are public by owner decision.
// Never account/workspace hashes or labels, workspace paths, project names or prompt text.
import { error, json } from './http.js'
import { resolveCtx } from './context.js'
import { limiter, deadline } from './pool.js'
import { num } from './tables.js'
import { needsRecompute, needsUpgrade, recomputeDay } from './days.js'
import { aliasAccts, hasData, legacyShape } from './rollup.js'
import { PROVIDERS, cleanCountry, cleanLabel } from './validate.js'
import { privateRollupData, readAccountUsage, reconcileAccountUsage, surroundingDates } from './account-usage.js'

const DAY_MS = 24 * 3600 * 1000
const LIVE_MS = 10 * 60 * 1000
const DEFAULT_DAYS = 371
const MAX_DAYS = 3700
const MAX_LAZY_DAYS = 31
const MAX_UPGRADE_DAYS = 120
const RECOMPUTE_WORKERS = 6
// Stop starting recomputes this long before the budget ends.
const RECOMPUTE_RESERVE_MS = 3000

// -> number of days, or null for `all`.
export function parseDays(raw) {
  if (raw === 'all') return null
  const n = parseInt(raw, 10)
  return Number.isFinite(n) ? Math.min(Math.max(n, 1), MAX_DAYS) : DEFAULT_DAYS
}

export async function handleUsage(req, rawCtx) {
  if (req.method !== 'GET') return error(405, 'method not allowed')
  const ctx = resolveCtx(rawCtx)
  const { store } = ctx
  const now = req.now
  const budget = deadline(Math.min(ctx.budgetMs, 20000))
  const lim = limiter(ctx.parallel)

  const days = parseDays(req.query.days)
  // One extra day back: local dates can trail UTC by up to 12 hours.
  const from = days == null ? null : new Date(now.getTime() - days * DAY_MS).toISOString().slice(0, 10)
  // A provider UTC day may include events in the preceding local calendar
  // day. Load that extra ledger without exposing an extra day in the view.
  const coverageFrom = from == null ? null : surroundingDates(from)[0]

  const [rollRows, dirtyRows, earlier, acctRows, machineRows, accountSnapshots] = await Promise.all([
    lim(() => store.table('rollups').list('r', { rkGte: coverageFrom })),
    lim(() => store.table('dirtydays').list('d', { rkGte: coverageFrom })),
    // Keys only, for firstDate when the range starts after the first data.
    from == null ? [] : lim(() => store.table('rollups').list('r', { rkLte: from, select: ['gen', 'data', 'accountLedger'] })),
    lim(() => store.table('accounts').list('a', { select: ['provider'] })),
    lim(() => store.table('machines').list('m', { select: ['label', 'cc', 'os', 'enrolledAt', 'lastSeenAt', 'lastIngestAt'] })),
    readAccountUsage(store, lim),
  ])

  const byDate = new Map()
  for (const r of rollRows) {
    try {
      byDate.set(r.rowKey, { gen: num(r.gen), data: privateRollupData(r) })
    } catch {
      byDate.set(r.rowKey, { gen: -1, data: null })
    }
  }

  // Lazy recompute, newest first: days still dirty a minute after their last
  // write (a deferred recompute, or an ingest that ran out of time or
  // crashed), then rollups written before the current schema or unreadable.
  const stale = dirtyRows
    .filter((d) => needsRecompute(d, byDate.get(d.rowKey), now))
    .map((d) => d.rowKey)
    .sort()
    .reverse()
    .slice(0, MAX_LAZY_DAYS)
  const staleSet = new Set(stale)
  const upgrade = [...byDate]
    .filter(([date, r]) => !staleSet.has(date) && (!r.data || needsUpgrade(r)))
    .map(([date]) => date)
    .sort()
    .reverse()
    .slice(0, MAX_UPGRADE_DAYS)
  const queue = [...stale, ...upgrade]
  let next = 0
  await Promise.all(Array.from({ length: Math.min(RECOMPUTE_WORKERS, queue.length) }, async () => {
    while (next < queue.length && !budget.expired(RECOMPUTE_RESERVE_MS)) {
      const date = queue[next++]
      try {
        const res = await recomputeDay(store, lim, date, now)
        if (res.data) byDate.set(date, { gen: res.gen, data: res.data })
      } catch (err) {
        console.error(`[usage] recompute ${date}: ${err?.statusCode || ''} ${err?.message || ''}`)
      }
    }
  }))

  // A v1 rollup that is still waiting for its recompute is served in the v2
  // shape (with v: 1), so the page never loses a day.
  const localDays = [...byDate.keys()]
    .sort()
    .map((d) => byDate.get(d).data)
    .filter(Boolean)
    .map(legacyShape)
  // Reconcile against the same complete account window for every requested
  // range. Filtering first could hide an overage outside the viewport and
  // incorrectly increase the recovered total in a shorter view.
  const reconciliationDays = new Map()
  for (const row of earlier) {
    try { reconciliationDays.set(row.rowKey, legacyShape(privateRollupData(row))) } catch { /* Pending ledger handled below. */ }
  }
  for (const day of localDays) reconciliationDays.set(day.date, day)
  const reconciled = reconcileAccountUsage([...reconciliationDays.values()], accountSnapshots, now)
  const dayList = reconciled.days.filter((day) => hasData(day) && (from == null || day.date >= from))

  const accountsByProvider = {}
  for (const p of PROVIDERS) accountsByProvider[p] = 0
  for (const a of acctRows) if (a.provider in accountsByProvider) accountsByProvider[a.provider]++

  let lastIngestAt = null
  for (const m of machineRows) {
    if (m.lastIngestAt && (!lastIngestAt || m.lastIngestAt > lastIngestAt)) lastIngestAt = m.lastIngestAt
  }
  const machines = publicMachines(machineRows, now)

  const firstDate = reconciled.days.find(hasData)?.date ?? null

  return json(200, {
    generatedAt: now.toISOString(),
    lastIngestAt,
    firstDate,
    days: aliasAccts(dayList),
    accountUsagePending: reconciled.pending,
    accountUsageConflicts: reconciled.conflicts,
    machines,
    totals: {
      accounts: acctRows.length,
      accountsByProvider,
      machines: machines.length,
      machinesLive: machines.filter((m) => m.live).length,
    },
  }, { 'Cache-Control': 'public, max-age=60' })
}

// The public machine list: an explicit allow-list of fields, so nothing else
// on the row (token hash, heartbeat, time zone) can leak.
export function publicMachines(rows, now) {
  return rows
    .map((m) => ({
      id: m.rowKey,
      label: cleanLabel(m.label) || m.rowKey,
      cc: cleanCountry(m.cc),
      os: typeof m.os === 'string' ? m.os.slice(0, 32) : '',
      live: Boolean(m.lastSeenAt) && now.getTime() - Date.parse(m.lastSeenAt) < LIVE_MS,
      lastSeenAt: m.lastSeenAt || null,
      firstSeenAt: m.enrolledAt || null,
    }))
    .sort((a, b) => (a.firstSeenAt || '').localeCompare(b.firstSeenAt || '') || a.id.localeCompare(b.id))
}
