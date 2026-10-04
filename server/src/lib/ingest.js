// POST /api/ingest (SPEC "Wire format: collector -> server").
//
// Order of work, all under a 30 s budget and a bounded storage pool:
//   1. pin each event id to a local date in eventindex (first sight wins)
//   2. per day: read existing rows, merge; if anything changes, bump the
//      day's dirty gen, write with ETags, then recompute the rollup unless it
//      is younger than 20 s (the day then stays dirty; see days.js)
//   3. prompts: encrypt and insert once (model-fill on an empty model; a
//      better account attribution re-encrypts acct/acctLabel in place)
//   4. accounts first/last seen, the prompt months list, the machine row,
//      and at most once a minute a sweep of quiet dirty days
// Anything not finished inside the budget is returned in `retry`.
//
// New event rows record the reporting machine and its current country
// (machine, cc); merges never change them.
import { error, json } from './http.js'
import { resolveCtx } from './context.js'
import { machineFromToken, patchCachedMachine } from './auth.js'
import { limiter, deadline } from './pool.js'
import { isStatus } from './tables.js'
import { acctRank, legacyPromptQ, mergeEvent, rowToEvent, sameEvent, storedFields } from './merge.js'
import { bumpDirty, recomputeDay, sweepDirty } from './days.js'
import { decryptJson, encryptJson, parseKey } from './crypto.js'
import { PROMPT_INDEX, addMonths } from './months.js'
import {
  checkCaps, localDate, truncateUtf8, utcMonth,
  validateActivity, validateAccountUsage, validateHeartbeat, validateLimit, validatePrompt, validateUsage,
} from './validate.js'
import { upsertLimits } from './limits.js'
import { ACCOUNT_LEDGER_VERSION, datesBetween, readAccountUsage, surroundingDates, upsertAccountUsage } from './account-usage.js'

const RETRIES = 6
// Leave room after the event writes for the rollup recompute and the response.
const RECOMPUTE_RESERVE_MS = 4000
const SWEEP_EVERY_MS = 60 * 1000
const SWEEP_MAX_DAYS = 4
// Entity group transaction limit (Azure Tables).
const BATCH_MAX = 100
export const INLINE_ENC_MAX = 30000
export const PREVIEW_BYTES = 4096

