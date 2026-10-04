// Dirty-day generations and rollup recompute (SPEC "Rollup").
//
// Protocol: an ingest bumps dirtydays.gen BEFORE writing a day's events, then
// recomputes, unless that day's rollup is younger than 20 s (a backfill
// writes the same day many times in a row; recomputing each time would list
// the whole partition per batch). A recompute reads gen G, then the rollup row
// (and its ETag), and only then lists the day's events; it writes {gen: G}
// with that ETag unless the stored gen is already newer. On any ETag conflict
// it starts over, so a rollup computed from a listing that predates another
// writer's rollup can never land on top of it.
//
// Only a recompute whose listing follows an ingest's writes can settle them.
// A concurrent recompute (usage, sweep, another ingest of the same day) may
// read the gen after this ingest's bump but list before its writes land, and
// store a rollup with that gen. So when an ingest does not recompute after
// its writes (deferred, or out of time) it bumps the gen AGAIN: any rollup
// listed before the writes is then behind the dirty gen, and the day is
// recomputed once its last write is more than 60 s old, by GET /api/usage
// and by the ingest sweep. A day left dirty by a crashed ingest settles the
// same way, so no day stays dirty for long.
import { computeRollup, isCurrentRollup } from './rollup.js'
import { rowToEvent } from './merge.js'
import { isStatus, num } from './tables.js'
import { ACCOUNT_LEDGER_VERSION, accountLedger } from './account-usage.js'

const RETRIES = 8
export const ROLLUP_MIN_AGE_MS = 20 * 1000
export const DIRTY_GRACE_MS = 60 * 1000

export const EVENT_SELECT = [
  'kind', 'provider', 'source', 'ts', 'tzOffsetMin', 'model', 'acct', 'acctQ', 'q', 'pv', 'session',
  'in', 'cacheW', 'cacheW1h', 'cacheR', 'out', 'reasoning', 'calls', 'hasUsage', 'machine', 'cc', 'ws',
]

export async function bumpDirty(store, lim, date, now) {
  const t = store.table('dirtydays')
  for (let i = 0; i < RETRIES; i++) {
    const row = await lim(() => t.get('d', date))
    const entity = { partitionKey: 'd', rowKey: date, gen: num(row?.gen) + 1, updatedAt: now.toISOString() }
    try {
      if (row) await lim(() => t.replace(entity, row.etag))
      else await lim(() => t.create(entity))
      return entity.gen
    } catch (err) {
      if (!isStatus(err, 404, 409, 412)) throw err
    }
  }
  throw new Error(`dirtydays contention on ${date}`)
}

// -> { data, gen } when written, { skipped: true } when a newer rollup exists,
// { deferred: true, gen } when minAgeMs is set and the stored rollup is
// younger. minAgeMs is only passed by an ingest after its writes: deferring
// bumps the dirty gen again (see above), so it is `gen` that a later
// recompute must reach; if that bump is contended, it recomputes now instead.
export async function recomputeDay(store, lim, date, now, { minAgeMs = 0 } = {}) {
  const dirtyT = store.table('dirtydays')
  const rollT = store.table('rollups')
  const eventsT = store.table('events')
  let mustCompute = false
  for (let i = 0; i < RETRIES; i++) {
    const dirty = await lim(() => dirtyT.get('d', date))
    const gen = num(dirty?.gen)
    const roll = await lim(() => rollT.get('r', date))
    if (roll && num(roll.gen) > gen) return { skipped: true }
    if (!mustCompute && minAgeMs > 0 && roll && now.getTime() - (Date.parse(roll.computedAt) || 0) < minAgeMs) {
      try {
        return { deferred: true, gen: await bumpDirty(store, lim, date, now) }
      } catch {
        // Contended or unreachable: settle the writes with a recompute now.
        mustCompute = true
      }
    }
    const rows = await lim(() => eventsT.list(date, { select: EVENT_SELECT }))
    const events = rows.map(rowToEvent)
    const data = computeRollup(date, events)
    const ledger = accountLedger(events)
    const entity = {
      partitionKey: 'r',
      rowKey: date,
      gen,
      data: JSON.stringify(data),
      accountLedger: JSON.stringify(ledger),
      accountLedgerVersion: ACCOUNT_LEDGER_VERSION,
      computedAt: now.toISOString(),
    }
    try {
      if (roll) await lim(() => rollT.replace(entity, roll.etag))
      else await lim(() => rollT.create(entity))
      return { data: { ...data, accountLedger: ledger }, gen }
    } catch (err) {
      if (!isStatus(err, 404, 409, 412)) throw err
    }
  }
  throw new Error(`rollup contention on ${date}`)
}

// A day needs a recompute when its dirty gen is ahead of the rollup (or it has
// none) and its last write is past the grace period. `roll` is
// { gen, data } | undefined, with data null when unreadable.
export function needsRecompute(dirtyRow, roll, now) {
  const quiet = now.getTime() - (Date.parse(dirtyRow.updatedAt) || 0) > DIRTY_GRACE_MS
  return quiet && (!roll || !roll.data || roll.gen < num(dirtyRow.gen))
}

// Rollups written before the current schema are recomputed on read, whatever their gen.
export const needsUpgrade = (roll) => Boolean(roll?.data) && !isCurrentRollup(roll.data)

// Ingest-side sweep: recompute up to `max` quiet dirty days, newest first.
// Used by ingest so dirty days settle even when nobody reads /api/usage.
export async function sweepDirty(store, lim, now, { max, budget }) {
  const [dirtyRows, rollRows] = await Promise.all([
    lim(() => store.table('dirtydays').list('d', { select: ['gen', 'updatedAt'] })),
    lim(() => store.table('rollups').list('r', { select: ['gen'] })),
  ])
  const gens = new Map(rollRows.map((r) => [r.rowKey, { gen: num(r.gen), data: true }]))
  const stale = dirtyRows
    .filter((d) => needsRecompute(d, gens.get(d.rowKey), now))
    .map((d) => d.rowKey)
    .sort()
    .reverse()
    .slice(0, max)
  let done = 0
  for (const date of stale) {
    if (budget.expired()) break
    await recomputeDay(store, lim, date, now)
    done++
  }
  return done
}