export async function handleIngest(req, rawCtx) {
  if (req.method !== 'POST') return error(405, 'method not allowed')
  const ctx = resolveCtx(rawCtx)
  const { store, env } = ctx
  const now = req.now
  const budget = deadline(ctx.budgetMs)
  const lim = limiter(ctx.parallel)

  const machine = await machineFromToken(store, req.headers)
  if (!machine) return error(401, 'bad token')

  const body = req.body
  if (!body || typeof body !== 'object' || Array.isArray(body)) return error(400, 'body must be a JSON object')
  if (body.v !== 1) return error(400, 'unsupported wire version')
  const capErr = checkCaps(body)
  if (capErr) return error(capErr.status, capErr.error)

  const accepted = new Set()
  const retry = new Set()
  const rejected = []
  const warnings = []
  const reject = (r) => {
    rejected.push({ id: r.id, error: r.error })
    if (r.id) accepted.add(r.id)
  }

  let heartbeat = null
  if (body.heartbeat != null) {
    const r = validateHeartbeat(body.heartbeat, now)
    if (r.ok) heartbeat = r.value
    else rejected.push({ id: null, error: `heartbeat: ${r.error}` })
  }
  // The reporter's current country: this request's heartbeat if it has a tz,
  // else what the machine row holds.
  const cc = heartbeat?.tz ? heartbeat.tz.cc : machine.cc

  // ---- validate and de-duplicate within the request ----
  const events = new Map()
  for (const [list, validate] of [[body.usage ?? [], validateUsage], [body.activity ?? [], validateActivity]]) {
    for (const x of list) {
      const r = validate(x, now)
      if (!r.ok) {
        reject(r)
        continue
      }
      const ev = r.value
      if (ev.clamped) {
        warnings.push({ id: ev.id, warning: ev.clamped })
        delete ev.clamped
      }
      ev.machine = machine.machineId
      ev.cc = cc
      const prev = events.get(ev.id)
      if (prev && prev.kind !== ev.kind) {
        reject({ id: ev.id, error: 'id reused across kinds' })
        continue
      }
      events.set(ev.id, prev ? withTs(mergeEvent(prev, ev)) : ev)
    }
  }
  const prompts = new Map()
  for (const x of body.prompts ?? []) {
    const r = validatePrompt(x, now)
    if (!r.ok) {
      reject(r)
      continue
    }
    const prev = prompts.get(r.value.id)
    if (!prev || (!prev.model && r.value.model)) prompts.set(r.value.id, r.value)
  }

  // ---- 1. pin dates ----
  const pinned = []
  await Promise.all([...events.values()].map(async (ev) => {
    if (budget.expired()) return retry.add(ev.id)
    try {
      const pin = await pinDate(store, lim, ev)
      ev.date = pin.date
      ev.fresh = pin.fresh
      pinned.push(ev)
    } catch (err) {
      logErr('eventindex', err)
      retry.add(ev.id)
    }
  }))

  // ---- 2. per-day merge, write, recompute ----
  const byDate = new Map()
  for (const ev of pinned) {
    if (!byDate.has(ev.date)) byDate.set(ev.date, [])
    byDate.get(ev.date).push(ev)
  }
  const acctSeen = new Map()
  await Promise.all([...byDate].map(([date, evs]) =>
    processDay({ store, lim, budget, now, date, evs, accepted, retry, minAgeMs: ctx.rollupMinAgeMs }).catch((err) => {
      logErr(`day ${date}`, err)
      for (const ev of evs) if (!accepted.has(ev.id)) retry.add(ev.id)
    }),
  ))
  for (const ev of events.values()) {
    if (!accepted.has(ev.id) || !ev.acct) continue
    const a = acctSeen.get(ev.acct) ?? { provider: ev.provider, first: ev.ts, last: ev.ts }
    if (ev.ts < a.first) a.first = ev.ts
    if (ev.ts > a.last) a.last = ev.ts
    acctSeen.set(ev.acct, a)
  }

  // ---- 3. prompts ----
  // month -> ids accepted into it this request
  const months = new Map()
  if (prompts.size > 0) {
    let key = null
    try {
      key = parseKey(env.PROMPT_ENC_KEY)
    } catch (err) {
      logErr('prompts', err)
    }
    await Promise.all([...prompts.values()].map(async (p) => {
      if (!key || budget.expired()) return retry.add(p.id)
      try {
        const month = await upsertPrompt(store, lim, key, p)
        if (!months.has(month)) months.set(month, [])
        months.get(month).push(p.id)
        accepted.add(p.id)
      } catch (err) {
        logErr('prompt', err)
        retry.add(p.id)
      }
    }))
  }

  // ---- 3b. plan windows. Newest observedAt wins; a failed write is retried. ----
  const limitItems = []
  for (const x of body.limits ?? []) {
    const r = validateLimit(x, now)
    if (!r.ok) {
      reject(r)
      continue
    }
    limitItems.push(r.value)
  }
  if (limitItems.length) {
    const pending = budget.expired() ? limitItems.map((item) => item.id) : await upsertLimits(store, lim, limitItems)
    const failed = new Set(pending)
    for (const item of limitItems) {
      if (failed.has(item.id)) retry.add(item.id)
      else accepted.add(item.id)
    }
  }

  // ---- 3c. account history. Snapshot writes are independent of machine,
  // and the relevant old rollups gain a UTC ledger once. Daily totals are
  // reconciled on read so future transcript arrivals reduce the supplement.
  const accountItems = []
  for (const x of body.accountUsage ?? []) {
    const r = validateAccountUsage(x, now)
    if (!r.ok) reject(r)
    else accountItems.push(r.value)
  }
  // Adjacent snapshots share most dates; reuse each upgrade within this batch.
  const coverage = new Map()
  const coverageWorkers = limiter(4)
  const historyWindows = new Map()
  for (const item of accountItems) {
    const key = `${item.source}|${item.acct}`
    const window = historyWindows.get(key) || { from: item.date, to: item.date }
    if (item.date < window.from) window.from = item.date
    if (item.date > window.to) window.to = item.date
    historyWindows.set(key, window)
  }
  let historyIndexFailed = false
  const coverageIndex = new Map()
  if (accountItems.length && !budget.expired(RECOMPUTE_RESERVE_MS)) {
    try {
      // Changed-only collector batches can extend an older window across a
      // gap. Include already stored snapshots so every intervening date gets
      // its ledger once, even when the collector does not resend older days.
      for (const item of await readAccountUsage(store, lim)) {
        const window = historyWindows.get(`${item.source}|${item.acct}`)
        if (!window) continue
        if (item.date < window.from) window.from = item.date
        if (item.date > window.to) window.to = item.date
      }
      const first = [...historyWindows.values()].map((w) => w.from).sort()[0]
      const last = [...historyWindows.values()].map((w) => w.to).sort().at(-1)
      // Query tiny version markers once; a routine poll must not perform a
      // storage read for every day in an ever-growing historical window.
      const rows = await lim(() => store.table('rollups').list('r', {
        rkGte: surroundingDates(first)[0], rkLte: surroundingDates(last)[2], select: ['accountLedgerVersion'],
      }))
      for (const row of rows) coverageIndex.set(row.rowKey, row.accountLedgerVersion)
    } catch (err) {
      logErr('account usage index', err)
      historyIndexFailed = true
    }
  } else if (accountItems.length) historyIndexFailed = true
  const ensureCoverage = (date) => {
    if (!coverage.has(date)) coverage.set(date, coverageWorkers(async () => {
      if (coverageIndex.get(date) === ACCOUNT_LEDGER_VERSION) return
      if (budget.expired(RECOMPUTE_RESERVE_MS)) throw new Error('account history coverage deferred')
      await recomputeDay(store, lim, date, now)
    }))
    return coverage.get(date)
  }
  // Bound recomputes, not merely individual storage requests. This prevents
  // a first historical upload from launching hundreds of event listings.
  const historyWorkers = limiter(4)
  await Promise.all(accountItems.map((item) => historyWorkers(async () => {
    if (historyIndexFailed || budget.expired()) return retry.add(item.id)
    try {
      await upsertAccountUsage(store, lim, item)
      const window = historyWindows.get(`${item.source}|${item.acct}`)
      await Promise.all(datesBetween(surroundingDates(window.from)[0], surroundingDates(window.to)[2]).map(ensureCoverage))
      accepted.add(item.id)
      const ts = `${item.date}T00:00:00.000Z`
      const previous = acctSeen.get(item.acct)
      acctSeen.set(item.acct, { provider: item.provider,
        first: previous?.first && previous.first < ts ? previous.first : ts,
        last: previous?.last && previous.last > ts ? previous.last : ts })
    } catch (err) {
      logErr('account usage', err)
      retry.add(item.id)
    }
  })))

  // ---- 4. bookkeeping (best effort: never fails the request) ----
  const nowIso = now.toISOString()
  await Promise.all([
    updateAccounts(store, lim, acctSeen).catch((err) => logErr('accounts', err)),
    // addMonths skips months this process already recorded. The months list
    // is part of accepting a prompt (the archive's month picker and lookups
    // without ?month read it), so on failure its prompts go back to retry;
    // a re-send is harmless, upsertPrompt and addMonths are idempotent.
    months.size ? addMonths(store, lim, [...months.keys()]).catch((err) => {
      logErr('months', err)
      for (const ids of months.values()) for (const id of ids) retry.add(id)
    }) : null,
    touchMachine(store, lim, machine.machineId, nowIso, {
      ingested: [...accepted].some((id) => events.has(id) || prompts.has(id) || accountItems.some((item) => item.id === id)),
      heartbeat,
      collectorVersion: typeof body.collectorVersion === 'string' ? body.collectorVersion.slice(0, 64) : '',
    }).catch((err) => logErr('machine', err)),
    maybeSweep(store, lim, budget, now).catch((err) => logErr('sweep', err)),
  ])

  // The archive index (prompts.js) is rebuilt on its next read.
  if (months.size) store.cache.delete(PROMPT_INDEX)
  for (const id of retry) accepted.delete(id)
  return json(200, {
    ok: true,
    accepted: [...accepted],
    retry: [...retry],
    rejected,
    warnings,
    serverTime: new Date().toISOString(),
  })
}

function withTs(ev) {
  return { ...ev, tsMs: Date.parse(ev.ts) }
}

function logErr(where, err) {
  // Status and message only: never payloads, which can hold prompt text.
  console.error(`[ingest] ${where}: ${err?.name || 'Error'} ${err?.statusCode || ''} ${err?.message || ''}`)
}

// First sight pins the id to the local date of its ts; later sightings reuse
// it. -> { date, fresh }: fresh means this call pinned it, so no event row can
// exist yet and processDay skips the read before the insert.
export async function pinDate(store, lim, ev) {
  const t = store.table('eventindex')
  const pk = ev.id.slice(0, 2)
  const date = localDate(ev.tsMs, ev.tzOffsetMin)
  try {
    await lim(() => t.create({ partitionKey: pk, rowKey: ev.id, date }))
    return { date, fresh: true }
  } catch (err) {
    if (!isStatus(err, 409)) throw err
  }
  const row = await lim(() => t.get(pk, ev.id))
  return { date: row?.date || date, fresh: false }
}

async function processDay({ store, lim, budget, now, date, evs, accepted, retry, minAgeMs }) {
  const t = store.table('events')
  const plans = []
  await Promise.all(evs.map(async (ev) => {
    if (budget.expired()) return retry.add(ev.id)
    try {
      // Should a row exist despite a fresh pin, the insert's 409 falls back to
      // the ETag loop in writeEvent.
      const row = ev.fresh ? null : await lim(() => t.get(date, ev.id))
      const existing = row ? rowToEvent(row) : null
      const merged = mergeEvent(existing, ev)
      if (existing && sameEvent(existing, merged)) accepted.add(ev.id)
      else plans.push({ ev, row, merged })
    } catch (err) {
      logErr('events read', err)
      retry.add(ev.id)
    }
  }))
  if (plans.length === 0) return
  if (budget.expired()) {
    for (const p of plans) retry.add(p.ev.id)
    return
  }
  await bumpDirty(store, lim, date, now)
  let wrote = false
  const writeOne = async (plan) => {
    try {
      if (await writeEvent(t, lim, budget, date, plan)) {
        accepted.add(plan.ev.id)
        wrote = true
      } else {
        retry.add(plan.ev.id)
      }
    } catch (err) {
      logErr('events write', err)
      retry.add(plan.ev.id)
    }
  }
  // Freshly pinned ids have no row: insert them in transactions of up to
  // 100 (one partition, all or nothing). A failed batch, e.g. a row that
  // exists after all, falls back to the per-event ETag path.
  const inserts = plans.filter((p) => p.ev.fresh && !p.row)
  const chunks = []
  for (let i = 0; i < inserts.length; i += BATCH_MAX) chunks.push(inserts.slice(i, i + BATCH_MAX))
  await Promise.all([
    ...plans.filter((p) => !(p.ev.fresh && !p.row)).map(writeOne),
    ...chunks.map(async (chunk) => {
      if (chunk.length > 1 && !budget.expired()) {
        try {
          await lim(() => t.createBatch(chunk.map(({ ev, merged }) => ({ partitionKey: date, rowKey: ev.id, ...storedFields(merged) }))))
          for (const p of chunk) accepted.add(p.ev.id)
          wrote = true
          return
        } catch (err) {
          if (!isStatus(err, 409)) logErr('events batch', err)
        }
      }
      await Promise.all(chunk.map(writeOne))
    }),
  ])
  if (!wrote) return
  // Only a recompute listed after these writes settles them. Out of time,
  // bump the gen again (as a deferred recompute does) so a rollup that
  // another recompute listed before the writes stays behind it (days.js).
  if (budget.expired(RECOMPUTE_RESERVE_MS)) await bumpDirty(store, lim, date, now)
  else await recomputeDay(store, lim, date, now, { minAgeMs })
}

// Settles days left dirty by deferred recomputes, at most once a minute per
// process, with whatever budget the request has left.
async function maybeSweep(store, lim, budget, now) {
  const last = store.cache.get('sweepAt') ?? 0
  if (Date.now() - last < SWEEP_EVERY_MS || budget.expired(2 * RECOMPUTE_RESERVE_MS)) return
  store.cache.set('sweepAt', Date.now())
  await sweepDirty(store, lim, now, {
    max: SWEEP_MAX_DAYS,
    budget: { expired: () => budget.expired(RECOMPUTE_RESERVE_MS) },
  })
}

// ETag-checked read-modify-write. Returns false when the budget ran out.
async function writeEvent(t, lim, budget, date, { ev, row, merged }) {
  for (let i = 0; i < RETRIES; i++) {
    if (budget.expired()) return false
    const entity = { partitionKey: date, rowKey: ev.id, ...storedFields(merged) }
    try {
      if (row) await lim(() => t.replace(entity, row.etag))
      else await lim(() => t.create(entity))
      return true
    } catch (err) {
      if (!isStatus(err, 404, 409, 412)) throw err
    }
    row = await lim(() => t.get(date, ev.id))
    const existing = row ? rowToEvent(row) : null
    merged = mergeEvent(existing, ev)
    if (existing && sameEvent(existing, merged)) return true
  }
  return false
}

// Returns the prompt's month partition (SPEC "Server prompt upsert"). A new
// row stores its acctQ in plaintext beside the encrypted payload. An existing
// row is re-encrypted only to upgrade its account: when the incoming acctQ
// ranks above the stored one, its acct and acctLabel are replaced (text,
// workspace, machine and session stay as first written). Otherwise the
// ciphertext is never rewritten and only an empty model is filled in.
export async function upsertPrompt(store, lim, key, p) {
  const t = store.table('prompts')
  const month = utcMonth(p.tsMs)
  const existing = await lim(() => t.get(month, p.id))
  if (existing) {
    await updatePrompt(store, t, lim, key, existing, p)
    return month
  }
  const payload = {
    text: p.text,
    workspace: p.workspace,
    acctLabel: p.acctLabel,
    acct: p.acct,
    machine: p.machine,
    session: p.session,
  }
  const entity = {
    partitionKey: month,
    rowKey: p.id,
    ts: p.ts,
    tzOffsetMin: p.tzOffsetMin,
    provider: p.provider,
    source: p.source,
    model: p.model,
    acctQ: p.acctQ,
    blob: false,
  }
  const enc = encryptJson(key, p.id, payload)
  if (enc.length > INLINE_ENC_MAX) {
    // Full ciphertext in blob prompts/<id>; the row keeps an encrypted
    // preview so month listings never need to touch blob storage.
    await lim(() => store.blobs.put(p.id, enc))
    entity.blob = true
    entity.penc = encryptJson(key, p.id, { ...payload, text: truncateUtf8(p.text, PREVIEW_BYTES).text, truncated: true })
  } else {
    entity.enc = enc
  }
  try {
    await lim(() => t.create(entity))
    return month
  } catch (err) {
    if (!isStatus(err, 409)) throw err
  }
  const row = await lim(() => t.get(month, p.id))
  if (row) await updatePrompt(store, t, lim, key, row, p)
  return month
}

// The stored attribution quality of a prompt row: its plaintext acctQ, or for
// a row written before v1.4 the legacy rule over its (decrypted) payload.
export function storedPromptQ(key, row) {
  if (row.acctQ) return row.acctQ
  try {
    const payload = decryptJson(key, row.rowKey, row.blob ? row.penc : row.enc)
    return legacyPromptQ(payload.acct, payload.acctLabel)
  } catch {
    // Undecryptable rows are never rewritten.
    return 'recorded'
  }
}

async function updatePrompt(store, t, lim, key, row, p) {
  for (let i = 0; i < RETRIES; i++) {
    if (!(p.acct && acctRank(p.acctQ) > acctRank(storedPromptQ(key, row)))) return fillModel(t, lim, row, p)
    const entity = { partitionKey: row.partitionKey, rowKey: row.rowKey, acctQ: p.acctQ }
    if (!row.model && p.model) entity.model = p.model
    const swap = (payload) => ({ ...payload, acct: p.acct, acctLabel: p.acctLabel })
    if (row.blob) {
      const full = await lim(() => store.blobs.get(row.rowKey))
      if (full) await lim(() => store.blobs.put(row.rowKey, encryptJson(key, row.rowKey, swap(decryptJson(key, row.rowKey, full)))))
      entity.penc = encryptJson(key, row.rowKey, swap(decryptJson(key, row.rowKey, row.penc)))
    } else {
      entity.enc = encryptJson(key, row.rowKey, swap(decryptJson(key, row.rowKey, row.enc)))
    }
    try {
      await lim(() => t.merge(entity, row.etag))
      return
    } catch (err) {
      if (!isStatus(err, 412)) throw err
    }
    row = await lim(() => t.get(row.partitionKey, row.rowKey))
    if (!row) return
  }
}

async function fillModel(t, lim, row, p) {
  if (row.model || !p.model) return
  try {
    await lim(() => t.merge({ partitionKey: row.partitionKey, rowKey: row.rowKey, model: p.model }, row.etag))
  } catch (err) {
    // A concurrent writer changed the row; its model (if any) stands.
    if (!isStatus(err, 412)) throw err
  }
}

// accounts table: PK 'a', RK acct -> provider, firstSeen, lastSeen. No labels.
async function updateAccounts(store, lim, seen) {
  if (seen.size === 0) return
  const t = store.table('accounts')
  let cache = store.cache.get('accounts')
  if (!cache) store.cache.set('accounts', (cache = new Map()))
  await Promise.all([...seen].map(async ([acct, s]) => {
    const c = cache.get(acct)
    if (c && c.firstSeen <= s.first && c.lastSeen >= s.last) return
    for (let i = 0; i < RETRIES; i++) {
      const row = await lim(() => t.get('a', acct))
      const firstSeen = row?.firstSeen && row.firstSeen <= s.first ? row.firstSeen : s.first
      const lastSeen = row?.lastSeen && row.lastSeen >= s.last ? row.lastSeen : s.last
      const entity = { partitionKey: 'a', rowKey: acct, provider: row?.provider || s.provider, firstSeen, lastSeen }
      try {
        if (!row) await lim(() => t.create(entity))
        else if (row.firstSeen !== firstSeen || row.lastSeen !== lastSeen) await lim(() => t.replace(entity, row.etag))
        cache.set(acct, { firstSeen, lastSeen })
        return
      } catch (err) {
        if (!isStatus(err, 404, 409, 412)) throw err
      }
    }
  }))
}

async function touchMachine(store, lim, machineId, nowIso, { ingested, heartbeat, collectorVersion }) {
  const entity = { partitionKey: 'm', rowKey: machineId, lastSeenAt: nowIso }
  if (ingested) entity.lastIngestAt = nowIso
  if (collectorVersion) entity.version = collectorVersion
  if (heartbeat) {
    // The label is public (SPEC "machines"); validateHeartbeat cleaned it.
    if (heartbeat.machineLabel) entity.label = heartbeat.machineLabel
    if (heartbeat.os) entity.os = heartbeat.os
    if (heartbeat.arch) entity.arch = heartbeat.arch
    if (heartbeat.version) entity.version = heartbeat.version
    if (heartbeat.tz) {
      entity.tzIana = heartbeat.tz.iana
      entity.cc = heartbeat.tz.cc
    }
    // The scanned homes stay on the row for the owner's diagnostics; the
    // public machine list is an allow-list (usage.js) and never reads them.
    if (heartbeat.homes) entity.homes = JSON.stringify(heartbeat.homes)
    entity.heartbeat = heartbeat.json
    entity.lastHeartbeatAt = nowIso
  }
  try {
    await lim(() => store.table('machines').merge(entity))
  } catch (err) {
    // The machine row was removed while this request was in flight.
    if (!isStatus(err, 404)) throw err
    return
  }
  const patch = {}
  if (entity.label) patch.label = entity.label
  if ('cc' in entity) patch.cc = entity.cc
  patchCachedMachine(store, machineId, patch)
}
